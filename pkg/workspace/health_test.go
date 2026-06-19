package workspace

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

func TestValidateHealthCheck(t *testing.T) {
	cases := []struct {
		name    string
		health  Health
		wantErr bool
	}{
		{"zero is inert", Health{}, false},
		{"valid exec", Health{Exec: []string{"true"}}, false},
		{"valid http", Health{HTTPGet: "/healthz", Port: 8080}, false},
		{"both forms", Health{Exec: []string{"true"}, HTTPGet: "/x", Port: 80}, true},
		{"empty exec command", Health{Exec: []string{"  "}}, true},
		{"http missing leading slash", Health{HTTPGet: "healthz", Port: 80}, true},
		{"http missing port", Health{HTTPGet: "/healthz"}, true},
		{"http port out of range", Health{HTTPGet: "/healthz", Port: 70000}, true},
		{"negative interval", Health{Exec: []string{"true"}, IntervalSeconds: -1}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateHealthCheck(tc.health)
			if tc.wantErr != (err != nil) {
				t.Fatalf("ValidateHealthCheck(%#v) err = %v, wantErr = %v", tc.health, err, tc.wantErr)
			}
		})
	}
}

func TestNormalizeHealthCheckDefaults(t *testing.T) {
	got := NormalizeHealthCheck(Health{Exec: []string{"true"}})
	if got.IntervalSeconds != DefaultHealthIntervalSeconds ||
		got.TimeoutSeconds != DefaultHealthTimeoutSeconds ||
		got.Retries != DefaultHealthRetries {
		t.Fatalf("defaults not applied: %#v", got)
	}
	// Explicit values are preserved.
	got = NormalizeHealthCheck(Health{Exec: []string{"true"}, IntervalSeconds: 10, TimeoutSeconds: 2, Retries: 1})
	if got.IntervalSeconds != 10 || got.TimeoutSeconds != 2 || got.Retries != 1 {
		t.Fatalf("explicit values overwritten: %#v", got)
	}
}

func TestHealthManifest(t *testing.T) {
	if healthManifest(Health{}) != nil {
		t.Error("undeclared health should produce nil manifest")
	}
	m := healthManifest(Health{Exec: []string{"true"}})
	if m == nil || m.IntervalSeconds != DefaultHealthIntervalSeconds {
		t.Errorf("declared health should produce normalized manifest, got %#v", m)
	}
}

func TestHealthApplicable(t *testing.T) {
	cases := []struct {
		name string
		opts Options
		want bool
	}{
		{"not declared", Options{Backend: vmkit.BackendLinuxKVM}, false},
		{"exec on firecracker", Options{Backend: vmkit.BackendLinuxKVM, Health: Health{Exec: []string{"true"}}}, true},
		{"exec on apple-vf", Options{Backend: vmkit.BackendAppleVF, Health: Health{Exec: []string{"true"}}}, true},
		{"exec on windows-hyperv", Options{Backend: vmkit.BackendWindowsHyperV, Health: Health{Exec: []string{"true"}}}, true},
		{"exec on backend without structured exec", Options{Backend: "unknown", Health: Health{Exec: []string{"true"}}}, false},
		{"http on any backend", Options{Backend: "applevf", Health: Health{HTTPGet: "/x", Port: 80}}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := healthApplicable(tc.opts); got != tc.want {
				t.Fatalf("healthApplicable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestHealthTrackerObserve(t *testing.T) {
	cfg := Health{Exec: []string{"true"}, IntervalSeconds: 10, TimeoutSeconds: 1, Retries: 3, StartPeriodSeconds: 5}

	t.Run("skips while not running", func(t *testing.T) {
		now := time.Unix(1000, 0)
		tr := newHealthTracker(cfg, func() time.Time { return now })
		probed := 0
		failProbe := func(context.Context, Options, Health) error { probed++; return errors.New("down") }
		now = now.Add(time.Minute)
		if tr.observe(context.Background(), Options{}, false, failProbe) {
			t.Fatal("must not report unhealthy when not running")
		}
		if probed != 0 {
			t.Fatalf("probe ran %d times while not running", probed)
		}
	})

	t.Run("honors start period", func(t *testing.T) {
		now := time.Unix(2000, 0)
		tr := newHealthTracker(cfg, func() time.Time { return now })
		probed := 0
		failProbe := func(context.Context, Options, Health) error { probed++; return errors.New("down") }
		// Within the 5s start period: no probe.
		now = now.Add(3 * time.Second)
		tr.observe(context.Background(), Options{}, true, failProbe)
		if probed != 0 {
			t.Fatalf("probe ran during start period (%d)", probed)
		}
		// After start period: probes.
		now = now.Add(3 * time.Second)
		tr.observe(context.Background(), Options{}, true, failProbe)
		if probed != 1 {
			t.Fatalf("probe should have run after start period, got %d", probed)
		}
	})

	t.Run("unhealthy after consecutive failures then reset on success", func(t *testing.T) {
		now := time.Unix(3000, 0)
		tr := newHealthTracker(cfg, func() time.Time { return now })
		fail := true
		probe := func(context.Context, Options, Health) error {
			if fail {
				return errors.New("down")
			}
			return nil
		}
		past := func() { now = now.Add(11 * time.Second) }
		past() // clear start period + first interval
		if tr.observe(context.Background(), Options{}, true, probe) {
			t.Fatal("unhealthy too early (1 failure)")
		}
		past()
		if tr.observe(context.Background(), Options{}, true, probe) {
			t.Fatal("unhealthy too early (2 failures)")
		}
		past()
		if !tr.observe(context.Background(), Options{}, true, probe) {
			t.Fatal("should be unhealthy after 3 consecutive failures")
		}
		// A success resets the counter.
		fail = false
		tr = newHealthTracker(cfg, func() time.Time { return now })
		past()
		past()
		if tr.observe(context.Background(), Options{}, true, probe) {
			t.Fatal("healthy probe must not report unhealthy")
		}
	})

	t.Run("interval gates probe frequency", func(t *testing.T) {
		now := time.Unix(4000, 0)
		tr := newHealthTracker(cfg, func() time.Time { return now })
		probed := 0
		probe := func(context.Context, Options, Health) error { probed++; return nil }
		now = now.Add(6 * time.Second) // past start period
		tr.observe(context.Background(), Options{}, true, probe)
		// Immediately again, before the 10s interval elapses: no new probe.
		tr.observe(context.Background(), Options{}, true, probe)
		if probed != 1 {
			t.Fatalf("probe should be interval-gated, ran %d times", probed)
		}
		now = now.Add(10 * time.Second)
		tr.observe(context.Background(), Options{}, true, probe)
		if probed != 2 {
			t.Fatalf("probe should run after interval, ran %d times", probed)
		}
	})
}

func TestRunHealthProbeHTTP(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer healthy.Close()

	hostPort := serverPort(t, healthy.URL)
	opts := Options{Network: vmkit.NetworkConfig{PortForwards: []vmkit.PortForward{{GuestPort: 8080, HostPort: hostPort}}}}

	if err := runHealthProbe(context.Background(), opts, Health{HTTPGet: "/healthz", Port: 8080, TimeoutSeconds: 2}); err != nil {
		t.Fatalf("healthy probe returned error: %v", err)
	}
	if err := runHealthProbe(context.Background(), opts, Health{HTTPGet: "/boom", Port: 8080, TimeoutSeconds: 2}); err == nil {
		t.Fatal("expected error for 500 response")
	}
	if err := runHealthProbe(context.Background(), opts, Health{HTTPGet: "/healthz", Port: 9999, TimeoutSeconds: 2}); err == nil {
		t.Fatal("expected error for unpublished port")
	}
}

func TestRunHealthProbeExec(t *testing.T) {
	saved := execReadinessProbe
	t.Cleanup(func() { execReadinessProbe = saved })
	execReadinessProbe = func(context.Context, RuntimeState, time.Duration) (vmkit.ReadinessSignal, bool) {
		return vmkit.ReadinessSignal{Ready: true}, true
	}

	exitCode := 0
	_, port, stop := startWorkspaceExecServer(t, func(conn net.Conn) {
		var req execprotocol.ExecRequest
		if err := execprotocol.DecodeMessage(conn, &req); err != nil {
			return
		}
		result := execprotocol.NewExecResult(execprotocol.ExecStatusExited)
		code := exitCode
		result.ExitCode = &code
		_ = execprotocol.EncodeMessage(conn, result)
	})
	defer stop()
	opts := writeExecRuntimeState(t, vmkit.BackendLinuxKVM, vmkit.StateRunning, port)

	exitCode = 0
	if err := runHealthProbe(context.Background(), opts, Health{Exec: []string{"/bin/true"}, TimeoutSeconds: 2}); err != nil {
		t.Fatalf("exit 0 should be healthy, got %v", err)
	}
	exitCode = 7
	if err := runHealthProbe(context.Background(), opts, Health{Exec: []string{"/bin/false"}, TimeoutSeconds: 2}); err == nil {
		t.Fatal("non-zero exit should be unhealthy")
	}
}

func TestHealthSpecRoundTrip(t *testing.T) {
	spec := Spec{
		Name:   "svc",
		Health: Health{Exec: []string{"curl", "-fsS", "http://localhost/health"}, IntervalSeconds: 15, Retries: 2},
	}
	opts := DefaultOptions()
	opts.Name = "svc"
	opts.StateDir = t.TempDir()
	if err := ApplySpec(&opts, spec, t.TempDir(), SpecApplyOptions{}); err != nil {
		t.Fatalf("ApplySpec: %v", err)
	}
	if !opts.Health.Declared() || opts.Health.IntervalSeconds != 15 || opts.Health.Retries != 2 {
		t.Fatalf("health not applied to options: %#v", opts.Health)
	}
	// Timeout defaulted during normalization.
	if opts.Health.TimeoutSeconds != DefaultHealthTimeoutSeconds {
		t.Fatalf("timeout not normalized: %#v", opts.Health)
	}

	if err := WriteManifest(opts); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	manifest, err := ReadManifest(opts.StateDir, opts.Name)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if manifest.Health == nil {
		t.Fatal("manifest dropped health block")
	}
	if strings.Join(manifest.Health.Exec, " ") != "curl -fsS http://localhost/health" ||
		manifest.Health.IntervalSeconds != 15 || manifest.Health.Retries != 2 {
		t.Fatalf("manifest health mismatch: %#v", manifest.Health)
	}
}

func serverPort(t *testing.T, url string) uint16 {
	t.Helper()
	_, portText, err := net.SplitHostPort(strings.TrimPrefix(url, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	p, err := strconvParseUint16(portText)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
