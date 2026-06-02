package secretxfer

import (
	"bytes"
	"testing"
)

func TestEncodeDecodeRoundTrip(t *testing.T) {
	in := Bundle{
		ProtocolVersion: ProtocolVersion,
		Secrets: []Entry{
			{Name: "API_KEY", Value: []byte("sekret")},
			{Name: "DB_PASS", Value: []byte("\x00\x01binary")},
		},
	}
	var buf bytes.Buffer
	if err := EncodeMessage(&buf, in); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var out Bundle
	if err := DecodeMessage(&buf, &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if out.ProtocolVersion != ProtocolVersion || len(out.Secrets) != 2 {
		t.Fatalf("round-trip mismatch: %+v", out)
	}
	if string(out.Secrets[1].Value) != "\x00\x01binary" {
		t.Fatalf("binary value not preserved: %q", out.Secrets[1].Value)
	}
}

func TestDecodeRejectsOversizeFrame(t *testing.T) {
	var buf bytes.Buffer
	buf.Write([]byte{0x00, 0x50, 0x00, 0x00})
	var out Bundle
	if err := DecodeMessageWithMax(&buf, &out, 1024); err == nil {
		t.Fatal("expected oversize-frame error")
	}
}

func TestDecodeNilTarget(t *testing.T) {
	var buf bytes.Buffer
	if err := DecodeMessage(&buf, nil); err == nil {
		t.Fatal("expected error for nil decode target")
	}
}
