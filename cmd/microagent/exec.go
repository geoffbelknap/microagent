package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/workspace"
	execprotocol "github.com/geoffbelknap/microagent/pkg/workspace/exec/protocol"
)

const (
	execServiceErrorExitCode = 2
	execTimeoutExitCode      = 124
	execSignaledExitCode     = 137
	execFailedToStartCode    = 127
)

// cliExitError carries an explicit process exit status. Silent errors are used
// when the command already wrote the workload result and no extra diagnostic
// should be printed.
type cliExitError struct {
	Code   int
	Silent bool
	Text   string
}

func (err cliExitError) Error() string {
	if err.Text != "" {
		return err.Text
	}
	return fmt.Sprintf("exit status %d", err.Code)
}

func runStructuredExec(ctx context.Context, args []string, stdout *os.File, stderr io.Writer) error {
	if len(args) == 0 || execWantsHelp(args) {
		printExecHelp(stdout)
		return nil
	}
	workspaceName := args[0]
	flagArgs, argv, err := splitExecArgs(args[1:])
	if err != nil {
		return err
	}
	if len(argv) == 0 {
		return fmt.Errorf("usage: microagent exec <workspace> [flags] -- <argv...>")
	}
	var envFlags multiFlag
	var timeout time.Duration
	var stdinPath string
	var stdoutLimit int64 = execprotocol.DefaultOutputLimitBytes
	var stderrLimit int64 = execprotocol.DefaultOutputLimitBytes
	stateDir := defaultStateDir()
	fs := newCommandFlagSet("exec")
	fs.Var(&envFlags, "env", "Environment variable KEY=VALUE")
	fs.Var(&envFlags, "e", "Environment variable KEY=VALUE")
	cwd := fs.String("cwd", "", "Working directory inside the workspace")
	stream := fs.Bool("stream", false, "Stream stdout/stderr incrementally as the command runs")
	fs.DurationVar(&timeout, "timeout", 0, "Command timeout")
	fs.StringVar(&stdinPath, "stdin", "", "Read command stdin from path, or '-' for stdin")
	fs.Int64Var(&stdoutLimit, "stdout-limit", stdoutLimit, "Stdout output limit in bytes")
	fs.Int64Var(&stderrLimit, "stderr-limit", stderrLimit, "Stderr output limit in bytes")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := parseCommandFlags(fs, stdout, flagArgs); err != nil {
		return err
	}
	env, err := parseEnvFlags(envFlags)
	if err != nil {
		return err
	}
	req := execprotocol.NewExecRequest(argv)
	req.Env = env
	req.Cwd = strings.TrimSpace(*cwd)
	if timeout > 0 {
		req.TimeoutMS = timeout.Milliseconds()
	}
	if stdoutLimit < 0 || stderrLimit < 0 {
		return fmt.Errorf("stdout-limit and stderr-limit must be non-negative")
	}
	req.OutputLimitBytesStdout = stdoutLimit
	req.OutputLimitBytesStderr = stderrLimit
	if stdinPath != "" {
		stdin, err := readExecStdin(stdinPath)
		if err != nil {
			return err
		}
		req.Stdin = stdin
	}
	if err := req.Validate(); err != nil {
		return err
	}
	opts := workspace.Options{Name: workspaceName, StateDir: stateDir}
	structured := outputStructured()
	if execUsesStreamingPath(*stream, structured) {
		return runStreamingExec(ctx, opts, req, stdout, stderr)
	}
	result, _, err := workspace.ExecWithMetadata(ctx, opts, req)
	if err != nil {
		return err
	}
	if structured {
		if err := writeJSON(stdout, result); err != nil {
			return err
		}
	} else {
		if _, err := stdout.Write(result.Stdout); err != nil {
			return err
		}
		if _, err := stderr.Write(result.Stderr); err != nil {
			return err
		}
	}
	if result.StdoutTruncated {
		fmt.Fprintf(stderr, "[microagent: stdout truncated at %d bytes]\n", len(result.Stdout))
	}
	if result.StderrTruncated {
		fmt.Fprintf(stderr, "[microagent: stderr truncated at %d bytes]\n", len(result.Stderr))
	}
	exitCode := structuredExecExitCode(result)
	if exitCode != 0 {
		return cliExitError{Code: exitCode, Silent: true}
	}
	return nil
}

// execUsesStreamingPath reports whether runStructuredExec should use the
// streaming transport (workspace.ExecStream) rather than the buffered one
// (workspace.ExecWithMetadata) when the caller passed --stream. Structured
// output forces the buffered path because raw bytes cannot be interleaved
// with JSON.
func execUsesStreamingPath(streamRequested, structured bool) bool {
	return streamRequested && !structured
}

func runStreamingExec(ctx context.Context, opts workspace.Options, req execprotocol.ExecRequest, stdout *os.File, stderr io.Writer) error {
	result, err := workspace.ExecStream(ctx, opts, req, func(kind execprotocol.ExecStreamKind, data []byte) {
		switch kind {
		case execprotocol.ExecStreamStdout:
			_, _ = stdout.Write(data)
		case execprotocol.ExecStreamStderr:
			_, _ = stderr.Write(data)
		}
	})
	if err != nil {
		return err
	}
	if result.StdoutTruncated {
		fmt.Fprintf(stderr, "[microagent: stdout truncated at the output limit]\n")
	}
	if result.StderrTruncated {
		fmt.Fprintf(stderr, "[microagent: stderr truncated at the output limit]\n")
	}
	exitCode := structuredExecExitCode(result)
	if exitCode != 0 {
		return cliExitError{Code: exitCode, Silent: true}
	}
	return nil
}

// execWantsHelp reports whether the CLI-side exec arguments ask for help.
// Everything after the "--" separator is the guest command's argv and must
// never trigger CLI help (exec ws -- psql -h x runs psql, it does not print
// usage).
func execWantsHelp(args []string) bool {
	for i, arg := range args {
		if arg == "--" {
			return wantsHelp(args[:i])
		}
	}
	return wantsHelp(args)
}

func splitExecArgs(args []string) ([]string, []string, error) {
	for i, arg := range args {
		if arg == "--" {
			return args[:i], args[i+1:], nil
		}
	}
	return nil, nil, fmt.Errorf("usage: microagent exec <workspace> [flags] -- <argv...>")
}

func readExecStdin(path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(path)
}

func structuredExecExitCode(result execprotocol.ExecResult) int {
	switch result.Status {
	case execprotocol.ExecStatusExited:
		if result.ExitCode == nil {
			return execFailedToStartCode
		}
		return normalizeProcessExitCode(*result.ExitCode)
	case execprotocol.ExecStatusTimedOut:
		return execTimeoutExitCode
	case execprotocol.ExecStatusSignaled:
		return execSignaledExitCode
	case execprotocol.ExecStatusFailedToStart:
		return execFailedToStartCode
	default:
		return execServiceErrorExitCode
	}
}

func normalizeProcessExitCode(code int) int {
	if code == 0 {
		return 0
	}
	if code < 0 {
		return 1
	}
	if code > 255 {
		return 255
	}
	return code
}

func printExecHelp(stdout *os.File) {
	fmt.Fprint(stdout, `microagent exec

Run a structured command in a running workspace.

Usage:
  microagent exec <workspace> [flags] -- <argv...>

Options:
  -env KEY=VALUE        Environment variable for the command
  -e KEY=VALUE          Environment variable for the command
  -cwd <path>           Working directory inside the workspace
  -stream               Stream stdout/stderr incrementally (human output)
  -timeout <duration>   Command timeout, e.g. 30s or 5m
  -stdin <path|- >      Read command stdin from a file or stdin
  -stdout-limit <bytes> Stdout output limit in bytes
  -stderr-limit <bytes> Stderr output limit in bytes
  -state-dir <dir>      State directory

Examples:
  microagent exec demo -- uname -a
  microagent exec demo -env FOO=bar -- sh -lc 'echo "$FOO"'
`)
}
