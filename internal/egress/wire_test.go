package egress

import (
	"bytes"
	"strings"
	"testing"
)

func TestDestHeaderRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		h    DestHeader
	}{
		{"tcp/hostname/443", DestHeader{Proto: "tcp", Host: "example.com", Port: 443}},
		{"udp/ipv4/53", DestHeader{Proto: "udp", Host: "10.0.0.5", Port: 53}},
		{"tcp/ipv6-literal/80", DestHeader{Proto: "tcp", Host: "2001:db8::1", Port: 80}},
		{"tcp/max-length-host/8080", DestHeader{Proto: "tcp", Host: strings.Repeat("a", 255), Port: 8080}},
		{"udp/port-65535", DestHeader{Proto: "udp", Host: "192.168.1.1", Port: 65535}},
		{"tcp/port-1", DestHeader{Proto: "tcp", Host: "host.internal", Port: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteDestHeader(&buf, tc.h); err != nil {
				t.Fatalf("WriteDestHeader: %v", err)
			}
			got, err := ReadDestHeader(&buf)
			if err != nil {
				t.Fatalf("ReadDestHeader: %v", err)
			}
			if got != tc.h {
				t.Fatalf("roundtrip mismatch: got %+v, want %+v", got, tc.h)
			}
			// Buffer must be fully consumed.
			if buf.Len() != 0 {
				t.Fatalf("ReadDestHeader left %d bytes in buffer", buf.Len())
			}
		})
	}
}

func TestReadDestHeaderRejectsMalformed(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{
			"empty buffer",
			[]byte{},
		},
		{
			"truncated after proto",
			[]byte{0x01}, // proto only, no host-len byte
		},
		{
			"truncated host bytes",
			[]byte{0x01, 0x05, 'h', 'e'}, // declares 5 host bytes, provides 2
		},
		{
			"truncated port",
			[]byte{0x01, 0x03, 'f', 'o', 'o'}, // host "foo" present, no port bytes
		},
		{
			"unknown proto byte",
			func() []byte {
				var buf bytes.Buffer
				_ = WriteDestHeader(&buf, DestHeader{Proto: "tcp", Host: "x.com", Port: 80})
				b := buf.Bytes()
				b[0] = 0xFF // corrupt proto
				return b
			}(),
		},
		{
			"empty host (declared len 0)",
			[]byte{0x01, 0x00, 0x00, 0x50}, // proto=tcp, hostlen=0, port=80
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := ReadDestHeader(bytes.NewReader(tc.payload))
			if err == nil {
				t.Fatalf("ReadDestHeader(%q) = %+v, nil; want error", tc.payload, h)
			}
		})
	}
}

func TestWriteDestHeaderRejectsOverLongHost(t *testing.T) {
	h := DestHeader{Proto: "tcp", Host: strings.Repeat("b", 256), Port: 443}
	if err := WriteDestHeader(&bytes.Buffer{}, h); err == nil {
		t.Fatal("WriteDestHeader with 256-byte host: want error, got nil")
	}
}

func TestWriteDestHeaderRejectsUnknownProto(t *testing.T) {
	h := DestHeader{Proto: "sctp", Host: "example.com", Port: 443}
	if err := WriteDestHeader(&bytes.Buffer{}, h); err == nil {
		t.Fatal("WriteDestHeader with unknown proto: want error, got nil")
	}
}
