package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"golang.zx2c4.com/wireguard/conn"
	"golang.zx2c4.com/wireguard/device"
	"golang.zx2c4.com/wireguard/tun/netstack"
)

type dialFunc func(context.Context, string, string) (net.Conn, error)

func openTunnel(c *config) (dialFunc, func(), error) {
	ipc, err := c.ipc()
	if err != nil {
		return nil, nil, err
	}
	tun, n, err := netstack.CreateNetTUN(c.addresses, c.dns, c.mtu)
	if err != nil {
		return nil, nil, err
	}
	dev := device.NewDevice(tun, conn.NewDefaultBind(), device.NewLogger(device.LogLevelSilent, ""))
	if err = dev.IpcSet(ipc); err == nil {
		err = dev.Up()
	}
	if err != nil {
		dev.Close()
		return nil, nil, err
	}
	tunnelDial := func(ctx context.Context, network, address string) (net.Conn, error) {
		connection, err := n.DialContext(ctx, network, address)
		if err == nil {
			return connection, nil
		}
		status, statusErr := dev.IpcGet()
		if statusErr != nil {
			return nil, err
		}
		if hint := handshakeHint(c, address, status); hint != "" {
			return nil, fmt.Errorf("%w (%s)", err, hint)
		}
		return nil, err
	}
	router := &splitRouter{config: c, tunnel: tunnelDial, direct: (&net.Dialer{}).DialContext}
	return router.DialContext, dev.Close, nil
}

// Inspect only selected IPC fields. Never expose raw IPC, which contains keys.
func handshakeHint(c *config, address, status string) string {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return ""
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return ""
	}
	key := ""
	longest := -1
	for _, peer := range c.peers {
		for _, prefix := range peer.allowed {
			if prefix.Contains(ip.Unmap()) && prefix.Bits() >= longest {
				key, longest = peer.public, prefix.Bits()
			}
		}
	}
	if key == "" {
		return ""
	}
	selected := false
	for _, line := range strings.Split(status, "\n") {
		name, value, _ := strings.Cut(line, "=")
		if name == "public_key" {
			selected = value == key
		}
		if selected && name == "last_handshake_time_sec" {
			seconds, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return ""
			}
			if seconds == 0 {
				return "no successful WireGuard handshake; check Endpoint, keys, UDP reachability and server peer configuration"
			}
			return "WireGuard handshake succeeded; check target service, port, routing and firewall"
		}
	}
	return ""
}
