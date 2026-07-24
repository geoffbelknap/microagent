package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// checkEgressDatapathBin validates the MICROAGENT_EGRESS_DATAPATH_BIN value
// the live egress caps smoke depends on. Under go test, os.Executable is the
// test binary, so supervisorEnvironment defers to this variable; if it is
// unset or unreadable the supervisor's host-fd datapath fails and every
// guest connection is blocked fail-closed, which lets cap assertions pass
// against a total outage while both audit assertions fail on an empty
// egress-access.jsonl.
func checkEgressDatapathBin(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("MICROAGENT_EGRESS_DATAPATH_BIN is not set: under go test os.Executable is the test binary, so set it to a built microagent binary or the supervisor's host-fd egress datapath fails and every guest connection is blocked fail-closed")
	}
	info, err := os.Stat(value)
	if err != nil {
		return fmt.Errorf("MICROAGENT_EGRESS_DATAPATH_BIN is not readable at %s: %w", value, err)
	}
	if info.IsDir() {
		return fmt.Errorf("MICROAGENT_EGRESS_DATAPATH_BIN is not readable at %s: path is a directory", value)
	}
	return nil
}

// capSmokeCount extracts the non-negative integer that immediately follows
// key (e.g. "VOLUME_OK=") in the guest stdout.
func capSmokeCount(output, key string) (int, bool) {
	idx := strings.Index(output, key)
	if idx < 0 {
		return 0, false
	}
	rest := output[idx+len(key):]
	end := 0
	for end < len(rest) && rest[end] >= '0' && rest[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0, false
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0, false
	}
	return n, true
}

func TestCheckEgressDatapathBin(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "microagent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatalf("write fake binary: %v", err)
	}

	if err := checkEgressDatapathBin(bin); err != nil {
		t.Errorf("readable file rejected: %v", err)
	}
	for name, value := range map[string]string{
		"empty":      "",
		"whitespace": "  \t",
		"missing":    filepath.Join(dir, "no-such-binary"),
		"directory":  dir,
	} {
		err := checkEgressDatapathBin(value)
		if err == nil {
			t.Errorf("%s value %q accepted, want error", name, value)
			continue
		}
		if !strings.Contains(err.Error(), "MICROAGENT_EGRESS_DATAPATH_BIN") {
			t.Errorf("%s error does not name the variable: %v", name, err)
		}
	}
}

func TestCapSmokeCount(t *testing.T) {
	stdout := "SECOND_CONN_BLOCKED\nFIRST_CONN_BYTES=1256\nVOLUME_OK=4 VOLUME_FAIL=8\nVOLUME_ZERO=0\n"
	cases := []struct {
		key   string
		want  int
		found bool
	}{
		{"FIRST_CONN_BYTES=", 1256, true},
		{"VOLUME_OK=", 4, true},
		{"VOLUME_FAIL=", 8, true},
		{"VOLUME_ZERO=", 0, true},
		{"MISSING=", 0, false},
	}
	for _, tc := range cases {
		n, found := capSmokeCount(stdout, tc.key)
		if n != tc.want || found != tc.found {
			t.Errorf("capSmokeCount(%q) = (%d, %v), want (%d, %v)", tc.key, n, found, tc.want, tc.found)
		}
	}
	if n, found := capSmokeCount("FIRST_CONN_BYTES=\n", "FIRST_CONN_BYTES="); found || n != 0 {
		t.Errorf("key with no digits reported (%d, %v), want (0, false)", n, found)
	}
}
