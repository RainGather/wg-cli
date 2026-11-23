package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func TestSplitRouting(t *testing.T) {
	c := &config{peers: []peerConfig{{allowed: []netip.Prefix{netip.MustParsePrefix("10.65.0.0/16"), netip.MustParsePrefix("fd00::/8")}}, {allowed: []netip.Prefix{netip.MustParsePrefix("192.168.9.0/24")}}}}
	for _, tt := range []struct {
		address string
		tunnel  bool
	}{
		{"10.65.3.3:80", true}, {"10.66.3.3:80", false}, {"223.5.5.5:53", false}, {"192.168.9.1:443", true}, {"[fd00::1]:443", true}, {"[2001:db8::1]:443", false}, {"[::ffff:10.65.3.3]:80", true},
	} {
		t.Run(tt.address, func(t *testing.T) {
			var tunnel, direct int
			r := &splitRouter{config: c, tunnel: func(context.Context, string, string) (net.Conn, error) {
				tunnel++
				return nil, fmt.Errorf("tunnel unavailable")
			}, direct: func(context.Context, string, string) (net.Conn, error) {
				direct++
				return nil, fmt.Errorf("direct unavailable")
			}}
			_, err := r.DialContext(context.Background(), "tcp", tt.address)
			if err == nil {
				t.Fatal("expected error")
			}
			if tt.tunnel && (tunnel != 1 || direct != 0) {
				t.Fatalf("tunnel=%d direct=%d", tunnel, direct)
			}
			if !tt.tunnel && (direct != 1 || tunnel != 0) {
				t.Fatalf("tunnel=%d direct=%d", tunnel, direct)
			}
		})
	}
	// A default route continues to keep every IPv4 address in the tunnel.
	c.peers[0].allowed = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0")}
	if !c.usesTunnel(netip.MustParseAddr("223.5.5.5")) {
		t.Fatal("default route not honored")
	}
}

func TestConfiguredDNSUsesSplitRouting(t *testing.T) {
	for _, dnsInTunnel := range []bool{false, true} {
		t.Run(fmt.Sprint(dnsInTunnel), func(t *testing.T) {
			server, err := net.ListenPacket("udp4", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			go func() {
				b := make([]byte, 2048)
				for {
					n, addr, err := server.ReadFrom(b)
					if err != nil {
						return
					}
					var q dnsmessage.Message
					if q.Unpack(b[:n]) != nil {
						continue
					}
					res := dnsmessage.Message{Header: dnsmessage.Header{ID: q.ID, Response: true, RecursionAvailable: true}, Questions: q.Questions}
					for _, question := range q.Questions {
						if question.Type == dnsmessage.TypeA {
							res.Answers = append(res.Answers, dnsmessage.Resource{Header: dnsmessage.ResourceHeader{Name: question.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60}, Body: &dnsmessage.AResource{A: [4]byte{10, 65, 3, 3}}})
						}
					}
					packet, err := res.Pack()
					if err == nil {
						server.WriteTo(packet, addr)
					}
				}
			}()
			allowed := []netip.Prefix{netip.MustParsePrefix("10.65.0.0/16")}
			if dnsInTunnel {
				allowed = append(allowed, netip.MustParsePrefix("223.5.5.5/32"))
			}
			c := &config{dns: []netip.Addr{netip.MustParseAddr("223.5.5.5")}, peers: []peerConfig{{allowed: allowed}}}
			var dnsTunnel, dnsDirect, targetTunnel atomic.Int32
			r := &splitRouter{config: c}
			makeDial := func(isTunnel bool) dialFunc {
				return func(ctx context.Context, network, address string) (net.Conn, error) {
					if address == "223.5.5.5:53" {
						if isTunnel {
							dnsTunnel.Add(1)
						} else {
							dnsDirect.Add(1)
						}
						return (&net.Dialer{}).DialContext(ctx, network, server.LocalAddr().String())
					}
					if isTunnel && address == "10.65.3.3:80" {
						targetTunnel.Add(1)
						a, b := net.Pipe()
						b.Close()
						return a, nil
					}
					return nil, fmt.Errorf("unexpected destination: %s", address)
				}
			}
			r.tunnel = makeDial(true)
			r.direct = makeDial(false)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			conn, err := r.DialContext(ctx, "tcp4", "split.example:80")
			if err != nil {
				t.Fatal(err)
			}
			conn.Close()
			if targetTunnel.Load() != 1 {
				t.Fatal("resolved target did not use tunnel")
			}
			if dnsInTunnel {
				if dnsTunnel.Load() == 0 || dnsDirect.Load() != 0 {
					t.Fatal("covered DNS escaped tunnel")
				}
			} else {
				if dnsDirect.Load() == 0 || dnsTunnel.Load() != 0 {
					t.Fatal("outside DNS did not connect directly")
				}
			}
		})
	}
}

func TestDNSFailureNeverUsesOtherRoute(t *testing.T) {
	c := &config{dns: []netip.Addr{netip.MustParseAddr("10.65.0.1")}, peers: []peerConfig{{allowed: []netip.Prefix{netip.MustParsePrefix("10.65.0.0/16")}}}}
	var direct atomic.Int32
	r := &splitRouter{config: c, tunnel: func(context.Context, string, string) (net.Conn, error) { return nil, fmt.Errorf("DNS unreachable") }, direct: func(context.Context, string, string) (net.Conn, error) {
		direct.Add(1)
		return nil, fmt.Errorf("unexpected direct connection")
	}}
	_, err := r.DialContext(context.Background(), "tcp", "unreachable.example:80")
	if err == nil || direct.Load() != 0 {
		t.Fatalf("err=%v direct=%d", err, direct.Load())
	}
	if !strings.Contains(err.Error(), "DNS unreachable") {
		t.Fatal(err)
	}
}
