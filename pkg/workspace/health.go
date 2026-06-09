package workspace

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

// Health declares a liveness probe for a workspace. When present, supervise
// runs the probe against the running guest and, after Retries consecutive
// failures, force-restarts the workspace through the restart policy — closing
// the gap where supervise only restarts on exit, not on "alive but wedged".
//
// Declare exactly one probe form:
//   - Exec: a command run in the guest through a backend with structured exec.
//     Healthy when it exits 0, like a docker HEALTHCHECK CMD.
//   - HTTPGet + Port: a host-side GET to a published guest Port. Healthy on a
//     non-error (<400) status.
type Health struct {
	Exec               []string `json:"exec,omitempty" yaml:"exec,omitempty"`
	HTTPGet            string   `json:"httpGet,omitempty" yaml:"httpGet,omitempty"`
	Port               int      `json:"port,omitempty" yaml:"port,omitempty"`
	IntervalSeconds    int      `json:"intervalSeconds,omitempty" yaml:"intervalSeconds,omitempty"`
	TimeoutSeconds     int      `json:"timeoutSeconds,omitempty" yaml:"timeoutSeconds,omitempty"`
	Retries            int      `json:"retries,omitempty" yaml:"retries,omitempty"`
	StartPeriodSeconds int      `json:"startPeriodSeconds,omitempty" yaml:"startPeriodSeconds,omitempty"`
}

const (
	DefaultHealthIntervalSeconds    = 30
	DefaultHealthTimeoutSeconds     = 5
	DefaultHealthRetries            = 3
	DefaultHealthStartPeriodSeconds = 0
)

// Declared reports whether a probe form is set. A zero Health is inert.
func (h Health) Declared() bool {
	return len(h.Exec) > 0 || strings.TrimSpace(h.HTTPGet) != ""
}

// IsExec reports whether the declared probe is an exec probe.
func (h Health) IsExec() bool { return len(h.Exec) > 0 }

func (h Health) interval() time.Duration {
	return time.Duration(h.IntervalSeconds) * time.Second
}
func (h Health) timeout() time.Duration {
	return time.Duration(h.TimeoutSeconds) * time.Second
}
func (h Health) startPeriod() time.Duration {
	return time.Duration(h.StartPeriodSeconds) * time.Second
}

// NormalizeHealthCheck fills unset timing fields with defaults.
func NormalizeHealthCheck(h Health) Health {
	if h.IntervalSeconds <= 0 {
		h.IntervalSeconds = DefaultHealthIntervalSeconds
	}
	if h.TimeoutSeconds <= 0 {
		h.TimeoutSeconds = DefaultHealthTimeoutSeconds
	}
	if h.Retries <= 0 {
		h.Retries = DefaultHealthRetries
	}
	if h.StartPeriodSeconds < 0 {
		h.StartPeriodSeconds = 0
	}
	h.HTTPGet = strings.TrimSpace(h.HTTPGet)
	return h
}

// ValidateHealthCheck rejects malformed health declarations. A zero Health is
// valid (no probe).
func ValidateHealthCheck(h Health) error {
	if !h.Declared() {
		return nil
	}
	if len(h.Exec) > 0 && strings.TrimSpace(h.HTTPGet) != "" {
		return fmt.Errorf("health check must declare either exec or httpGet, not both")
	}
	if len(h.Exec) > 0 && strings.TrimSpace(h.Exec[0]) == "" {
		return fmt.Errorf("health check exec command must not be empty")
	}
	if path := strings.TrimSpace(h.HTTPGet); path != "" {
		if !strings.HasPrefix(path, "/") {
			return fmt.Errorf("health check httpGet path must start with \"/\"")
		}
		if h.Port <= 0 || h.Port > 65535 {
			return fmt.Errorf("health check httpGet requires a published guest port (1-65535)")
		}
	}
	if h.IntervalSeconds < 0 || h.TimeoutSeconds < 0 || h.Retries < 0 || h.StartPeriodSeconds < 0 {
		return fmt.Errorf("health check interval, timeout, retries, and startPeriod must not be negative")
	}
	return nil
}

// healthManifest returns the manifest form of a health config: a normalized
// pointer when declared, nil otherwise (so the manifest omits an inert block).
func healthManifest(h Health) *Health {
	if !h.Declared() {
		return nil
	}
	normalized := NormalizeHealthCheck(h)
	return &normalized
}

// healthProber runs one probe and returns nil when healthy. It is a package
// variable so the supervise loop test can substitute a deterministic probe.
var healthProber = runHealthProbe

// healthNow is the clock used by health monitoring; overridable in tests.
var healthNow = time.Now

func runHealthProbe(ctx context.Context, opts Options, h Health) error {
	timeout := h.timeout()
	if h.IsExec() {
		req := execprotocol.NewExecRequest(h.Exec)
		if timeout > 0 {
			req.TimeoutMS = int64(timeout / time.Millisecond)
		}
		result, err := Exec(ctx, opts, req)
		if err != nil {
			return fmt.Errorf("health exec probe failed: %w", err)
		}
		if result.Error != nil {
			return fmt.Errorf("health exec probe error: %s", result.Error.Message)
		}
		if result.Status != execprotocol.ExecStatusExited || result.ExitCode == nil || *result.ExitCode != 0 {
			return fmt.Errorf("health exec probe unhealthy (status=%s)", result.Status)
		}
		return nil
	}

	hostPort, err := healthHTTPHostPort(opts, h.Port)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("http://127.0.0.1:%d%s", hostPort, h.HTTPGet)
	probeCtx := ctx
	if timeout > 0 {
		var cancel context.CancelFunc
		probeCtx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("health http probe failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("health http probe unhealthy (status %d)", resp.StatusCode)
	}
	return nil
}

// healthHTTPHostPort resolves the host port a guest port is published on.
func healthHTTPHostPort(opts Options, guestPort int) (uint16, error) {
	for _, f := range opts.Network.PortForwards {
		if int(f.GuestPort) == guestPort {
			return f.HostPort, nil
		}
	}
	return 0, fmt.Errorf("health httpGet port %d is not published; add it to the workspace network forwards", guestPort)
}

// healthApplicable reports whether the declared probe can run against this
// workspace's backend. Exec probes require the structured exec service; HTTP
// probes are host-side and backend-neutral.
func healthApplicable(opts Options) bool {
	if !opts.Health.Declared() {
		return false
	}
	if opts.Health.IsExec() && !backendSupportsStructuredExec(opts.Backend) {
		return false
	}
	return true
}

func backendSupportsStructuredExec(backend string) bool {
	switch backend {
	case vmkit.BackendFirecracker, vmkit.BackendAppleVF:
		return true
	default:
		return false
	}
}

// healthTracker holds the consecutive-failure state for one supervised run. It
// is reset per Start so a fresh boot starts with a clean slate and its own
// start-period grace.
type healthTracker struct {
	cfg         Health
	start       time.Time
	last        time.Time
	consecutive int
	now         func() time.Time
}

func newHealthTracker(cfg Health, now func() time.Time) *healthTracker {
	if now == nil {
		now = time.Now
	}
	return &healthTracker{cfg: NormalizeHealthCheck(cfg), start: now(), now: now}
}

// observe is called each supervise tick. It runs the probe at most once per
// configured interval (after the start-period grace) and returns true once the
// workspace has failed Retries consecutive probes and should be restarted.
func (t *healthTracker) observe(ctx context.Context, opts Options, running bool, probe func(context.Context, Options, Health) error) bool {
	if !t.cfg.Declared() || !running {
		return false
	}
	now := t.now()
	if now.Sub(t.start) < t.cfg.startPeriod() {
		return false
	}
	if !t.last.IsZero() && now.Sub(t.last) < t.cfg.interval() {
		return false
	}
	t.last = now
	if probe(ctx, opts, t.cfg) != nil {
		t.consecutive++
		return t.consecutive >= t.cfg.Retries
	}
	t.consecutive = 0
	return false
}
