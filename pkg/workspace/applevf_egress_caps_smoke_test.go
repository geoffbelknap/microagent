//go:build darwin

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// TestAppleVFEgressCapsLiveSmoke boots real apple-vf workspaces with
// bounded-operations caps set on vmkit.Config — the library surface; the CLI
// has no caps flags — and proves the Swift supervisor forwards them to the
// host-fd datapath and the datapath enforces them at runtime:
//
//   - a concurrency-capped workspace refuses the second concurrent connection
//     (egress_cap_exceeded reason "concurrency"),
//   - a volume-capped workspace tears down the breaching flow once cumulative
//     upstream bytes exceed the cap (egress_cap_exceeded reason "volume").
//
// The caps are applied between Request and runForeground because Options has
// no caps source: library consumers set them on vmkit.Config directly, and
// this is the config→supervisor→datapath chain the smoke pins live.
//
// Gated: set MICROAGENT_APPLEVF_CAPS_SMOKE=1 on macOS/arm64 with a readable
// apple-vf kernel, a built signed supervisor, and network access. Overrides:
// MICROAGENT_APPLEVF_KERNEL, MICROAGENT_APPLEVF_SUPERVISOR,
// MICROAGENT_APPLEVF_GUESTINIT, MICROAGENT_APPLEVF_CAPS_IMAGE.
func TestAppleVFEgressCapsLiveSmoke(t *testing.T) {
	if os.Getenv("MICROAGENT_APPLEVF_CAPS_SMOKE") != "1" {
		t.Skip("set MICROAGENT_APPLEVF_CAPS_SMOKE=1 to run the live Apple VF egress caps smoke")
	}

	t.Run("concurrency", func(t *testing.T) {
		// One held-open connection to the allowed host, then a second fetch
		// while it is still up: the second must be refused fail-closed.
		cmd := `sh -c '(printf "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: keep-alive\r\n\r\n"; sleep 6) | nc example.com 80 >/dev/null 2>&1 & sleep 2; if wget -q -O /dev/null -T 5 http://example.com/; then echo SECOND_CONN_OK; else echo SECOND_CONN_BLOCKED; fi; wait'`
		resp, audit := runAppleVFCapped(t, "caps-conc-smoke", cmd, func(config *vmkit.Config) {
			config.EgressMaxConcurrentConns = 1
			config.EgressMaxBytesPerSec = 1024
		})
		if resp.Result == nil || !strings.Contains(resp.Result.Stdout, "SECOND_CONN_BLOCKED") {
			t.Errorf("second concurrent connection was not blocked; result=%#v", resp.Result)
		}
		if !strings.Contains(audit, `"reason":"concurrency"`) {
			t.Errorf("audit log has no egress_cap_exceeded concurrency record:\n%s", audit)
		}
		logCapRecords(t, audit)
	})

	t.Run("volume", func(t *testing.T) {
		// Sequential fetches: each plain-HTTP request sends ~100-150 upstream
		// bytes, so a 600-byte cumulative cap trips within a few fetches and
		// every later flow dies on its first upstream write.
		cmd := `sh -c 'ok=0; fail=0; for i in 1 2 3 4 5 6 7 8 9 10 11 12; do if wget -q -O /dev/null -T 5 http://example.com/; then ok=$((ok+1)); else fail=$((fail+1)); fi; done; echo VOLUME_OK=$ok VOLUME_FAIL=$fail'`
		resp, audit := runAppleVFCapped(t, "caps-vol-smoke", cmd, func(config *vmkit.Config) {
			config.EgressMaxTotalBytes = 600
		})
		if resp.Result == nil || !strings.Contains(resp.Result.Stdout, "VOLUME_FAIL=") ||
			strings.Contains(resp.Result.Stdout, "VOLUME_FAIL=0") {
			t.Errorf("no fetch failed under the volume cap; result=%#v", resp.Result)
		}
		if !strings.Contains(audit, `"reason":"volume"`) {
			t.Errorf("audit log has no egress_cap_exceeded volume record:\n%s", audit)
		}
		logCapRecords(t, audit)
	})
}

// runAppleVFCapped mirrors Create's run path (normalize → kernel → rootfs →
// manifest → Request → runForeground), applying mutate to the request Config
// before boot, and returns the run response plus the raw egress audit log.
func runAppleVFCapped(t *testing.T, name, execCommand string, mutate func(*vmkit.Config)) (vmkit.Response, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()

	opts := DefaultOptions()
	opts.Name = name
	opts.StateDir = t.TempDir()
	opts.PrepareForStart = true
	opts.ImageRef = envOr("MICROAGENT_APPLEVF_CAPS_IMAGE",
		"docker.io/library/busybox@sha256:c4e5b27bf840ba1ebd5568b6b914f6926f3559b2ad4f505b1f37aae483b907d6")
	opts.ExecCommand = execCommand
	opts.EgressMode = vmkit.EgressModeBroker
	opts.EgressAllow = []string{"example.com"}
	opts.MemoryMiB = 512
	opts.CPUCount = 2
	opts.SizeMiB = 256
	opts.Timeout = 2 * time.Minute
	if v := os.Getenv("MICROAGENT_APPLEVF_KERNEL"); v != "" {
		opts.KernelPath = v
	}
	if v := os.Getenv("MICROAGENT_APPLEVF_SUPERVISOR"); v != "" {
		opts.SupervisorPath = v
	}
	if v := os.Getenv("MICROAGENT_APPLEVF_GUESTINIT"); v != "" {
		opts.GuestInitPath = v
	}
	if _, err := os.Stat(opts.KernelPath); err != nil {
		t.Skipf("apple-vf kernel not readable at %s", opts.KernelPath)
	}
	if _, err := os.Stat(opts.SupervisorPath); err != nil {
		t.Skipf("apple-vf supervisor not built at %s", opts.SupervisorPath)
	}

	if err := normalizeLifecycleOptions(&opts, true); err != nil {
		t.Fatalf("normalize options: %v", err)
	}
	if err := EnsureKernel(ctx, &opts); err != nil {
		t.Fatalf("ensure kernel: %v", err)
	}
	if err := EnsureCanCreate(opts); err != nil {
		t.Fatalf("ensure can create: %v", err)
	}
	disks, err := PrepareDisks(ctx, opts)
	if err != nil {
		t.Fatalf("prepare disks: %v", err)
	}
	opts.Disks = disks
	result, err := BuildRootfs(ctx, opts)
	if err != nil {
		t.Fatalf("build rootfs: %v", err)
	}
	verification, err := BuildVerification(opts, result)
	if err != nil {
		t.Fatalf("build verification: %v", err)
	}
	opts.Verification = &verification
	if err := WriteManifest(opts); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	req, err := Request(opts, "run", result.RootfsPath, NewRequestID())
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	mutate(req.Config)

	resp, runErr := runForeground(ctx, opts, req)
	auditData, _ := os.ReadFile(filepath.Join(opts.StateDir, opts.Name, "egress-access.jsonl"))
	if runErr != nil {
		if serial, err := os.ReadFile(SerialLogPath(opts.StateDir, opts.Name)); err == nil {
			t.Logf("serial.log:\n%s", string(serial))
		}
		t.Fatalf("run: %v\nresponse=%#v", runErr, resp)
	}
	// The foreground response carries the lifecycle event; the structured
	// runtime result (guest stdout/exit) is read back like Create does.
	if final, err := Inspect(ctx, opts); err == nil && final.Result != nil {
		resp = final
	}
	return resp, string(auditData)
}

// logCapRecords surfaces the enforcement decisions the smoke keyed on so a
// passing run's output carries the evidence.
func logCapRecords(t *testing.T, audit string) {
	t.Helper()
	for _, line := range strings.Split(audit, "\n") {
		if strings.Contains(line, "egress_cap_exceeded") {
			t.Logf("audit: %s", line)
		}
	}
}

func envOr(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
