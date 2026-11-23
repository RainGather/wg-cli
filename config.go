package main

import (
	"bufio"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strconv"
	"strings"
)

type peerConfig struct {
	public, preshared, endpoint string
	allowed                     []netip.Prefix
	keepalive                   int
}
type config struct {
	private        string
	addresses, dns []netip.Addr
	mtu            int
	peers          []peerConfig
}

func keyHex(s string) (string, error) {
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != 32 {
		return "", fmt.Errorf("expected a base64-encoded 32-byte WireGuard key")
	}
	return hex.EncodeToString(b), nil
}

// Reject unsupported wg-quick hooks rather than executing commands from a config.
func parseConfig(r io.Reader) (*config, error) {
	c := &config{mtu: 1420}
	section := ""
	var p *peerConfig
	scan := bufio.NewScanner(r)
	line := 0
	for scan.Scan() {
		line++
		s := strings.TrimSpace(strings.SplitN(scan.Text(), "#", 2)[0])
		if s == "" {
			continue
		}
		if strings.HasPrefix(s, "[") {
			switch s {
			case "[Interface]":
				if section != "" {
					return nil, fmt.Errorf("line %d: Interface must appear once, before peers", line)
				}
				section = "Interface"
			case "[Peer]":
				if section == "" {
					return nil, fmt.Errorf("line %d: missing Interface", line)
				}
				section = "Peer"
				c.peers = append(c.peers, peerConfig{})
				p = &c.peers[len(c.peers)-1]
			default:
				return nil, fmt.Errorf("line %d: unsupported section", line)
			}
			continue
		}
		k, v, ok := strings.Cut(s, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if !ok || section == "" {
			return nil, fmt.Errorf("line %d: expected Key = Value in a section", line)
		}
		var err error
		if section == "Interface" {
			switch k {
			case "PrivateKey":
				c.private, err = keyHex(v)
			case "Address":
				for _, a := range strings.Split(v, ",") {
					var prefix netip.Prefix
					prefix, err = netip.ParsePrefix(strings.TrimSpace(a))
					if err != nil {
						break
					}
					c.addresses = append(c.addresses, prefix.Addr())
				}
			case "DNS":
				for _, a := range strings.Split(v, ",") {
					var ip netip.Addr
					ip, err = netip.ParseAddr(strings.TrimSpace(a))
					if err != nil {
						break
					}
					c.dns = append(c.dns, ip)
				}
			case "MTU":
				c.mtu, err = strconv.Atoi(v)
				if err == nil && (c.mtu < 1280 || c.mtu > 65535) {
					err = fmt.Errorf("MTU must be 1280..65535")
				}
			case "ListenPort":
				var n int
				n, err = strconv.Atoi(v)
				if err == nil && (n < 0 || n > 65535) {
					err = fmt.Errorf("invalid ListenPort")
				} // Client always uses an ephemeral local port.
			default:
				err = fmt.Errorf("unsupported Interface field %s", k)
			}
		} else {
			switch k {
			case "PublicKey":
				p.public, err = keyHex(v)
			case "PresharedKey":
				p.preshared, err = keyHex(v)
			case "Endpoint":
				p.endpoint = v
				var host, port string
				host, port, err = net.SplitHostPort(v)
				if err == nil {
					var n int
					n, err = strconv.Atoi(port)
					if err == nil && (host == "" || n < 1 || n > 65535) {
						err = fmt.Errorf("invalid Endpoint")
					}
				}
			case "AllowedIPs":
				for _, a := range strings.Split(v, ",") {
					var prefix netip.Prefix
					prefix, err = netip.ParsePrefix(strings.TrimSpace(a))
					if err != nil {
						break
					}
					p.allowed = append(p.allowed, prefix.Masked())
				}
			case "PersistentKeepalive":
				p.keepalive, err = strconv.Atoi(v)
				if err == nil && (p.keepalive < 0 || p.keepalive > 65535) {
					err = fmt.Errorf("invalid keepalive")
				}
			default:
				err = fmt.Errorf("unsupported Peer field %s", k)
			}
		}
		// Never include field values: they may be private keys.
		if err != nil {
			return nil, fmt.Errorf("line %d: invalid %s", line, k)
		}
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	if c.private == "" || len(c.addresses) == 0 || len(c.peers) == 0 {
		return nil, fmt.Errorf("PrivateKey, Address and at least one Peer are required")
	}
	for _, p := range c.peers {
		if p.public == "" || p.endpoint == "" || len(p.allowed) == 0 {
			return nil, fmt.Errorf("each Peer requires PublicKey, Endpoint and AllowedIPs")
		}
	}
	if len(c.dns) == 0 {
		return nil, fmt.Errorf("DNS must contain an IP address reachable through AllowedIPs")
	}
	for _, dns := range c.dns {
		reachable := false
		for _, p := range c.peers {
			for _, prefix := range p.allowed {
				if prefix.Contains(dns) {
					reachable = true
				}
			}
		}
		sameFamily := false
		for _, a := range c.addresses {
			if a.Is4() == dns.Is4() {
				sameFamily = true
			}
		}
		if !reachable || !sameFamily {
			return nil, fmt.Errorf("DNS %s is not reachable with the configured Address and AllowedIPs", dns)
		}
	}
	return c, nil
}

func (c *config) ipc() (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "private_key=%s\nreplace_peers=true\n", c.private)
	for _, p := range c.peers {
		// Only the outer WireGuard endpoint uses the host resolver.
		addr, err := net.ResolveUDPAddr("udp", p.endpoint)
		if err != nil {
			return "", fmt.Errorf("resolve WireGuard Endpoint: %w", err)
		}
		fmt.Fprintf(&b, "public_key=%s\nendpoint=%s\nreplace_allowed_ips=true\n", p.public, addr)
		if p.preshared != "" {
			fmt.Fprintf(&b, "preshared_key=%s\n", p.preshared)
		}
		for _, a := range p.allowed {
			fmt.Fprintf(&b, "allowed_ip=%s\n", a)
		}
		fmt.Fprintf(&b, "persistent_keepalive_interval=%d\n", p.keepalive)
	}
	return b.String(), nil
}
