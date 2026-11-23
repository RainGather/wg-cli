package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

// Two actual WireGuard devices exchange encrypted UDP over loopback. The
// upstream HTTP and DNS servers exist only inside the server's user-space stack.
func testTunnel(t *testing.T) dialFunc {
	t.Helper()
	serverKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverIP := netip.MustParseAddr("10.88.0.1")
	clientIP := netip.MustParseAddr("10.88.0.2")
	tun, n, err := netstack.CreateNetTUN([]netip.Addr{serverIP}, nil, 1420)
	if err != nil {
		t.Fatal(err)
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	t.Cleanup(dev.Close)
	ipc := fmt.Sprintf("private_key=%s\nlisten_port=0\npublic_key=%s\nallowed_ip=%s/32\n", hex.EncodeToString(serverKey.Bytes()), hex.EncodeToString(clientKey.PublicKey().Bytes()), clientIP)
	if err = dev.IpcSet(ipc); err != nil {
		t.Fatal(err)
	}
	if err = dev.Up(); err != nil {
		t.Fatal(err)
	}
	status, err := dev.IpcGet()
	if err != nil {
		t.Fatal(err)
	}
	port := ""
	for _, line := range strings.Split(status, "\n") {
		if strings.HasPrefix(line, "listen_port=") {
			port = strings.TrimPrefix(line, "listen_port=")
		}
	}
	if port == "" || port == "0" {
		t.Fatal("server did not bind")
	}
	dns, err := n.ListenUDPAddrPort(netip.AddrPortFrom(serverIP, 53))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { dns.Close() })
	go func() {
		b := make([]byte, 1500)
		for {
			size, addr, err := dns.ReadFrom(b)
			if err != nil {
				return
			}
			var q dnsmessage.Message
			if q.Unpack(b[:size]) != nil {
				continue
			}
			res := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, RecursionAvailable: true}, Questions: q.Questions}
			for _, question := range q.Questions {
				if question.Type == dnsmessage.TypeA {
					res.Answers = append(res.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: serverIP.As4()}})
				}
			}
			packet, err := res.Pack()
			if err == nil {
				_, _ = dns.WriteTo(packet, addr)
			}
		}
	}()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy credential leaked upstream")
		}
		switch r.URL.Path {
		case "/repo/info/refs":
			fmt.Fprint(w, "1111111111111111111111111111111111111111\trefs/heads/main\n")
		case "/repo/HEAD":
			fmt.Fprint(w, "ref: refs/heads/main\n")
		case "/":
			w.Header().Set("Content-Type", "text/html")
			fmt.Fprint(w, `<html><body><a href="hello.txt">hello.txt</a></body></html>`)
		default:
			fmt.Fprint(w, "wireguard-ok")
		}
	})
	ln, err := n.ListenTCPAddrPort(netip.AddrPortFrom(serverIP, 80))
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	// Obtain a test certificate and serve TLS entirely within the WireGuard stack.
	certServer := httptest.NewTLSServer(handler)
	tlsConfig := certServer.TLS.Clone()
	certServer.Close()
	tlsLn, err := n.ListenTCPAddrPort(netip.AddrPortFrom(serverIP, 443))
	if err != nil {
		t.Fatal(err)
	}
	tlsSrv := &http.Server{Handler: handler}
	go tlsSrv.Serve(tls.NewListener(tlsLn, tlsConfig))
	t.Cleanup(func() { tlsSrv.Close() })
	c := &config{private: hex.EncodeToString(clientKey.Bytes()), addresses: []netip.Addr{clientIP}, dns: []netip.Addr{serverIP}, mtu: 1420, peers: []peerConfig{{public: hex.EncodeToString(serverKey.PublicKey().Bytes()), endpoint: "127.0.0.1:" + port, allowed: []netip.Prefix{netip.PrefixFrom(serverIP, 32)}}}}
	dial, close, err := openTunnel(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(close)
	return dial
}

func TestWireGuardProxy(t *testing.T) {
	dial := testTunnel(t)
	proxyURL, closeProxy, err := startProxy(dial)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(closeProxy)
	u, _ := url.Parse(proxyURL)
	t.Run("stdio forwarding", func(t *testing.T) {
		var out bytes.Buffer
		err := stdioDial(context.Background(), dial, "files.wg:80", strings.NewReader("GET /hello.txt HTTP/1.1\r\nHost: files.wg\r\nConnection: close\r\n\r\n"), &out)
		if err != nil || !strings.Contains(out.String(), "wireguard-ok") {
			t.Fatalf("stdio: %v %s", err, out.String())
		}
	})
	for _, scheme := range []string{"http", "https"} {
		t.Run(scheme, func(t *testing.T) {
			tr := &http.Transport{Proxy: http.ProxyURL(u), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			defer tr.CloseIdleConnections() // Test certificate only.
			client := &http.Client{Transport: tr, Timeout: 15 * time.Second}
			res, err := client.Get(scheme + "://files.wg/hello.txt")
			if err != nil {
				t.Fatal(err)
			}
			defer res.Body.Close()
			body, _ := io.ReadAll(res.Body)
			if res.StatusCode != 200 || string(body) != "wireguard-ok" {
				t.Fatalf("%s %s", res.Status, body)
			}
		})
	}
	t.Run("unauthenticated", func(t *testing.T) {
		clean := *u
		clean.User = nil
		tr := &http.Transport{Proxy: http.ProxyURL(&clean)}
		defer tr.CloseIdleConnections()
		res, err := (&http.Client{Transport: tr, Timeout: time.Second}).Get("http://files.wg/")
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 407 {
			t.Fatal(res.Status)
		}
	})
	t.Run("failure has no direct fallback", func(t *testing.T) {
		// A reachable host listener outside AllowedIPs must never receive a connection.
		ln, err := net.Listen("tcp4", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		if c, err := dial(ctx, "tcp", ln.Addr().String()); err == nil {
			c.Close()
			t.Fatal("unexpected direct fallback")
		}
	})
	for _, tool := range []string{"git", "rclone"} {
		t.Run(tool, func(t *testing.T) {
			path, err := exec.LookPath(tool)
			if err != nil {
				t.Skip(tool + " unavailable")
			}
			var args []string
			if tool == "git" {
				args = []string{"-c", "http.proxy=" + proxyURL, "ls-remote", "http://files.wg/repo"}
			} else {
				args = []string{"lsf", ":http:", "--http-url", "http://files.wg/", "--config", os.DevNull}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, path, args...)
			cmd.Env = proxyEnv(os.Environ(), proxyURL)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			want := "refs/heads/main"
			if tool == "rclone" {
				want = "hello.txt"
			}
			if !strings.Contains(string(out), want) {
				t.Fatalf("unexpected output: %s", out)
			}
		})
	}
}

func TestConnectBufferedBytes(t *testing.T) {
	dial := func(ctx context.Context, network, address string) (net.Conn, error) {
		a, b := net.Pipe()
		go func() {
			defer b.Close()
			buf := make([]byte, 4)
			if _, err := io.ReadFull(b, buf); err == nil {
				b.Write(buf)
			}
		}()
		return a, nil
	}
	raw, close, err := startProxy(dial)
	if err != nil {
		t.Fatal(err)
	}
	defer close()
	u, _ := url.Parse(raw)
	c, err := net.Dial("tcp", u.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	req, _ := http.NewRequest("CONNECT", "http://example.com:443", nil)
	req.Host = "example.com:443"
	password, _ := u.User.Password()
	req.Header.Set("Proxy-Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte("wg:"+password)))
	var b strings.Builder
	req.Write(&b)
	fmt.Fprint(c, b.String()+"PING")
	reader := bufio.NewReader(c)
	res, err := http.ReadResponse(reader, req)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 200 {
		t.Fatal(res.Status)
	}
	data := make([]byte, 4)
	if _, err = io.ReadFull(reader, data); err != nil || string(data) != "PING" {
		t.Fatalf("buffered bytes lost: %q %v", data, err)
	}
}

func TestChildExitStatus(t *testing.T) {
	for _, code := range []int{0, 37} {
		var stderr bytes.Buffer
		if got := runChild([]string{"/bin/sh", "-c", fmt.Sprintf("exit %d", code)}, os.Environ(), &stderr); got != code {
			t.Fatalf("want %d, got %d: %s", code, got, &stderr)
		}
	}
	if got := runChild([]string{"/wg-cli-nonexistent-command"}, os.Environ(), io.Discard); got != 127 {
		t.Fatalf("missing command exit: %d", got)
	}
}

func TestProxyShutdown(t *testing.T) {
	raw, stop, err := startProxy(func(context.Context, string, string) (net.Conn, error) {
		return nil, fmt.Errorf("no upstream")
	})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(raw)
	stop()
	c, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err == nil {
		c.Close()
		t.Fatal("proxy still listening after shutdown")
	}
}
