//go:build !windows

package applevfnet

import (
	"context"
	"os"
	"syscall"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv6"
)

// socketpair returns two connected SOCK_DGRAM unix sockets as *os.File, so each
// Read/Write carries exactly one Ethernet frame — matching the framing Apple's
// VZFileHandleNetworkDeviceAttachment uses.
func socketpair(t *testing.T) (*os.File, *os.File) {
	t.Helper()
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("socketpair: %v", err)
	}
	return os.NewFile(uintptr(fds[0]), "gw"), os.NewFile(uintptr(fds[1]), "vm")
}

func TestGatewayRegistersIPv6Address(t *testing.T) {
	gwEnd, vmEnd := socketpair(t)
	defer vmEnd.Close()
	addr6 := tcpip.AddrFromSlice([]byte{0xfd, 0, 0x6d, 0x69, 0x63, 0x72, 0, 0x7f, 0, 0, 0, 0, 0, 0, 0, 1})
	gw, err := newGateway(context.Background(), gwEnd, Config{
		GatewayIP:   tcpip.AddrFromSlice([]byte{192, 168, 127, 1}),
		GatewayIPv6: addr6,
	})
	if err != nil {
		t.Fatalf("newGateway: %v", err)
	}
	defer gw.Close()
	got, terr := gw.stack.GetMainNICAddress(nicID, ipv6.ProtocolNumber)
	if terr != nil {
		t.Fatalf("GetMainNICAddress IPv6: %v", terr)
	}
	if got.Address != addr6 {
		t.Fatalf("IPv6 gateway address = %v, want %v", got.Address, addr6)
	}
}

// TestGatewayAnswersARP proves the link pump + stack + ethernet/ARP work
// end-to-end over the datagram socket without a real VM: an ARP "who-has
// gateway" from the guest gets an ARP reply carrying the gateway MAC.
func TestGatewayAnswersARP(t *testing.T) {
	gwEnd, vmEnd := socketpair(t)
	defer vmEnd.Close()

	gwIP := tcpip.AddrFromSlice([]byte{192, 168, 127, 1})
	gwMAC := tcpip.LinkAddress("\x0a\x00\x00\x00\x00\x01")
	guestMAC := tcpip.LinkAddress("\x0a\x00\x00\x00\x00\x02")
	guestIP := [4]byte{192, 168, 127, 2}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = Run(ctx, gwEnd, Config{GatewayIP: gwIP, GatewayMAC: gwMAC}) }()

	// Build an ARP request frame: who has gwIP, tell guest.
	arpBody := make([]byte, header.ARPSize)
	a := header.ARP(arpBody)
	a.SetIPv4OverEthernet()
	a.SetOp(header.ARPRequest)
	copy(a.HardwareAddressSender(), guestMAC)
	copy(a.ProtocolAddressSender(), guestIP[:])
	copy(a.ProtocolAddressTarget(), gwIP.AsSlice())

	eth := make([]byte, header.EthernetMinimumSize)
	header.Ethernet(eth).Encode(&header.EthernetFields{
		SrcAddr: guestMAC,
		DstAddr: header.EthernetBroadcastAddress,
		Type:    header.ARPProtocolNumber,
	})
	if _, err := vmEnd.Write(append(eth, arpBody...)); err != nil {
		t.Fatalf("write ARP request: %v", err)
	}

	// Read frames until we see the ARP reply (or time out).
	_ = vmEnd.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, maxFrame)
	for {
		n, err := vmEnd.Read(buf)
		if err != nil {
			t.Fatalf("no ARP reply: %v", err)
		}
		if n < header.EthernetMinimumSize+header.ARPSize {
			continue
		}
		if header.Ethernet(buf[:header.EthernetMinimumSize]).Type() != header.ARPProtocolNumber {
			continue
		}
		reply := header.ARP(buf[header.EthernetMinimumSize:n])
		if !reply.IsValid() || reply.Op() != header.ARPReply {
			continue
		}
		if tcpip.LinkAddress(reply.HardwareAddressSender()) != gwMAC {
			t.Fatalf("ARP reply sender MAC = %x, want gateway %x", reply.HardwareAddressSender(), gwMAC)
		}
		senderIP := tcpip.AddrFromSlice(reply.ProtocolAddressSender())
		if senderIP != gwIP {
			t.Fatalf("ARP reply sender IP = %v, want gateway %v", senderIP, gwIP)
		}
		return // success
	}
}

func TestRunReturnsOnContextCancel(t *testing.T) {
	gwEnd, vmEnd := socketpair(t)
	defer vmEnd.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- Run(ctx, gwEnd, Config{
			GatewayIP: tcpip.AddrFromSlice([]byte{192, 168, 127, 1}),
		})
	}()

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned error after context cancel: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
