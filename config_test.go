package main

import (
	"encoding/base64"
	"strings"
	"testing"
)

func validConfig() string {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	return "[Interface]\nPrivateKey = " + key + "\nAddress = 10.0.0.2/32\nDNS = 10.0.0.1\n[Peer]\nPublicKey = " + key + "\nEndpoint = 127.0.0.1:51820\nAllowedIPs = 0.0.0.0/0\n"
}

func TestConfig(t *testing.T) {
	c, err := parseConfig(strings.NewReader(validConfig()))
	if err != nil {
		t.Fatal(err)
	}
	ipc, err := c.ipc()
	if err != nil || !strings.Contains(ipc, "allowed_ip=0.0.0.0/0") {
		t.Fatalf("IPC conversion: %v", err)
	}
	for name, s := range map[string]string{
		"private key":       strings.Replace(validConfig(), "PrivateKey = ", "PrivateKey = secret", 1),
		"hook":              validConfig() + "PostUp = touch /tmp/no\n",
		"dns outside route": strings.Replace(validConfig(), "0.0.0.0/0", "10.1.0.0/16", 1),
		"endpoint":          strings.Replace(validConfig(), "127.0.0.1:51820", "host:70000", 1),
		"missing DNS":       strings.Replace(validConfig(), "DNS = 10.0.0.1\n", "", 1),
		"MTU":               strings.Replace(validConfig(), "DNS =", "MTU = 42\nDNS =", 1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseConfig(strings.NewReader(s)); err == nil {
				t.Fatal("invalid config accepted")
			} else if strings.Contains(err.Error(), "secret") {
				t.Fatal("key disclosed")
			}
		})
	}
}

func TestProxyEnv(t *testing.T) {
	env := proxyEnv([]string{"http_proxy=old", "NO_PROXY=*", "RCLONE_HTTP_PROXY=old", "FOO=keep"}, "http://new")
	all := strings.Join(env, "\n")
	if strings.Contains(all, "old") || strings.Contains(all, "NO_PROXY=*") || !strings.Contains(all, "FOO=keep") || !strings.Contains(all, "RCLONE_HTTP_PROXY=http://new") {
		t.Fatal(all)
	}
}
