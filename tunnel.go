package main

import (
	"context"
	"net"

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
	router := &splitRouter{config: c, tunnel: n.DialContext, direct: (&net.Dialer{}).DialContext}
	return router.DialContext, dev.Close, nil
}
