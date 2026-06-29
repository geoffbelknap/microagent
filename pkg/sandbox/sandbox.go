// Package sandbox is microagency's oneshot sandboxed-execution primitive: it runs
// a WASI (wasm32-wasip1) module in-process via wazero, with no virtualization.
//
// It is deliberately NOT a vmkit backend. A WASI module is not a microVM — it
// has no kernel, no rootfs, no exec/shell/snapshot — so modelling it behind the
// microVM Supervisor contract is the wrong abstraction (the Spike-1 "backend"
// fit was a false positive: it satisfied the interface but ~none of the
// semantics). This is a separate execution shape for oneshot, ephemeral,
// lower-risk delegated *work*. What it shares with the microVM workspaces is the
// governance brain: the module's only path to the network is a host-mediated
// egress capability backed by internal/egress (the same default-deny allowlist
// and audit the microVM mediator uses) — proving the brain, not the backend, is
// the shared substrate.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/internal/egress"
	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// Result is the outcome of one sandboxed execution.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// EgressConfig gives a sandboxed module a host-mediated egress capability backed
// by the shared egress brain (internal/egress): a default-deny allowlist and an
// audited decision for every destination the module asks to reach. The module
// has no sockets of its own — it reaches the network ONLY through this host
// function — so mediation is complete at the host boundary, with no netns or
// TPROXY (the host owns the call, so the destination is known up front).
type EgressConfig struct {
	Allow  []string      // default-deny destination allowlist (the brain's policy)
	Logger egress.Logger // audit sink; every decision is recorded (never a payload)
}

// Config configures one execution of a WASI module.
type Config struct {
	WasmPath string
	Args     []string
	// Egress, when set, installs the host-mediated egress capability. When nil,
	// the module has no egress capability at all (fail-closed by absence).
	Egress *EgressConfig
}

// Run executes a WASI module in-process via wazero and returns its exit code and
// captured stdout/stderr.
func Run(ctx context.Context, cfg Config) (Result, error) {
	wasmBytes, err := os.ReadFile(cfg.WasmPath)
	if err != nil {
		return Result{}, fmt.Errorf("read wasm module: %w", err)
	}

	var stdout, stderr bytes.Buffer
	rt := wazero.NewRuntime(ctx)
	defer rt.Close(ctx)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)

	if cfg.Egress != nil {
		if err := installEgress(ctx, rt, cfg.Egress); err != nil {
			return Result{}, err
		}
	}

	modCfg := wazero.NewModuleConfig().
		WithStdout(&stdout).
		WithStderr(&stderr).
		WithArgs(append([]string{"sandbox"}, cfg.Args...)...)

	exitCode := 0
	mod, instErr := rt.InstantiateWithConfig(ctx, wasmBytes, modCfg)
	if mod != nil {
		_ = mod.Close(ctx)
	}
	if instErr != nil {
		// proc_exit surfaces as *sys.ExitError — normal termination, any code.
		var exitErr *sys.ExitError
		if errors.As(instErr, &exitErr) {
			exitCode = int(exitErr.ExitCode())
		} else {
			return Result{Stdout: stdout.String(), Stderr: stderr.String()}, instErr
		}
	}
	return Result{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// installEgress instantiates the host module the guest imports for egress. The
// guest calls microagency.egress_allowed(ptr,len) with a destination host in its
// linear memory; the host reads it, asks the egress brain's default-deny policy,
// records the decision in the audit log, and returns 1 (allow) or 0 (deny). The
// decision IS internal/egress.Policy — the same brain the microVM mediator uses.
func installEgress(ctx context.Context, rt wazero.Runtime, cfg *EgressConfig) error {
	policy, err := egress.NewPolicy(cfg.Allow)
	if err != nil {
		return fmt.Errorf("egress policy: %w", err)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = &egress.BufferLogger{}
	}
	_, err = rt.NewHostModuleBuilder("microagency").
		NewFunctionBuilder().
		WithFunc(func(_ context.Context, mod api.Module, ptr, length uint32) int32 {
			buf, ok := mod.Memory().Read(ptr, length)
			if !ok {
				logger.Log("sandbox_egress_error", map[string]any{"reason": "bad memory range"})
				return -1
			}
			host := strings.TrimSpace(string(buf))
			d := policy.AllowHost(host)
			logger.Log("sandbox_egress_decision", map[string]any{
				"host": host, "allow": d.Allow, "reason": d.Reason,
			})
			if d.Allow {
				return 1
			}
			return 0
		}).
		Export("egress_allowed").
		Instantiate(ctx)
	return err
}
