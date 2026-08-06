//go:build linux

package egress

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

// buildOrigDstCmsg hand-builds an IP_ORIGDSTADDR control message carrying the
// given IPv4 destination, mirroring what the kernel writes for a socket with
// IP_RECVORIGDSTADDR enabled. Layout: a unix.Cmsghdr (Level=IPPROTO_IP,
// Type=IP_ORIGDSTADDR) followed by a RawSockaddrInet4 (family host-order, port
// big-endian, 4-byte addr).
func buildOrigDstCmsg(ap netip.AddrPort) []byte {
	sa := unix.RawSockaddrInet4{
		Family: unix.AF_INET,
		Addr:   ap.Addr().As4(),
	}
	// Port is stored big-endian (network byte order) on the wire.
	binary.BigEndian.PutUint16((*[2]byte)(unsafe.Pointer(&sa.Port))[:], ap.Port())

	dataLen := int(unsafe.Sizeof(sa))
	oob := make([]byte, unix.CmsgSpace(dataLen))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
	h.Level = unix.IPPROTO_IP
	h.Type = unix.IP_ORIGDSTADDR
	h.SetLen(unix.CmsgLen(dataLen))

	// Copy the sockaddr into the cmsg data area (just past the header).
	dst := oob[unix.CmsgLen(0) : unix.CmsgLen(0)+dataLen]
	copy(dst, (*[unsafe.Sizeof(sa)]byte)(unsafe.Pointer(&sa))[:])
	return oob
}

func TestParseUDPOrigDst(t *testing.T) {
	want := netip.MustParseAddrPort("203.0.113.5:443")
	oob := buildOrigDstCmsg(want)

	got, err := parseUDPOrigDst(oob)
	if err != nil {
		t.Fatalf("parseUDPOrigDst: %v", err)
	}
	if got != want {
		t.Fatalf("parseUDPOrigDst = %v, want %v", got, want)
	}
}

func TestParseUDPOrigDstV6(t *testing.T) {
	want := netip.MustParseAddrPort("[2001:db8::5]:443")
	sa := unix.RawSockaddrInet6{Family: unix.AF_INET6, Addr: want.Addr().As16()}
	binary.BigEndian.PutUint16((*[2]byte)(unsafe.Pointer(&sa.Port))[:], want.Port())
	dataLen := int(unsafe.Sizeof(sa))
	oob := make([]byte, unix.CmsgSpace(dataLen))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
	h.Level = unix.IPPROTO_IPV6
	h.Type = unix.IPV6_ORIGDSTADDR
	h.SetLen(unix.CmsgLen(dataLen))
	copy(oob[unix.CmsgLen(0):unix.CmsgLen(0)+dataLen], (*[unsafe.Sizeof(sa)]byte)(unsafe.Pointer(&sa))[:])

	got, err := parseUDPOrigDst(oob)
	if err != nil {
		t.Fatalf("parseUDPOrigDst v6: %v", err)
	}
	if got != want {
		t.Fatalf("parseUDPOrigDst v6 = %v, want %v", got, want)
	}
}

func TestParseUDPOrigDstMissing(t *testing.T) {
	// A control buffer with an unrelated cmsg (IP_PKTINFO) and no
	// IP_ORIGDSTADDR must yield an error, not a zero AddrPort.
	oob := make([]byte, unix.CmsgSpace(0))
	h := (*unix.Cmsghdr)(unsafe.Pointer(&oob[0]))
	h.Level = unix.IPPROTO_IP
	h.Type = unix.IP_PKTINFO
	h.SetLen(unix.CmsgLen(0))

	if _, err := parseUDPOrigDst(oob); err == nil {
		t.Fatal("expected error when IP_ORIGDSTADDR cmsg is absent")
	}
}

func TestParseUDPOrigDstEmpty(t *testing.T) {
	if _, err := parseUDPOrigDst(nil); err == nil {
		t.Fatal("expected error for empty oob buffer")
	}
}
