package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestHandshakeHint(t *testing.T) {
	c := &config{peers: []peerConfig{
		{public: "broad", allowed: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}},
		{public: "specific", allowed: []netip.Prefix{netip.MustParsePrefix("10.65.0.0/16")}},
	}}
	status := "private_key=never-print-this\npublic_key=broad\nlast_handshake_time_sec=123\npublic_key=specific\nlast_handshake_time_sec=0\n"
	hint := handshakeHint(c, "10.65.3.3:8080", status)
	if !strings.Contains(hint, "no successful WireGuard handshake") || strings.Contains(hint, "never-print-this") {
		t.Fatal(hint)
	}
	hint = handshakeHint(c, "10.66.3.3:8080", status)
	if !strings.Contains(hint, "handshake succeeded") {
		t.Fatal(hint)
	}
	if got := handshakeHint(c, "192.168.0.1:80", status); got != "" {
		t.Fatal(got)
	}
}

func TestProxyReportsActualFailure(t *testing.T) {
	var stderr bytes.Buffer
	dial := func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("WireGuard route to 10.65.3.3:8080: connection refused")
	}
	raw, stop, err := startProxyWithErrors(dial, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	defer stop()
	u, _ := url.Parse(raw)
	tr := &http.Transport{Proxy: http.ProxyURL(u)}
	defer tr.CloseIdleConnections()
	res, err := (&http.Client{Transport: tr, Timeout: 2 * time.Second}).Get("http://10.65.3.3:8080/")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 502 || !strings.Contains(string(body), "connection refused") {
		t.Fatalf("%s: %s", res.Status, body)
	}
	if !strings.Contains(stderr.String(), "10.65.3.3:8080") {
		t.Fatal("error missing from stderr")
	}
	password, _ := u.User.Password()
	if strings.Contains(string(body)+stderr.String(), password) {
		t.Fatal("proxy credential disclosed")
	}
	// The diagnostic is also available for clients that discard CONNECT bodies.
	request, _ := http.NewRequest("CONNECT", raw, nil)
	request.URL.User = nil
	request.URL.Path = ""
	request.Host = "10.65.3.3:8080"
	request.SetBasicAuth("wg", password)
	request.Header.Set("Proxy-Authorization", request.Header.Get("Authorization"))
	request.Header.Del("Authorization")
	c, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	if err = request.Write(c); err != nil {
		t.Fatal(err)
	}
	// Reading until the complete body synchronizes with the server's stderr write.
	// Request a closed connection so the response can be consumed without a hang.
	buf := make([]byte, 4096)
	n, err := c.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(buf[:n]), "502") {
		t.Fatalf("CONNECT response: %s", buf[:n])
	}
}
