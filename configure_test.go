package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfigPath(t *testing.T) {
	if got := defaultConfigPath("/termux/bin/wg-cli", ""); got != "/termux/bin/wg.conf" {
		t.Fatal(got)
	}
	if got := defaultConfigPath("/termux/bin/wg-cli", "custom.conf"); got != "custom.conf" {
		t.Fatal(got)
	}
}

func TestConfigureImport(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.conf")
	if err := os.WriteFile(source, []byte(validConfig()), 0600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "nested", "wg.conf")
	var out bytes.Buffer
	if err := configureFile(path, strings.NewReader("1\n"+source+"\n"), &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != validConfig() {
		t.Fatal("import changed configuration")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("permissions: %o", info.Mode().Perm())
	}
	if strings.Contains(out.String(), base64.StdEncoding.EncodeToString(make([]byte, 32))) {
		t.Fatal("key leaked in prompts")
	}
	// Default response preserves the existing file, including when importing itself.
	if err := configureFile(path, strings.NewReader("1\n"+source+"\n\n"), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Cancelled") {
		t.Fatal("replacement not cancelled")
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if err := configureFile(path, strings.NewReader("1\n"+source+"\nyes\n"), &out); err != nil {
		t.Fatal(err)
	}
	info, _ = os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("replacement failed to restrict permissions")
	}
	leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(path), ".wg-cli-*"))
	if len(leftovers) != 0 {
		t.Fatal(leftovers)
	}
}

func TestConfigureManual(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	path := filepath.Join(t.TempDir(), "wg.conf")
	input := "2\n" + key + "\n10.0.0.2/32\n\n\n" + key + "\n\n127.0.0.1:51820\n\n\n"
	var out bytes.Buffer
	if err := configureFile(path, strings.NewReader(input), &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := parseConfig(strings.NewReader(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if c.mtu != 1420 || c.peers[0].keepalive != 25 || c.dns[0].String() != "1.1.1.1" {
		t.Fatal("wizard defaults incorrect")
	}
	if strings.Contains(out.String(), key) {
		t.Fatal("wizard printed a key")
	}
}

func TestConfigureRejectsInvalidWithoutChangingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "wg.conf")
	source := filepath.Join(dir, "bad.conf")
	if err := os.WriteFile(path, []byte(validConfig()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(source, []byte("[Interface]\nPrivateKey = secret\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, input := range []string{"1\n" + source + "\n", "2\n", "unknown\n"} {
		var out bytes.Buffer
		err := configureFile(path, strings.NewReader(input), &out)
		if err == nil {
			t.Fatal("bad/incomplete input accepted")
		}
		if strings.Contains(err.Error()+out.String(), "secret") {
			t.Fatal("invalid key disclosed")
		}
		data, _ := os.ReadFile(path)
		if string(data) != validConfig() {
			t.Fatal("existing file changed on error")
		}
	}
}

func TestConfigureRefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.conf")
	path := filepath.Join(dir, "wg.conf")
	if err := os.WriteFile(source, []byte(validConfig()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(source, path); err != nil {
		t.Fatal(err)
	}
	if err := configureFile(path, strings.NewReader("1\n"+source+"\nyes\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("symlink target accepted")
	}
}

func TestConfigModeConflicts(t *testing.T) {
	for _, args := range [][]string{{"--config", "--check"}, {"--config", "--dial", "host:80"}, {"--config", "git"}} {
		if code := run(args, &bytes.Buffer{}); code != 2 {
			t.Fatalf("%v: got %d", args, code)
		}
	}
}
