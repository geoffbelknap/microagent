// Package modelcompanion implements the model service child-process command.
package modelcompanion

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/modelservice"
)

// ErrArguments identifies invalid companion arguments.
var ErrArguments = errors.New("invalid model service arguments")

// Run serves the companion protocol used by modelservice.Attach. Readiness is
// written to ready, diagnostics to stderr. It runs until cancellation or signal.
func Run(ctx context.Context, args []string, ready, stderr io.Writer) error {
	if len(args) > 0 && args[0] == "--host-worker-mediator" {
		args = args[1:]
	}
	fs := flag.NewFlagSet("host-worker-mediator", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var opts hostworker.Options
	var mode string
	var logPath string
	var modelRef string
	var stateDir string
	fs.StringVar(&opts.TargetBaseURL, "target-base-url", "", "Target worker base URL")
	fs.StringVar(&opts.BindHost, "bind-host", "127.0.0.1", "Bind host")
	fs.IntVar(&opts.BindPort, "bind-port", 0, "Bind port")
	fs.StringVar(&mode, "mode", string(hostworker.ModeLocalAllow), "Mediation mode")
	fs.StringVar(&opts.PolicyURL, "policy-url", "", "Policy endpoint URL")
	fs.StringVar(&opts.PolicyFile, "policy-file", "", "Policy JSON file path")
	fs.DurationVar(&opts.PolicyTimeout, "policy-timeout", 2*time.Second, "Policy timeout")
	fs.StringVar(&opts.WorkspaceID, "workspace-id", "", "Workspace ID")
	fs.StringVar(&opts.Capability, "capability", hostworker.DefaultCapability, "Capability")
	fs.StringVar(&opts.WorkerID, "worker-id", "", "Worker ID")
	fs.DurationVar(&opts.UpstreamTimeout, "upstream-timeout", 180*time.Second, "Upstream timeout")
	fs.StringVar(&logPath, "log-path", "", "JSONL audit log path")
	fs.StringVar(&modelRef, "model-ref", "", "Canonical model ref of the runner this mediator fronts")
	fs.StringVar(&stateDir, "state-dir", "", "State directory holding the model runner registry")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return err
		}
		return fmt.Errorf("%w: %s", ErrArguments, err)
	}
	if fs.NArg() != 0 || strings.TrimSpace(opts.TargetBaseURL) == "" {
		return fmt.Errorf("usage: microagent --host-worker-mediator --target-base-url <url> [--bind-host <host>] [--bind-port <port>] [--mode forward|local-allow|policy] [--policy-url <url>|--policy-file <path>] [--log-path <path>] [--model-ref <ref> --state-dir <dir>]")
	}
	opts.Mode = hostworker.Mode(mode)
	opts.Ready = ready
	if strings.TrimSpace(modelRef) != "" && strings.TrimSpace(stateDir) != "" {
		// The guest's vsock forward is pinned to this mediator, so a runner
		// restart has to be absorbed here: resolve the current runner for the
		// ref before each proxied request instead of holding the address the
		// runner happened to have at spawn.
		opts.ResolveUpstreamHost = modelservice.UpstreamResolver(stateDir, opts.WorkerID, modelRef, stderr)
	}
	var logger *hostworker.JSONLLogger
	if strings.TrimSpace(logPath) != "" {
		var err error
		logger, err = hostworker.OpenJSONLLogger(logPath)
		if err != nil {
			return err
		}
		defer func() { _ = logger.Close() }()
		opts.Logger = logger
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	return hostworker.Run(ctx, opts)
}
