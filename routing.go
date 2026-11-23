package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"time"
)

type splitRouter struct {
	config         *config
	tunnel, direct dialFunc
}

// IPs are selected once; a failed tunnel connection never retries that IP directly.
func (r *splitRouter) dialIP(ctx context.Context, network string, ip netip.Addr, port string) (net.Conn, error) {
	ip = ip.Unmap()
	dial := r.direct
	if r.config.usesTunnel(ip) {
		dial = r.tunnel
	}
	return dial(ctx, network, net.JoinHostPort(ip.String(), port))
}

func (r *splitRouter) lookup(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{ip.Unmap()}, nil
	}
	family := "ip"
	if network == "tcp4" || network == "udp4" {
		family = "ip4"
	}
	if network == "tcp6" || network == "udp6" {
		family = "ip6"
	}
	var lastErr error
	for _, server := range r.config.dns {
		// Resolve against the configured DNS IP, using the same split policy as traffic.
		// Never use the system resolver to resolve a target name or retry DNS directly
		// when this DNS server belongs to AllowedIPs.
		resolver := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return r.dialIP(ctx, network, server, "53")
		}}
		queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		ips, err := resolver.LookupNetIP(queryCtx, family, host)
		cancel()
		if err == nil && len(ips) > 0 {
			return ips, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no addresses returned by configured DNS")
	}
	return nil, lastErr
}

func (r *splitRouter) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6", "udp", "udp4", "udp6":
	default:
		return nil, net.UnknownNetworkError(network)
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return nil, fmt.Errorf("invalid numeric port")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ips, err := r.lookup(ctx, network, host)
	if err != nil {
		return nil, err
	}
	var lastErr error
	for i, ip := range ips {
		ip = ip.Unmap()
		if (network == "tcp4" || network == "udp4") && !ip.Is4() {
			continue
		}
		if (network == "tcp6" || network == "udp6") && !ip.Is6() {
			continue
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		deadline, _ := ctx.Deadline()
		budget := time.Until(deadline) / time.Duration(len(ips)-i)
		attempt, cancel := context.WithTimeout(ctx, budget)
		c, err := r.dialIP(attempt, network, ip, port)
		cancel()
		if err == nil {
			return c, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no addresses for %s", network)
	}
	return nil, lastErr
}
