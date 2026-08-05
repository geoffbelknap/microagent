package egress

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"net"
	"net/netip"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestQUICV1InitialKeysRFC9001(t *testing.T) {
	dcid, _ := hex.DecodeString("8394c8f03e515708")
	key, iv, hp, err := quicInitialKeys(quicV1InitialSalt, dcid, "quic")
	if err != nil {
		t.Fatal(err)
	}
	assertHex := func(name string, got []byte, want string) {
		t.Helper()
		if hex.EncodeToString(got) != want {
			t.Fatalf("%s = %x, want %s", name, got, want)
		}
	}
	assertHex("key", key, "1f369613dd76d5467730efcbe3b1a22d")
	assertHex("iv", iv, "fa044b2f42a3fd3b46fb255c")
	assertHex("hp", hp, "9f50449e04a0e810283a1e9933adedd2")
}

func TestQUICV2InitialKeysRFC9369(t *testing.T) {
	dcid, _ := hex.DecodeString("8394c8f03e515708")
	key, iv, hp, err := quicInitialKeys(quicV2InitialSalt, dcid, "quicv2")
	if err != nil {
		t.Fatal(err)
	}
	for name, tt := range map[string]struct {
		got  []byte
		want string
	}{
		"key": {key, "8b1a0bc121284290a29e0971b5cd045d"},
		"iv":  {iv, "91f73e2351d8fa91660e909f"},
		"hp":  {hp, "45b95e15235d6f45a6b19cbcb0294ba9"},
	} {
		if hex.EncodeToString(tt.got) != tt.want {
			t.Fatalf("%s = %x, want %s", name, tt.got, tt.want)
		}
	}
}

func TestParseQUICInitialFramesCrypto(t *testing.T) {
	frames := []byte{0x01, 0x06, 0x05, 0x03, 'a', 'b', 'c', 0x00, 0x00}
	got, err := parseQUICInitialFrames(frames)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].offset != 5 || string(got[0].data) != "abc" {
		t.Fatalf("fragments = %#v", got)
	}
}

func TestParseClientHelloHandshakeSNI(t *testing.T) {
	c, s := net.Pipe()
	go func() {
		_ = tls.Client(c, &tls.Config{ServerName: "h3.example.com", InsecureSkipVerify: true}).Handshake()
		_ = c.Close()
	}()
	_ = s.SetReadDeadline(time.Now().Add(2 * time.Second))
	record := make([]byte, 4096)
	n, err := s.Read(record)
	if err != nil {
		t.Fatal(err)
	}
	record = record[:n]
	got, ok := parseClientHelloHandshakeSNI(record[5:])
	if !ok || got != "h3.example.com" {
		t.Fatalf("SNI = %q,%v", got, ok)
	}
}

func TestQUICVarint(t *testing.T) {
	for _, tt := range []struct {
		wire []byte
		want uint64
	}{
		{[]byte{0x25}, 37},
		{[]byte{0x7b, 0xbd}, 15293},
		{[]byte{0x9d, 0x7f, 0x3e, 0x7d}, 494878333},
		{[]byte{0xc2, 0x19, 0x7c, 0x5e, 0xff, 0x14, 0xe8, 0x8c}, 151288809941952652},
	} {
		got, n, ok := readQUICVarint(tt.wire)
		if !ok || n != len(tt.wire) || got != tt.want {
			t.Fatalf("readQUICVarint(%x) = %d,%d,%v; want %d", tt.wire, got, n, ok, tt.want)
		}
	}
}

func TestQUICInspectionReassemblesClientHello(t *testing.T) {
	handshake := testClientHelloHandshake(t, "example.com")
	cut := len(handshake) / 2
	dcid, _ := hex.DecodeString("8394c8f03e515708")
	first := protectTestQUICInitial(t, dcid, 0, quicCryptoFrame(0, handshake[:cut]))
	second := protectTestQUICInitial(t, dcid, 1, quicCryptoFrame(uint64(cut), handshake[cut:]))

	inspection := &quicInspection{}
	if host, complete, err := inspection.add(first); err != nil || complete || host != "" {
		t.Fatalf("first fragment = %q,%v,%v", host, complete, err)
	}
	host, complete, err := inspection.add(second)
	if err != nil || !complete || host != "example.com" {
		t.Fatalf("reassembled = %q,%v,%v", host, complete, err)
	}
}

func TestQUICInspectionIdleReapingKeepsActiveHandshake(t *testing.T) {
	p := newUDPProxyWithIdle(&Handler{Logger: &BufferLogger{}}, time.Second, time.Hour)
	defer p.closeAll()
	active := udpFlowKey{src: netip.MustParseAddrPort("10.0.0.5:50000"), origDst: netip.MustParseAddrPort("203.0.113.9:443")}
	stale := udpFlowKey{src: netip.MustParseAddrPort("10.0.0.5:50001"), origDst: netip.MustParseAddrPort("203.0.113.9:443")}
	p.quic[active] = &quicInspection{lastSeen: time.Now()}
	p.quic[stale] = &quicInspection{lastSeen: time.Now().Add(-2 * time.Second)}
	p.reapIdle()
	if p.quic[active] == nil {
		t.Fatal("active QUIC inspection was reaped")
	}
	if p.quic[stale] != nil {
		t.Fatal("stale QUIC inspection was retained")
	}
}

func TestQUICInspectionBufferBound(t *testing.T) {
	inspection := &quicInspection{bufferedBytes: maxQUICBufferedBytes}
	if _, _, err := inspection.add([]byte{0}); err == nil {
		t.Fatal("inspection accepted data after its byte bound")
	}
}

func TestQUICUDP443UsesSNIAndOrdinaryPolicy(t *testing.T) {
	guest := netip.MustParseAddrPort("10.0.0.5:52000")
	dst := netip.MustParseAddrPort("203.0.113.9:443")
	dcid, _ := hex.DecodeString("8394c8f03e515708")
	packet := protectTestQUICInitial(t, dcid, 0, quicCryptoFrame(0, testClientHelloHandshake(t, "example.com")))
	cache := NewNameCache()
	cache.Put("example.com", dst.Addr(), time.Minute)
	upstream := newScriptedPacketConn()
	log := &BufferLogger{}
	h := &Handler{
		Mode: "strict", Policy: mustPolicy(t), AllowlistLocked: true,
		NameCache: cache, Logger: log,
		OpenUDP: func(netip.AddrPort) (net.PacketConn, error) { return upstream, nil },
	}
	p := newUDPProxy(h)
	defer p.closeAll()
	p.handleUDPDatagram(guest, dst, packet)

	select {
	case write := <-upstream.writes:
		if write.to != dst || string(write.payload) != string(packet) {
			t.Fatalf("forwarded QUIC datagram = %#v", write)
		}
	case <-time.After(time.Second):
		t.Fatal("allowlisted QUIC Initial was not forwarded")
	}
	assertEventWithField(t, log, "egress_udp_allow", "host", "example.com")
	assertEventWithField(t, log, "egress_udp_allow", "protocol", "quic")
}

func TestQUICUDP443OffAllowlistUsesOrdinaryDeny(t *testing.T) {
	guest := netip.MustParseAddrPort("10.0.0.5:52000")
	dst := netip.MustParseAddrPort("203.0.113.9:443")
	dcid, _ := hex.DecodeString("8394c8f03e515708")
	packet := protectTestQUICInitial(t, dcid, 0, quicCryptoFrame(0, testClientHelloHandshake(t, "blocked.example")))
	cache := NewNameCache()
	cache.Put("blocked.example", dst.Addr(), time.Minute)
	opened := false
	log := &BufferLogger{}
	h := &Handler{
		Mode: "strict", Policy: mustPolicy(t), AllowlistLocked: true,
		NameCache: cache, Logger: log,
		OpenUDP: func(netip.AddrPort) (net.PacketConn, error) {
			opened = true
			return newScriptedPacketConn(), nil
		},
	}
	p := newUDPProxy(h)
	defer p.closeAll()
	p.handleUDPDatagram(guest, dst, packet)
	if opened {
		t.Fatal("off-allowlist QUIC opened an upstream association")
	}
	assertEventWithField(t, log, "egress_udp_deny", "host", "blocked.example")
	assertEventWithField(t, log, "egress_udp_deny", "signal", SignalDenied)
}

func TestQUICOpenSSLInitial(t *testing.T) {
	openssl, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is unavailable")
	}
	help, _ := exec.Command(openssl, "s_client", "-help").CombinedOutput()
	if !strings.Contains(string(help), "-quic") {
		t.Skip("openssl has no QUIC client")
	}
	listener, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, openssl, "s_client", "-quic", "-alpn", "h3", "-servername", "openssl.example", "-connect", listener.LocalAddr().String())
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Wait() }()
	inspection := &quicInspection{}
	_ = listener.SetReadDeadline(time.Now().Add(time.Second))
	packet := make([]byte, maxUDPDatagram)
	for {
		n, _, readErr := listener.ReadFromUDP(packet)
		if readErr != nil {
			t.Fatalf("OpenSSL QUIC Initial incomplete after %d datagrams: %v", len(inspection.buffered), readErr)
		}
		host, complete, inspectErr := inspection.add(packet[:n])
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if complete {
			if host != "openssl.example" {
				t.Fatalf("OpenSSL QUIC SNI = %q", host)
			}
			break
		}
	}
}

func testClientHelloHandshake(t *testing.T, host string) []byte {
	t.Helper()
	c, s := net.Pipe()
	go func() {
		_ = tls.Client(c, &tls.Config{ServerName: host, InsecureSkipVerify: true}).Handshake()
		_ = c.Close()
	}()
	_ = s.SetReadDeadline(time.Now().Add(2 * time.Second))
	record := make([]byte, 4096)
	n, err := s.Read(record)
	if err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), record[5:n]...)
}

func quicCryptoFrame(offset uint64, data []byte) []byte {
	p := []byte{0x06}
	p = appendTestQUICVarint(p, offset)
	p = appendTestQUICVarint(p, uint64(len(data)))
	return append(p, data...)
}

func protectTestQUICInitial(t *testing.T, dcid []byte, pn uint64, plain []byte) []byte {
	t.Helper()
	for len(plain) < 32 {
		plain = append(plain, 0)
	}
	const pnLen = 2
	header := []byte{0xc1, 0, 0, 0, 1, byte(len(dcid))}
	header = append(header, dcid...)
	header = append(header, 0, 0) // empty SCID and token
	header = appendTestQUICVarint(header, uint64(pnLen+len(plain)+16))
	pnOffset := len(header)
	header = append(header, byte(pn>>8), byte(pn))
	key, iv, hp, err := quicInitialKeys(quicV1InitialSalt, dcid, "quic")
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(key)
	aead, _ := cipher.NewGCM(block)
	nonce := append([]byte(nil), iv...)
	nonce[len(nonce)-2] ^= byte(pn >> 8)
	nonce[len(nonce)-1] ^= byte(pn)
	ciphertext := aead.Seal(nil, nonce, plain, header)
	packet := append(append([]byte(nil), header...), ciphertext...)
	hpBlock, _ := aes.NewCipher(hp)
	mask := make([]byte, aes.BlockSize)
	hpBlock.Encrypt(mask, packet[pnOffset+4:pnOffset+4+aes.BlockSize])
	packet[0] ^= mask[0] & 0x0f
	for i := 0; i < pnLen; i++ {
		packet[pnOffset+i] ^= mask[i+1]
	}
	return packet
}

func appendTestQUICVarint(dst []byte, value uint64) []byte {
	switch {
	case value < 1<<6:
		return append(dst, byte(value))
	case value < 1<<14:
		return binary.BigEndian.AppendUint16(dst, uint16(value)|(1<<14))
	case value < 1<<30:
		return binary.BigEndian.AppendUint32(dst, uint32(value)|(2<<30))
	default:
		return binary.BigEndian.AppendUint64(dst, value|(3<<62))
	}
}
