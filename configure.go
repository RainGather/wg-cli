package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/term"
)

type configPrompt struct {
	in     io.Reader
	reader *bufio.Reader
	out    io.Writer
}

func (p *configPrompt) ask(label, fallback string, secret bool) (string, error) {
	fmt.Fprint(p.out, label)
	if fallback != "" {
		fmt.Fprintf(p.out, " [%s]", fallback)
	}
	fmt.Fprint(p.out, ": ")
	var value string
	var err error
	if f, ok := p.in.(*os.File); secret && ok && p.reader.Buffered() == 0 && term.IsTerminal(int(f.Fd())) {
		var b []byte
		b, err = term.ReadPassword(int(f.Fd()))
		value = string(b)
		fmt.Fprintln(p.out)
	} else {
		value, err = p.reader.ReadString('\n')
		if err == io.EOF && len(value) > 0 {
			err = nil
		}
	}
	if err != nil {
		return "", fmt.Errorf("configuration input ended before completion: %w", err)
	}
	value = strings.TrimSpace(value)
	if value == "" {
		value = fallback
	}
	// Prevent terminal/piped values from injecting additional config fields.
	if strings.ContainsAny(value, "\r\n") {
		return "", fmt.Errorf("configuration values must be a single line")
	}
	return value, nil
}

func configureFile(path string, in io.Reader, out io.Writer) error {
	p := &configPrompt{in: in, reader: bufio.NewReader(in), out: out}
	fmt.Fprintf(out, "Configure WireGuard: %s\n1) Import an existing WireGuard .conf file\n2) Enter settings for one peer\n", path)
	mode, err := p.ask("Choose", "1", false)
	if err != nil {
		return err
	}
	var data []byte
	switch mode {
	case "1":
		source, err := p.ask("Source configuration path", "", false)
		if err != nil {
			return err
		}
		data, err = os.ReadFile(source)
		if err != nil {
			return fmt.Errorf("read source configuration: %w", err)
		}
	case "2":
		fields := []struct {
			name, fallback string
			secret         bool
		}{
			{"PrivateKey", "", true}, {"Address (comma-separated CIDRs)", "", false},
			{"DNS (comma-separated IPs)", "1.1.1.1", false}, {"MTU", "1420", false},
			{"PublicKey (server)", "", false}, {"PresharedKey (optional)", "", true},
			{"Endpoint (host:port)", "", false}, {"AllowedIPs", "0.0.0.0/0", false},
			{"PersistentKeepalive", "25", false},
		}
		values := make([]string, len(fields))
		for i, field := range fields {
			values[i], err = p.ask(field.name, field.fallback, field.secret)
			if err != nil {
				return err
			}
		}
		var b strings.Builder
		fmt.Fprintf(&b, "[Interface]\nPrivateKey = %s\nAddress = %s\nDNS = %s\nMTU = %s\n\n[Peer]\nPublicKey = %s\n", values[0], values[1], values[2], values[3], values[4])
		if values[5] != "" {
			fmt.Fprintf(&b, "PresharedKey = %s\n", values[5])
		}
		fmt.Fprintf(&b, "Endpoint = %s\nAllowedIPs = %s\nPersistentKeepalive = %s\n", values[6], values[7], values[8])
		data = []byte(b.String())
	default:
		return fmt.Errorf("choose 1 (import) or 2 (enter settings)")
	}
	if _, err := parseConfig(strings.NewReader(string(data))); err != nil {
		return fmt.Errorf("configuration not saved: %w", err)
	}
	if info, err := os.Lstat(path); err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("refusing to replace a non-regular file: %s", path)
		}
		answer, err := p.ask("Replace existing configuration? (yes/no)", "no", false)
		if err != nil {
			return err
		}
		if answer != "yes" {
			fmt.Fprintln(out, "Cancelled; existing configuration unchanged.")
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := saveConfig(path, data); err != nil {
		return err
	}
	fmt.Fprintf(out, "Saved configuration (0600): %s\nUse --check to validate; no connection has been tested.\n", path)
	return nil
}

func saveConfig(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".wg-cli-*") // 0600, including when replacing an existing file.
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
