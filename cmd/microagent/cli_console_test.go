package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestCopyConsoleInputNormalizesNewlines(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyConsoleInput(&dst, strings.NewReader("echo ready\n"))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	if written != int64(len("echo ready\r")) || dst.String() != "echo ready\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyConsoleInputStripsBracketedPasteMarkers(t *testing.T) {
	var dst bytes.Buffer
	input := "\x1b[200~hostname -I\x1b[201~\n"
	written, err := copyConsoleInput(&dst, strings.NewReader(input))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	if written != int64(len("hostname -I\r")) || dst.String() != "hostname -I\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyConsoleInputDetachesOnCtrlBracket(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyConsoleInput(&dst, strings.NewReader("echo before\n"+string([]byte{consoleDetachByte})+"echo after\n"))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	if written != int64(len("echo before\r")) || dst.String() != "echo before\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyConsoleInputDetachesOnCtrlPCtrlQ(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyConsoleInput(&dst, strings.NewReader("echo before\n"+string([]byte{consoleDetachPrefix, consoleDetachSuffix})+"echo after\n"))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	if written != int64(len("echo before\r")) || dst.String() != "echo before\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyConsoleInputKeepsCtrlPWithoutCtrlQ(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyConsoleInput(&dst, strings.NewReader("echo "+string([]byte{consoleDetachPrefix, 'x'})+"\n"))
	if err != nil {
		t.Fatalf("copyConsoleInput: %v", err)
	}
	want := "echo " + string([]byte{consoleDetachPrefix, 'x'}) + "\r"
	if written != int64(len(want)) || dst.String() != want {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyShellInputPreservesCarriageReturns(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyShellInput(&dst, strings.NewReader("echo ready\r"))
	if err != nil {
		t.Fatalf("copyShellInput: %v", err)
	}
	if written != int64(len("echo ready\r")) || dst.String() != "echo ready\r" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestCopyShellInputDetachesOnCtrlPCtrlQ(t *testing.T) {
	var dst bytes.Buffer
	written, err := copyShellInput(&dst, strings.NewReader("echo before\n"+string([]byte{consoleDetachPrefix, consoleDetachSuffix})+"echo after\n"))
	if err != nil {
		t.Fatalf("copyShellInput: %v", err)
	}
	if written != int64(len("echo before\n")) || dst.String() != "echo before\n" {
		t.Fatalf("written=%d dst=%q", written, dst.String())
	}
}

func TestDataAfterOffsetIgnoresOldConsoleMarkers(t *testing.T) {
	data := []byte("old marker\nnew marker\n")
	got := dataAfterOffset(data, int64(len(data)), int64(len("old marker\n")))
	if string(got) != "new marker\n" {
		t.Fatalf("dataAfterOffset = %q", got)
	}
}
