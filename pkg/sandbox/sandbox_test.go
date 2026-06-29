package sandbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// buildWASIModule compiles a Go program to wasm32-wasip1 (stdlib only → offline)
// and returns the .wasm path. Skips if the toolchain can't target wasip1.
func buildWASIModule(t *testing.T, mainSrc string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(mainSrc), 0o644); err != nil {
		t.Fatalf("write main.go: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module wasitest\n\ngo 1.26\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	out := filepath.Join(dir, "module.wasm")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", out, ".")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm", "GOFLAGS=-mod=mod")
	if combined, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot build wasip1 module (toolchain unavailable?): %v\n%s", err, combined)
	}
	return out
}

func buildWASIBytes(t *testing.T, mainSrc string) []byte {
	t.Helper()
	b, err := os.ReadFile(buildWASIModule(t, mainSrc))
	if err != nil {
		t.Fatalf("read module: %v", err)
	}
	return b
}

// Tier 0: a oneshot module's exit code and stdout/stderr round-trip through Run.
func TestRunCapturesExitAndOutput(t *testing.T) {
	wasm := buildWASIModule(t, `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Fprintln(os.Stdout, "hello-stdout")
	fmt.Fprintln(os.Stderr, "hello-stderr")
	os.Exit(7)
}
`)
	res, err := Run(context.Background(), Config{WasmPath: wasm})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit code: got %d want 7", res.ExitCode)
	}
	if !strings.Contains(res.Stdout, "hello-stdout") {
		t.Fatalf("stdout: %q", res.Stdout)
	}
	if !strings.Contains(res.Stderr, "hello-stderr") {
		t.Fatalf("stderr: %q", res.Stderr)
	}
}

// Args reach the guest as argv after argv[0]=="sandbox".
func TestRunPassesArgs(t *testing.T) {
	wasm := buildWASIBytes(t, `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() { fmt.Print(strings.Join(os.Args[1:], ",")) }
`)
	res, err := Run(context.Background(), Config{Module: wasm, Args: []string{"a", "b", "c"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "a,b,c" {
		t.Fatalf("args: got %q want a,b,c", res.Stdout)
	}
}

// Stdin is delivered to the guest; output is the transform of input.
func TestRunPassesStdin(t *testing.T) {
	wasm := buildWASIBytes(t, `package main

import (
	"io"
	"os"
	"strings"
)

func main() {
	b, _ := io.ReadAll(os.Stdin)
	os.Stdout.WriteString(strings.ToUpper(string(b)))
}
`)
	res, err := Run(context.Background(), Config{Module: wasm, Stdin: strings.NewReader("query me")})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != "QUERY ME" {
		t.Fatalf("stdin transform: got %q want QUERY ME", res.Stdout)
	}
}

// Env is least-privilege: the guest sees ONLY the keys Config grants — a key it
// was not given (even one set in the host process) reads back empty.
func TestRunEnvIsLeastPrivilege(t *testing.T) {
	t.Setenv("SANDBOX_HOST_ONLY", "leaked")
	wasm := buildWASIBytes(t, `package main

import (
	"fmt"
	"os"
)

func main() {
	fmt.Printf("granted=%q host_only=%q", os.Getenv("GRANTED"), os.Getenv("SANDBOX_HOST_ONLY"))
}
`)
	res, err := Run(context.Background(), Config{Module: wasm, Env: map[string]string{"GRANTED": "ok"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Stdout != `granted="ok" host_only=""` {
		t.Fatalf("env least-privilege: got %q (host env must not leak)", res.Stdout)
	}
}

// The poolable path: Compile once, instantiate many. Each Run is a fresh,
// isolated guest — a second run with different args is unaffected by the first.
func TestRuntimePoolReusesCompilation(t *testing.T) {
	wasm := buildWASIBytes(t, `package main

import (
	"fmt"
	"os"
	"strings"
)

func main() { fmt.Print(strings.Join(os.Args[1:], "")) }
`)
	rt, err := Compile(context.Background(), wasm, RuntimeOptions{})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	defer rt.Close(context.Background())

	for _, want := range []string{"first", "second", "third"} {
		res, err := rt.Run(context.Background(), Config{Args: []string{want}})
		if err != nil {
			t.Fatalf("Run(%s): %v", want, err)
		}
		if res.Stdout != want {
			t.Fatalf("pooled run: got %q want %q", res.Stdout, want)
		}
	}
}

// Bounds: an absurdly low memory cap fails the module closed at instantiation
// rather than letting it run unbounded (ASK tenet 8).
func TestRunMemoryLimitFailsClosed(t *testing.T) {
	wasm := buildWASIBytes(t, `package main

func main() {}
`)
	_, err := Run(context.Background(), Config{Module: wasm, Limits: Limits{MaxMemoryPages: 1}})
	if err == nil {
		t.Fatal("expected instantiation to fail closed under a 1-page memory cap")
	}
}

// Bounds: a runaway guest is interrupted at the Timeout rather than blocking the
// host forever (the runtime is built WithCloseOnContextDone).
func TestRunTimeoutInterrupts(t *testing.T) {
	wasm := buildWASIBytes(t, `package main

func main() {
	x := 0
	for {
		x++
		_ = x
	}
}
`)
	start := time.Now()
	_, err := Run(context.Background(), Config{Module: wasm, Limits: Limits{Timeout: 300 * time.Millisecond}})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a runaway guest to be interrupted, got nil error")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("timeout did not interrupt promptly: %v", elapsed)
	}
}

// A Config with no module to run is an error, not a silent no-op.
func TestRunNoModuleErrors(t *testing.T) {
	_, err := Run(context.Background(), Config{})
	if err == nil {
		t.Fatal("expected an error when neither Module nor WasmPath is set")
	}
}
