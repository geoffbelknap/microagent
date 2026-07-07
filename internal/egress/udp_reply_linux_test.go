//go:build linux

package egress

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// skipWithoutTransparentCap skips the test when this process cannot set
// IP_TRANSPARENT (needs CAP_NET_ADMIN in the current netns's user namespace).
// Run the suite as root or under `unshare -Ur` to exercise these tests; a
// plain unprivileged `go test` skips them.
func skipWithoutTransparentCap(t *testing.T) {
	t.Helper()
	fd, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM, 0)
	if err != nil {
		t.Fatalf("probe socket: %v", err)
	}
	defer unix.Close(fd)
	if err := unix.SetsockoptInt(fd, unix.IPPROTO_IP, unix.IP_TRANSPARENT, 1); err != nil {
		if errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES) {
			t.Skipf("IP_TRANSPARENT unavailable (no CAP_NET_ADMIN): %v", err)
		}
		t.Fatalf("probe IP_TRANSPARENT: %v", err)
	}
}

// TestTransparentReplyCoexistsWithWildcardPortSquatter reproduces the pasta
// port-mirror collision that broke guarded-egress DNS on hosts with a local
// UDP :53 service (e.g. systemd-resolved on GCE Ubuntu): pasta mirrors every
// host-bound UDP port into the workspace netns as a wildcard SO_REUSEADDR
// listener, and transparentReply must bind the SAME port (the resolver's :53)
// at a specific foreign address to spoof the reply source. Without
// SO_REUSEADDR on the reply socket that bind fails EADDRINUSE and every DNS
// answer to the guest is silently dropped.
//
// The squatter here is a wildcard SO_REUSEADDR UDP socket on an ephemeral
// port — exactly what pasta's mirrored listener looks like to bind conflict
// resolution (pasta sets SO_REUSEADDR on its mirrored sockets; a specific-addr
// bind coexists with a wildcard bind only when BOTH carry it). origDst uses
// 127.0.0.1 so the spoofed-source datagram is routable over lo without
// per-netns TPROXY routing; the IP_TRANSPARENT + bind + send path exercised is
// identical to the foreign-resolver case.
func TestTransparentReplyCoexistsWithWildcardPortSquatter(t *testing.T) {
	skipWithoutTransparentCap(t)

	// The "guest": a plain UDP receiver standing in for the guest's stub resolver.
	guest, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen guest receiver: %v", err)
	}
	defer guest.Close()
	guestSrc := netip.MustParseAddrPort(guest.LocalAddr().String())

	// The squatter: wildcard + SO_REUSEADDR on an ephemeral port, then reply to
	// the guest from origDst = 127.0.0.1 on that SAME port.
	var ctrlErr error
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			if cerr := c.Control(func(fd uintptr) {
				ctrlErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			}); cerr != nil {
				return cerr
			}
			return ctrlErr
		},
	}
	squatter, err := lc.ListenPacket(context.Background(), "udp4", "0.0.0.0:0")
	if err != nil {
		t.Fatalf("listen wildcard squatter: %v", err)
	}
	defer squatter.Close()
	squatPort := netip.MustParseAddrPort(squatter.LocalAddr().String()).Port()
	origDst := netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), squatPort)

	payload := []byte("spoofed-dns-answer")
	if err := transparentReply(origDst, guestSrc, payload); err != nil {
		t.Fatalf("transparentReply with wildcard squatter on port %d: %v", squatPort, err)
	}

	// The guest must receive the payload with source = origDst (the spoofed
	// resolver address), not the mediator's own address.
	_ = guest.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, from, err := guest.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("guest read: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Errorf("guest payload = %q, want %q", buf[:n], payload)
	}
	if got := netip.MustParseAddrPort(from.String()); got != origDst {
		t.Errorf("reply source = %v, want spoofed origDst %v", got, origDst)
	}
}

// TestTransparentReplyConcurrentSameOrigDst proves two replies spoofing the
// SAME origDst can be in flight at once: a guest's parallel A and AAAA queries
// to one resolver produce two transparentReply calls binding the identical
// foreign addr:port. SO_REUSEADDR permits the duplicate datagram binds; before
// it, the second bind raced EADDRINUSE against the first socket's lifetime.
func TestTransparentReplyConcurrentSameOrigDst(t *testing.T) {
	skipWithoutTransparentCap(t)

	guest, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("listen guest receiver: %v", err)
	}
	defer guest.Close()
	guestSrc := netip.MustParseAddrPort(guest.LocalAddr().String())

	// Hold one transparent reply-style socket open on origDst for the duration,
	// simulating an in-flight sibling reply, then send through transparentReply.
	var ctrlErr error
	lc := net.ListenConfig{
		Control: func(_, _ string, c syscall.RawConn) error {
			if cerr := c.Control(func(fd uintptr) {
				if e := unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_TRANSPARENT, 1); e != nil {
					ctrlErr = e
					return
				}
				ctrlErr = unix.SetsockoptInt(int(fd), unix.SOL_SOCKET, unix.SO_REUSEADDR, 1)
			}); cerr != nil {
				return cerr
			}
			return ctrlErr
		},
	}
	sibling, err := lc.ListenPacket(context.Background(), "udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen sibling reply socket: %v", err)
	}
	defer sibling.Close()
	origDst := netip.MustParseAddrPort(sibling.LocalAddr().String())

	payload := []byte("second-answer")
	if err := transparentReply(origDst, guestSrc, payload); err != nil {
		t.Fatalf("transparentReply while sibling reply socket holds %v: %v", origDst, err)
	}

	_ = guest.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 512)
	n, from, err := guest.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("guest read: %v", err)
	}
	if string(buf[:n]) != string(payload) {
		t.Errorf("guest payload = %q, want %q", buf[:n], payload)
	}
	if got := netip.MustParseAddrPort(from.String()); got != origDst {
		t.Errorf("reply source = %v, want %v", got, origDst)
	}
}
