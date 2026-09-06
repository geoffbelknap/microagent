// Package modelservice attaches host model runners through a stable endpoint.
// VM supervisors only need to forward to the returned address; runner lookup
// and optional model request mediation remain in the host service process.
package modelservice

import (
	"context"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
)

type Options struct {
	StateDir, WorkspaceID, ExecPath string
	Runner                          modelrunner.Record
	// Mode is empty for byte forwarding, or local-allow/policy for HTTP mediation.
	Mode                  string
	PolicyURL, PolicyFile string
	PolicyTimeout         time.Duration
}

type Attachment struct {
	Target       string
	Mode         string
	PID, Port    int
	AuditLogPath string
}

// Attach starts or reuses the workspace's model service. ExecPath must name a
// microagent executable implementing the bundled host-worker companion.
// The caller retains ownership of the runner and calls Release on teardown.
func Attach(ctx context.Context, opts Options) (Attachment, error) {
	r := opts.Runner
	if strings.TrimSpace(r.ModelRef) == "" || strings.TrimSpace(r.Host) == "" || r.Port <= 0 || r.Port > 65535 {
		return Attachment{}, fmt.Errorf("model service requires a model reference and valid runner endpoint")
	}
	mode := hostworker.Mode(opts.Mode)
	switch mode {
	case "":
		mode = hostworker.ModeForward
	case hostworker.ModeLocalAllow, hostworker.ModePolicy:
	default:
		return Attachment{}, fmt.Errorf("model service mode must be empty, local-allow, or policy")
	}
	if mode != hostworker.ModePolicy && (strings.TrimSpace(opts.PolicyURL) != "" || strings.TrimSpace(opts.PolicyFile) != "") {
		return Attachment{}, fmt.Errorf("model service mode %q cannot enforce a policy; select policy mode", opts.Mode)
	}
	rec, err := hostworker.EnsureProcess(ctx, hostworker.ProcessOptions{
		StateDir: opts.StateDir, WorkspaceID: opts.WorkspaceID,
		ExecPath: opts.ExecPath, WorkerID: r.Key, ModelRef: r.ModelRef,
		Capability:    hostworker.DefaultCapability,
		TargetBaseURL: "http://" + net.JoinHostPort(r.Host, strconv.Itoa(r.Port)) + "/v1",
		Mode:          mode, PolicyURL: opts.PolicyURL, PolicyFile: opts.PolicyFile,
		PolicyTimeout: opts.PolicyTimeout, UpstreamTimeout: 180 * time.Second,
	})
	if err != nil {
		return Attachment{}, err
	}
	return Attachment{Target: net.JoinHostPort(rec.Host, strconv.Itoa(rec.Port)),
		Mode: string(rec.Mode), PID: rec.PID, Port: rec.Port, AuditLogPath: rec.AuditLogPath}, nil
}

func Release(stateDir, workspaceID string) error {
	return hostworker.ReleaseProcess(stateDir, workspaceID, hostworker.DefaultCapability)
}

// UpstreamResolver resolves on each new connection or mediated request. Empty
// means no live runner; the transport decides whether a bootstrap fallback is
// supported. A fallback runner warning is emitted at most once per resolver.
func UpstreamResolver(stateDir, runnerKey, modelRef string, warnings io.Writer) func() string {
	var once sync.Once
	return func() string {
		r, ok := modelrunner.FindByKeyOrModelRef(stateDir, runnerKey, modelRef)
		if !ok {
			return ""
		}
		if runnerKey != "" && r.Key != runnerKey && warnings != nil {
			once.Do(func() {
				_, _ = fmt.Fprintf(warnings, "model runner key %q unavailable; forwarding model %q through fallback runner %q\n", runnerKey, modelRef, r.Key)
			})
		}
		return net.JoinHostPort(r.Host, strconv.Itoa(r.Port))
	}
}
