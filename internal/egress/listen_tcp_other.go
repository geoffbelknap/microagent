//go:build !linux

package egress

import "net"

func listenTCP(addr string) (net.Listener, error) {
	return net.Listen("tcp", addr)
}
