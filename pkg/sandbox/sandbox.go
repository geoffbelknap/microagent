// Package sandbox is microagent's wasm execution shape: it runs a WASI
// (wasm32-wasip1) module in-process via wazero, with no virtualization and no
// /dev/kvm. It is the cheap, poolable, millisecond substrate for oneshot,
// ephemeral, lower-risk delegated work — the counterpart to the microVM
// workspace shape, not a replacement for it.
//
// # What it is for
//
// wasm-native deterministic units of work an agent delegates off-context:
// data transforms, parsing, validation, a bundled query engine over bytes the
// host hands in. Input arrives as args + stdin (and, with an EgressConfig, a
// governed host-fetch capability — see Run); output leaves as stdout/stderr and
// the exit code. The same egress brain that governs the microVM path
// (internal/egress: default-deny allowlist + cred-blind credential swap + audit)
// governs a sandbox module's network through a host function, so a consumer gets
// the SAME guarantees regardless of shape.
//
// # The honest boundary (read this before routing risk here)
//
//   - A wasm sandbox is a SOFTWARE sandbox (wazero). Its isolation is weaker than
//     a hardware microVM. Right-size by risk: route low-risk, oneshot, ephemeral,
//     wasm-native work here; keep fat agents, Python, arbitrary binaries, and
//     stateful or higher-risk work in microVM workspaces.
//   - WASI preview1 has NO fork/exec. A wasm module therefore CANNOT run Python
//     or any external binary. This is structural, not a TODO. If your work needs
//     a subprocess, it does not belong in this shape.
//   - No host filesystem is exposed (no preopens) and no environment is inherited.
//     A module sees only what Config grants it (Args, Stdin, Env) — least
//     privilege by default (ASK tenet 7).
//   - Without an EgressConfig a module has NO network capability at all
//     (fail-closed by absence): a guest that tries to reach the network simply
//     has no import to call.
package sandbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
	"github.com/tetratelabs/wazero/sys"
)

// Limits bounds one sandboxed execution (ASK tenet 8 — operations are bounded).
// The zero value imposes no explicit bound beyond ctx and wazero's defaults.
type Limits struct {
	// Timeout bounds wall-clock time for a single execution. When > 0, Run derives
	// a child context with this deadline; a guest still running at the deadline is
	// interrupted (the runtime is built WithCloseOnContextDone). <= 0 relies on the
	// caller's ctx alone.
	Timeout time.Duration

	// MaxMemoryPages caps the guest's linear memory in 64 KiB WASM pages (e.g. 256
	// = 16 MiB). 0 leaves wazero's default ceiling. A module whose declared minimum
	// memory exceeds the cap fails to instantiate (fail-closed). Honored by Run and
	// by Compile via RuntimeOptions; it is a property of the runtime, so a pooled
	// Runtime fixes it at construction.
	MaxMemoryPages uint32
}

// Config configures one execution of a WASI module.
type Config struct {
	// Module is the wasm module binary. It takes precedence over WasmPath.
	Module []byte
	// WasmPath is read for the module binary when Module is nil.
	WasmPath string
	// Args is the guest argv after argv[0] (which is supplied as "sandbox").
	Args []string
	// Stdin is the guest's standard input. Nil is an empty stdin.
	Stdin interface{ Read([]byte) (int, error) }
	// Env is the explicit, least-privilege environment exposed to the guest. No
	// host environment is inherited; a module sees only these keys.
	Env map[string]string
	// Limits bounds this execution.
	Limits Limits
	// Egress, when set, installs the governed host-fetch capability: the guest's
	// ONLY path to the network is the microagency.fetch host function, mediated by
	// the shared egress brain (allowlist + guarded inside-deny + cred-blind
	// credential swap + audit). When nil, the module has no network capability at
	// all — fail-closed by absence (a guest that imports the capability fails to
	// instantiate). For a pooled Runtime, the capability is installed at Compile
	// (RuntimeOptions.Egress) and the per-run policy is supplied here.
	Egress *EgressConfig
}

// Result is the outcome of one sandboxed execution.
type Result struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

// Run executes a WASI module once and returns its exit code and captured
// stdout/stderr. It compiles the module, runs it, and tears the runtime down; for
// repeated execution of the same module use Compile + Runtime.Run, which compiles
// once and instantiates many (the poolable path).
func Run(ctx context.Context, cfg Config) (Result, error) {
	module, err := loadModule(cfg)
	if err != nil {
		return Result{}, err
	}
	rt, err := Compile(ctx, module, RuntimeOptions{
		MaxMemoryPages: cfg.Limits.MaxMemoryPages,
		Egress:         cfg.Egress != nil,
	})
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = rt.Close(ctx) }()
	return rt.Run(ctx, cfg)
}

// RuntimeOptions configures a pooled Runtime at construction.
type RuntimeOptions struct {
	// MaxMemoryPages caps guest linear memory for every module this Runtime runs
	// (see Limits.MaxMemoryPages). 0 leaves wazero's default ceiling.
	MaxMemoryPages uint32
	// Egress installs the microagency host-fetch capability on the runtime. The
	// per-run policy (allowlist, swaps, audit sink) is supplied via Config.Egress
	// at each Run; this only decides whether the capability EXISTS. A runtime built
	// without it cannot perform governed egress — a guest that imports the
	// capability fails to instantiate (fail-closed by absence).
	Egress bool
}

// Runtime is a compiled module bound to a wazero runtime: Compile pays the
// compilation cost ONCE, and Run instantiates a fresh, isolated guest per call
// (each instance is anonymous, so calls are safe to run concurrently). This is
// the poolable path that makes the shape cheap — a warm Runtime turns a oneshot
// unit of work into a sub-millisecond instantiate-run-discard.
type Runtime struct {
	rt       wazero.Runtime
	compiled wazero.CompiledModule
	// egress is the host-fetch capability's per-runtime state (the response stash),
	// non-nil when the runtime was built WithEgress. The per-run brain is supplied
	// via Config.Egress at Run time, not held here.
	egress *egressState
}

// Compile builds a wazero runtime (with WASI preview1, plus the microagency
// host-fetch capability when opts.Egress) and compiles module into it, returning
// a reusable Runtime. The caller owns it and must Close it.
func Compile(ctx context.Context, module []byte, opts RuntimeOptions) (*Runtime, error) {
	rc := wazero.NewRuntimeConfig().
		// Let ctx cancellation / a Limits.Timeout interrupt a runaway guest rather
		// than block a host goroutine indefinitely (ASK tenet 8).
		WithCloseOnContextDone(true)
	if opts.MaxMemoryPages > 0 {
		rc = rc.WithMemoryLimitPages(opts.MaxMemoryPages)
	}
	rt := wazero.NewRuntimeWithConfig(ctx, rc)
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("sandbox: instantiate wasi: %w", err)
	}
	r := &Runtime{rt: rt}
	if opts.Egress {
		r.egress = newEgressState()
		if err := r.egress.install(ctx, rt); err != nil {
			_ = rt.Close(ctx)
			return nil, fmt.Errorf("sandbox: install egress capability: %w", err)
		}
	}
	compiled, err := rt.CompileModule(ctx, module)
	if err != nil {
		_ = rt.Close(ctx)
		return nil, fmt.Errorf("sandbox: compile module: %w", err)
	}
	r.compiled = compiled
	return r, nil
}

// Close releases the runtime and every resource it holds.
func (r *Runtime) Close(ctx context.Context) error { return r.rt.Close(ctx) }

// Run instantiates the compiled module and runs it to completion. cfg's module
// source (Module/WasmPath) and MaxMemoryPages are ignored — the module is already
// compiled and the memory ceiling is fixed at Compile — while Args, Stdin, Env,
// and Limits.Timeout apply per call.
func (r *Runtime) Run(ctx context.Context, cfg Config) (Result, error) {
	if cfg.Limits.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, cfg.Limits.Timeout)
		defer cancel()
	}

	// Bind this run's governance brain into the context the host-fetch functions
	// read. A run that supplies an EgressConfig requires a runtime that installed
	// the capability; otherwise the guest's import is unresolved and instantiation
	// fails fail-closed.
	if cfg.Egress != nil {
		if r.egress == nil {
			return Result{}, errors.New("sandbox: Config.Egress set but runtime was built without egress (RuntimeOptions.Egress)")
		}
		brain, err := cfg.Egress.brain()
		if err != nil {
			return Result{}, err
		}
		ctx = withBrain(ctx, brain)
	}

	var stdout, stderr bytes.Buffer
	modCfg := wazero.NewModuleConfig().
		WithStdout(&stdout).
		WithStderr(&stderr).
		// Anonymous instance: no name registration, so concurrent Run calls on one
		// Runtime do not collide on the module name.
		WithName("").
		WithArgs(append([]string{"sandbox"}, cfg.Args...)...)
	if cfg.Stdin != nil {
		modCfg = modCfg.WithStdin(cfg.Stdin)
	}
	for k, v := range cfg.Env {
		modCfg = modCfg.WithEnv(k, v)
	}

	exitCode := 0
	mod, instErr := r.rt.InstantiateModule(ctx, r.compiled, modCfg)
	if mod != nil {
		_ = mod.Close(ctx)
	}
	if instErr != nil {
		// proc_exit surfaces as *sys.ExitError — normal termination, any code —
		// EXCEPT the sentinel codes wazero uses when WithCloseOnContextDone tears a
		// runaway guest down: those are a bound being enforced, not a guest exit, so
		// they are an error (the work did not complete), never a "clean" exit code.
		var exitErr *sys.ExitError
		if errors.As(instErr, &exitErr) {
			switch exitErr.ExitCode() {
			case sys.ExitCodeDeadlineExceeded:
				return Result{Stdout: stdout.String(), Stderr: stderr.String()},
					fmt.Errorf("sandbox: execution deadline exceeded: %w", instErr)
			case sys.ExitCodeContextCanceled:
				return Result{Stdout: stdout.String(), Stderr: stderr.String()},
					fmt.Errorf("sandbox: execution canceled: %w", instErr)
			default:
				exitCode = int(exitErr.ExitCode())
			}
		} else {
			return Result{Stdout: stdout.String(), Stderr: stderr.String()}, instErr
		}
	}
	return Result{ExitCode: exitCode, Stdout: stdout.String(), Stderr: stderr.String()}, nil
}

// loadModule resolves the module binary from Config: Module wins, else WasmPath
// is read, else it is an error (a sandbox with no module to run).
func loadModule(cfg Config) ([]byte, error) {
	if len(cfg.Module) > 0 {
		return cfg.Module, nil
	}
	if cfg.WasmPath != "" {
		b, err := os.ReadFile(cfg.WasmPath)
		if err != nil {
			return nil, fmt.Errorf("sandbox: read wasm module: %w", err)
		}
		return b, nil
	}
	return nil, errors.New("sandbox: no module: set Config.Module or Config.WasmPath")
}
