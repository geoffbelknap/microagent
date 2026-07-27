package workspace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeSerialFixture writes a line-structured console log of roughly n bytes
// and returns its path and exact content.
func writeSerialFixture(t *testing.T, n int) (string, string) {
	t.Helper()
	var b strings.Builder
	for i := 0; b.Len() < n; i++ {
		b.WriteString(strings.Repeat("x", 40))
		b.WriteString("\n")
	}
	path := filepath.Join(t.TempDir(), "serial.log")
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	return path, b.String()
}

// TestSerialLogIsBoundedByDefault pins the payload bound. The structured
// result is the agent-facing surface, and a full boot log inlined there made a
// two-word run cost ~39 KB of which ~86% was console noise — on a substrate
// whose gateway layer exists to keep large results out of a model's context.
func TestSerialLogIsBoundedByDefault(t *testing.T) {
	path, content := writeSerialFixture(t, 4*DefaultSerialLogMaxBytes)

	result := Result{SerialPath: path}
	fillRunResult(&result, Options{})

	if len(result.SerialLog) > DefaultSerialLogMaxBytes {
		t.Errorf("SerialLog = %d bytes, want <= %d", len(result.SerialLog), DefaultSerialLogMaxBytes)
	}
	if !result.SerialLogTruncated {
		t.Error("SerialLogTruncated not set on a bounded log")
	}
	if result.SerialLogBytes != len(content) {
		t.Errorf("SerialLogBytes = %d, want the full size %d", result.SerialLogBytes, len(content))
	}
	if !strings.HasSuffix(content, result.SerialLog) {
		t.Error("the excerpt is not the tail of the log; failures live at the end")
	}
	if !strings.HasSuffix(result.SerialLog, "\n") || len(result.SerialLog) == 0 {
		t.Error("tail lost the final line ending")
	}
	// The excerpt must open at a line boundary: its start position in the
	// original content is either 0 or just after a newline.
	start := len(content) - len(result.SerialLog)
	if start > 0 && content[start-1] != '\n' {
		t.Error("excerpt opens mid-line")
	}
}

// TestSerialLogFullOptIn pins the negative-limit escape hatch and that a
// small log is never marked truncated.
func TestSerialLogFullOptIn(t *testing.T) {
	path, content := writeSerialFixture(t, 4*DefaultSerialLogMaxBytes)

	result := Result{SerialPath: path}
	fillRunResult(&result, Options{SerialLogMaxBytes: -1})
	if result.SerialLog != content || result.SerialLogTruncated {
		t.Errorf("negative limit must inline the full log untruncated (got %d bytes, truncated=%v)",
			len(result.SerialLog), result.SerialLogTruncated)
	}

	small := Result{SerialPath: path}
	fillRunResult(&small, Options{SerialLogMaxBytes: len(content) + 1})
	if small.SerialLog != content || small.SerialLogTruncated {
		t.Error("a log under the limit must be inlined whole and unmarked")
	}

	custom := Result{SerialPath: path}
	fillRunResult(&custom, Options{SerialLogMaxBytes: 100})
	if len(custom.SerialLog) > 100 || !custom.SerialLogTruncated {
		t.Errorf("custom limit not honored: %d bytes, truncated=%v", len(custom.SerialLog), custom.SerialLogTruncated)
	}
}
