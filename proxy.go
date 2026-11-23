package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type proxy struct {
	dial      dialFunc
	transport *http.Transport
	token     string
	mu        sync.Mutex
	active    map[net.Conn]bool
	closed    bool
}

func startProxy(dial dialFunc) (string, func(), error) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	var secret [24]byte
	if _, err = rand.Read(secret[:]); err != nil {
		ln.Close()
		return "", nil, err
	}
	token := base64.RawURLEncoding.EncodeToString(secret[:])
	p := &proxy{dial: dial, token: token, active: make(map[net.Conn]bool)}
	p.transport = &http.Transport{DialContext: dial, Proxy: nil, ForceAttemptHTTP2: false, MaxIdleConns: 100, IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 30 * time.Second}
	srv := &http.Server{Handler: p, ReadHeaderTimeout: 15 * time.Second, MaxHeaderBytes: 1 << 20}
	go func() { _ = srv.Serve(ln) }()
	close := func() {
		_ = srv.Close()
		p.transport.CloseIdleConnections()
		p.mu.Lock()
		p.closed = true
		for c := range p.active {
			_ = c.Close()
		}
		p.mu.Unlock()
	}
	return "http://wg:" + token + "@" + ln.Addr().String(), close, nil
}

func stripHop(h http.Header) {
	for _, v := range h.Values("Connection") {
		for _, k := range strings.Split(v, ",") {
			h.Del(strings.TrimSpace(k))
		}
	}
	for _, k := range []string{"Connection", "Proxy-Connection", "Proxy-Authorization", "Proxy-Authenticate", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade"} {
		h.Del(k)
	}
}

func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("wg:"+p.token))
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Proxy-Authorization")), []byte(auth)) != 1 {
		w.Header().Set("Proxy-Authenticate", `Basic realm="wg-cli"`)
		http.Error(w, "proxy authentication required", http.StatusProxyAuthRequired)
		return
	}
	if r.Method == http.MethodConnect {
		p.connect(w, r)
		return
	}
	if r.URL.Scheme != "http" || r.URL.Host == "" || r.URL.User != nil {
		http.Error(w, "expected an absolute http URL or CONNECT", 400)
		return
	}
	req := r.Clone(r.Context())
	req.RequestURI = ""
	stripHop(req.Header)
	res, err := p.transport.RoundTrip(req)
	if err != nil {
		http.Error(w, "WireGuard upstream connection failed", 502)
		return
	}
	defer res.Body.Close()
	stripHop(res.Header)
	for k, vs := range res.Header {
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	_, _ = io.Copy(w, res.Body)
}

func (p *proxy) connect(w http.ResponseWriter, r *http.Request) {
	if _, port, err := net.SplitHostPort(r.Host); err != nil || port == "" {
		http.Error(w, "CONNECT requires host:port", 400)
		return
	}
	upstream, err := p.dial(r.Context(), "tcp", r.Host)
	if err != nil {
		http.Error(w, "WireGuard upstream connection failed", 502)
		return
	}
	defer upstream.Close()
	client, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		return
	}
	defer client.Close()
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.active[client] = true
	p.active[upstream] = true
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.active, client); delete(p.active, upstream); p.mu.Unlock() }()
	if _, err = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	if err = rw.Flush(); err != nil {
		return
	}
	// rw.Reader preserves any TLS/SSH bytes already buffered after CONNECT.
	done := make(chan struct{})
	go func() {
		_, _ = io.Copy(upstream, rw)
		if c, ok := upstream.(interface{ CloseWrite() error }); ok {
			_ = c.CloseWrite()
		}
		close(done)
	}()
	_, _ = io.Copy(client, upstream)
	_ = client.Close()
	_ = upstream.Close()
	<-done
}

func stdioDial(ctx context.Context, dial dialFunc, address string, in io.Reader, out io.Writer) error {
	c, err := dial(ctx, "tcp", address)
	if err != nil {
		return err
	}
	defer c.Close()
	go func() {
		_, _ = io.Copy(c, in)
		if h, ok := c.(interface{ CloseWrite() error }); ok {
			_ = h.CloseWrite()
		}
	}()
	_, err = io.Copy(out, c)
	return err
}
