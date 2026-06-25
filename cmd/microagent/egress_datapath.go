package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/geoffbelknap/microagent/internal/applevfnet"
)

// runEgressDatapath is the hidden subprocess the apple-vf supervisor spawns to
// own the guest's host-fd NIC: it runs the userspace gVisor datapath over an
// inherited datagram socket fd carrying the guest's Ethernet frames. It exits
// when the socket closes or it receives SIGTERM/SIGINT (the supervisor signals
// it when the VM stops).
func runEgressDatapath(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("egress-datapath", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fdNum := fs.Int("fd", -1, "inherited datagram socket fd carrying guest Ethernet frames")
	gatewayIP := fs.String("gateway-ip", "", "IPv4 address the gateway owns and answers ARP for")
	gatewayMAC := fs.String("gateway-mac", "", "gateway MAC address (optional)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *fdNum < 0 {
		return fmt.Errorf("egress-datapath: --fd is required")
	}
	ctx, stop := signal.NotifyContext(ctx, syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	logf := func(format string, a ...any) {
		fmt.Fprintf(os.Stderr, "egress-datapath: "+format+"\n", a...)
	}
	return applevfnet.RunFromFD(ctx, *fdNum, *gatewayIP, *gatewayMAC, logf)
}
