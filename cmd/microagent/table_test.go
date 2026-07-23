package main

import (
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
)

// captureTableOutput runs fn with the write end of a pipe and returns
// everything written, draining concurrently so fn never blocks on a full
// pipe buffer.
func captureTableOutput(t *testing.T, fn func(stdout *os.File)) []byte {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	done := make(chan struct{})
	var out []byte
	go func() {
		out, _ = io.ReadAll(r)
		close(done)
	}()
	fn(w)
	if err := w.Close(); err != nil {
		t.Fatalf("close write end: %v", err)
	}
	<-done
	r.Close()
	return out
}

// --- Golden non-TTY byte-stability, one per migrated renderer -------------
//
// These strings were captured from the pre-table-renderer code (the plain
// fmt.Fprintf("%-Ns ...", ...) calls this task replaced) run against the
// same sample inputs, then diffed byte-for-byte against the new renderTable
// output. Workspace/volume/snapshot matched exactly; the image list row
// differed only inside the fixed 72-byte DIGEST field, where the full
// "sha256:"+64-hex string was replaced by the shortened 12-hex form (the
// one documented, deliberate exception to byte-for-byte piped output). Any
// future change to these goldens outside the digest field is a piped-output
// regression the awk/cut compatibility bar exists to catch.

func TestWorkspaceListNonTTYGoldenBytes(t *testing.T) {
	prevFormat := outputFormat
	t.Cleanup(func() { outputFormat = prevFormat })
	outputFormat = "text"

	entries := []workspaceListEntry{
		{Name: "alpha", State: "running", Backend: "linux-kvm", Profile: "small", Network: "user", Restart: "always"},
		{Name: "a-very-long-workspace-name-here", State: "failed", Backend: "apple-vf", Profile: "tiny", Network: "isolated", Restart: "never"},
	}
	got := captureTableOutput(t, func(stdout *os.File) {
		if err := writeWorkspaceList(stdout, entries); err != nil {
			t.Fatalf("writeWorkspaceList: %v", err)
		}
	})
	want := "NAME                     STATE        BACKEND      PROFILE      NETWORK    RESTART\n" +
		"alpha                    running      linux-kvm    small        user       always\n" +
		"a-very-long-workspace-name-here failed       apple-vf     tiny         isolated   never\n"
	if string(got) != want {
		t.Fatalf("writeWorkspaceList non-TTY output changed:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestImageListNonTTYGoldenBytesDigestShortensOnly(t *testing.T) {
	prevFormat := outputFormat
	t.Cleanup(func() { outputFormat = prevFormat })
	outputFormat = "text"

	images := []imageRecord{
		{
			ImageRef:   "docker.io/library/alpine:3.19",
			Digest:     "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567",
			SizeBytes:  7340032,
			LastUsedAt: "2026-07-20T10:00:00Z",
		},
	}
	got := captureTableOutput(t, func(stdout *os.File) {
		if err := writeImageList(stdout, images); err != nil {
			t.Fatalf("writeImageList: %v", err)
		}
	})
	// Legacy bytes had the full digest in the 72-wide DIGEST field:
	// "docker.io/library/alpine:3.19                    sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567    /                7340032    2026-07-20T10:00:00Z\n"
	// The DIGEST field stays 72 bytes wide either way (73 with its
	// separator), so the row's total length and everything after that
	// field is unchanged; only the digest text itself shortens.
	want := "IMAGE                                            DIGEST                                                                   PLATFORM         SIZE       LAST USED\n" +
		"docker.io/library/alpine:3.19                    abcdef012345                                                             /                7340032    2026-07-20T10:00:00Z\n"
	if string(got) != want {
		t.Fatalf("writeImageList non-TTY output changed:\ngot:  %q\nwant: %q", got, want)
	}

	// Confirm the legacy row (full digest) and the new row (short digest)
	// are the exact same total length: the DIGEST field is a fixed 72-wide
	// column either way, so shortening the digest text changes what those
	// bytes hold, never how many bytes the row occupies.
	legacyDigestField := "sha256:abcdef0123456789abcdef0123456789abcdef0123456789abcdef01234567"
	legacyRow := "docker.io/library/alpine:3.19                    " + legacyDigestField +
		strings.Repeat(" ", 72-len(legacyDigestField)) + " /                7340032    2026-07-20T10:00:00Z\n"
	newRowLine := strings.SplitN(string(got), "\n", 2)[1]
	if len(legacyRow) != len(newRowLine) {
		t.Fatalf("expected identical row byte length old vs new (digest field padding absorbs the shorter text): legacy=%d new=%d", len(legacyRow), len(newRowLine))
	}
}

func TestVolumeListNonTTYGoldenBytes(t *testing.T) {
	records := []volume.Record{
		{Name: "data", SizeMiB: 2048, AttachedTo: "alpha"},
		{Name: "cache", SizeMiB: 512},
	}
	got := captureTableOutput(t, func(stdout *os.File) {
		if err := writeVolumeList(stdout, records); err != nil {
			t.Fatalf("writeVolumeList: %v", err)
		}
	})
	want := "NAME                 SIZE-MIB   ATTACHED\n" +
		"data                 2048       alpha\n" +
		"cache                512        -\n"
	if string(got) != want {
		t.Fatalf("writeVolumeList non-TTY output changed:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestSnapshotListNonTTYGoldenBytes(t *testing.T) {
	prevFormat := outputFormat
	t.Cleanup(func() { outputFormat = prevFormat })
	outputFormat = "text"

	infos := []vmkit.SnapshotInfo{
		{
			SnapshotManifest: vmkit.SnapshotManifest{
				Tag:       "before-upgrade",
				CreatedAt: "2026-07-20T10:00:00Z",
				ImageRef:  "docker.io/library/alpine:3.19",
			},
			SizeBytes: 104857600,
		},
	}
	got := captureTableOutput(t, func(stdout *os.File) {
		if err := writeSnapshotListResult(stdout, "alpha", infos); err != nil {
			t.Fatalf("writeSnapshotListResult: %v", err)
		}
	})
	want := "TAG                      SIZE         CREATED               IMAGE\n" +
		"before-upgrade           100.0MiB     2026-07-20T10:00:00Z  docker.io/library/alpine:3.19\n"
	if string(got) != want {
		t.Fatalf("writeSnapshotListResult non-TTY output changed:\ngot:  %q\nwant: %q", got, want)
	}
}

// --- TTY narrow-width truncation --------------------------------------

// withFakeTerminalWidth overrides the sole term.GetSize seam so tests can
// exercise TTY layout logic without a real terminal.
func withFakeTerminalWidth(t *testing.T, width int) {
	t.Helper()
	prev := terminalWidth
	terminalWidth = func(*os.File) (int, bool) { return width, true }
	t.Cleanup(func() { terminalWidth = prev })
}

func TestRenderTableTTYNarrowWidthTruncatesFlexColumn(t *testing.T) {
	withFakeTerminalWidth(t, 40)

	cols := []tableColumn{
		{Header: "NAME", Legacy: 24, Min: 8, Max: 32, Flex: true},
		{Header: "STATE", Legacy: 12, Min: 5, Max: 12},
		{Header: "RESTART", Legacy: 0, Min: 7},
	}
	rows := [][]tableCell{
		{cell("a-very-long-workspace-name-that-overflows"), cell("running"), cell("always")},
	}
	got := captureTableOutput(t, func(stdout *os.File) {
		renderTable(stdout, cols, rows)
	})
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected header + 1 data row, got %d lines: %q", len(lines), got)
	}
	if !strings.Contains(lines[1], "…") {
		t.Fatalf("expected the over-long NAME to be truncated with an ellipsis, got %q", lines[1])
	}
	if strings.Contains(lines[1], "a-very-long-workspace-name-that-overflows") {
		t.Fatalf("expected NAME to be shortened for a 40-column terminal, got the full value: %q", lines[1])
	}
	// The trailing RESTART column must still be intact and untruncated.
	if !strings.HasSuffix(lines[1], "always") {
		t.Fatalf("expected RESTART value intact at end of row, got %q", lines[1])
	}
}

func TestRenderTableTTYWideWidthDoesNotTruncate(t *testing.T) {
	withFakeTerminalWidth(t, 200)
	cols := []tableColumn{
		{Header: "NAME", Legacy: 24, Min: 8, Max: 60, Flex: true},
		{Header: "STATE", Legacy: 12, Min: 5, Max: 12},
	}
	rows := [][]tableCell{
		{cell("short-name"), cell("running")},
	}
	got := captureTableOutput(t, func(stdout *os.File) {
		renderTable(stdout, cols, rows)
	})
	lines := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	if strings.Contains(lines[1], "…") {
		t.Fatalf("did not expect truncation on a wide terminal, got %q", lines[1])
	}
	if !strings.Contains(lines[1], "short-name") {
		t.Fatalf("expected full NAME value present, got %q", lines[1])
	}
}

// --- Truncation composes correctly with ANSI color --------------------

func TestRenderTableTruncationNeverCutsColorEscapes(t *testing.T) {
	withFakeTerminalWidth(t, 30)
	tty := ttyStandinForTest(t)
	noColorFlag = false
	withNoColorEnv(t, "", false)

	cols := []tableColumn{
		{Header: "NAME", Legacy: 24, Min: 8, Max: 40, Flex: true},
		{Header: "STATE", Legacy: 12, Min: 5, Max: 12},
	}
	rows := [][]tableCell{
		{
			cell("a-name-that-is-long-enough-to-force-truncation"),
			// Colorize checks tty (a real character device stand-in) for
			// colorEnabled, decoupled from where bytes are written, so the
			// test can both force color on and capture the output; the
			// production call site (writeWorkspaceList) always uses the
			// same *os.File for both.
			{Text: "running", Colorize: func(s string) string { return colorizeState(tty, s) }},
		},
	}
	got := captureTableOutput(t, func(stdout *os.File) {
		renderTable(stdout, cols, rows)
	})
	line := strings.Split(strings.TrimRight(string(got), "\n"), "\n")
	last := line[len(line)-1]

	if !strings.Contains(last, ansiGreen+"running"+ansiReset) {
		t.Fatalf("expected an intact ansiGreen(\"running\")ansiReset escape sequence, got %q", last)
	}
	if !strings.Contains(last, "…") {
		t.Fatalf("expected the NAME column to be truncated with an ellipsis, got %q", last)
	}
	// A cut escape sequence would leave a bare ESC (0x1b) not immediately
	// followed by a valid "[" CSI introducer, or a color code left
	// unterminated by ansiReset. Since Colorize is applied to the raw
	// column's already-truncated text (not the other way around), the
	// escape bytes for the STATE column can never straddle a truncation cut
	// that only ever happens in the NAME column.
	if strings.Count(last, "\x1b") != 2 {
		t.Fatalf("expected exactly 2 escape sequences (color start + reset), got %d in %q", strings.Count(last, "\x1b"), last)
	}
}

// TestTableColumnMaxFitsHeader guards against the header-truncation footgun:
// if a column's Max > 0, ensure Max >= header width so the header is never
// truncated. This test collects all declared tables and validates each.
func TestTableColumnMaxFitsHeader(t *testing.T) {
	allTables := []struct {
		name string
		cols []tableColumn
	}{
		{
			name: "workspace list",
			cols: []tableColumn{
				{Header: "NAME", Legacy: 24, Min: 12, Max: 32, Flex: true},
				{Header: "STATE", Legacy: 12, Min: 5, Max: 12},
				{Header: "BACKEND", Legacy: 12, Min: 7, Max: 12},
				{Header: "PROFILE", Legacy: 12, Min: 7, Max: 16},
				{Header: "NETWORK", Legacy: 10, Min: 7, Max: 10},
				{Header: "RESTART", Legacy: 0, Min: 7},
			},
		},
		{
			name: "image list",
			cols: []tableColumn{
				{Header: "IMAGE", Legacy: 48, Min: 16, Max: 60, Flex: true},
				{Header: "DIGEST", Legacy: 72, Min: 12, Max: 12},
				{Header: "PLATFORM", Legacy: 16, Min: 8, Max: 16},
				{Header: "SIZE", Legacy: 10, Min: 6, Max: 10},
				{Header: "LAST USED", Legacy: 0, Min: 10},
			},
		},
		{
			name: "volume list",
			cols: []tableColumn{
				{Header: "NAME", Legacy: 20, Min: 10, Max: 28, Flex: true},
				{Header: "SIZE-MIB", Legacy: 10, Min: 8, Max: 10},
				{Header: "ATTACHED", Legacy: 0, Min: 8},
			},
		},
		{
			name: "snapshot list",
			cols: []tableColumn{
				{Header: "TAG", Legacy: 24, Min: 10, Max: 32, Flex: true},
				{Header: "SIZE", Legacy: 12, Min: 6, Max: 12},
				{Header: "CREATED", Legacy: 21, Min: 19, Max: 25},
				{Header: "IMAGE", Legacy: 0, Min: 10},
			},
		},
	}

	for _, table := range allTables {
		t.Run(table.name, func(t *testing.T) {
			for _, col := range table.cols {
				if col.Max > 0 {
					headerWidth := utf8.RuneCountInString(col.Header)
					if col.Max < headerWidth {
						t.Errorf("column %q has Max=%d but header width=%d (Max must be >= header width)",
							col.Header, col.Max, headerWidth)
					}
				}
			}
		})
	}
}
