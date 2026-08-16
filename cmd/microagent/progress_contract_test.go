package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

func TestProgressCommandInventoryCoversRegistry(t *testing.T) {
	valid := map[progressBehavior]bool{
		progressBehaviorAnimated: true, progressBehaviorDeterminate: true,
		progressBehaviorDelayed: true, progressBehaviorReadiness: true,
		progressBehaviorStreaming: true, progressBehaviorNone: true,
	}
	seen := make(map[string]bool)
	for _, spec := range commandRegistry {
		if spec.Hidden {
			continue
		}
		behaviors, ok := progressCommandInventory[spec.Name]
		if !ok || len(behaviors) == 0 {
			t.Errorf("command %q has no progress classification", spec.Name)
			continue
		}
		seen[spec.Name] = true
		hasNone := false
		for _, behavior := range behaviors {
			if !valid[behavior] {
				t.Errorf("command %q has invalid progress classification %q", spec.Name, behavior)
			}
			hasNone = hasNone || behavior == progressBehaviorNone
		}
		if hasNone && len(behaviors) != 1 {
			t.Errorf("command %q mixes no-progress with active classifications: %v", spec.Name, behaviors)
		}
	}
	for name := range progressCommandInventory {
		if !seen[name] {
			t.Errorf("progress inventory contains unknown or hidden command %q", name)
		}
	}
}

func TestProgressPrintersAreCreatedOnlyBySurfacePolicyAdapters(t *testing.T) {
	allowed := map[string]map[string]bool{
		"newOperationProgressPrinter":   {"progress_output.go": true, "perf.go": true},
		"newCommandProgress":            {"progress_output.go": true},
		"newCommandProgressWithOptions": {"progress_output.go": true, "mcp.go": true},
	}
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Errorf("parse %s: %v", name, err)
			continue
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			ident, ok := call.Fun.(*ast.Ident)
			if !ok {
				return true
			}
			locations, tracked := allowed[ident.Name]
			if tracked && !locations[name] {
				t.Errorf("%s creates %s outside a progress surface policy adapter", name, ident.Name)
			}
			return true
		})
	}
}

func TestJSONSurfaceParsesAndCannotInstallHumanProgress(t *testing.T) {
	previousOutput := outputFormat
	previousProgress := progressFormat
	outputFormat = "json"
	progressFormat = progressPlain
	t.Cleanup(func() {
		outputFormat = previousOutput
		progressFormat = previousProgress
	})

	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
	})
	progress, finish := commandProgressFor(stdoutWriter, "contract-test", "Contract test")
	if progress != nil {
		t.Fatal("JSON surface installed a human progress callback")
	}
	finish(nil)
	if err := writeJSON(stdoutWriter, map[string]any{"ok": true, "surface": "json"}); err != nil {
		t.Fatal(err)
	}
	if err := stdoutWriter.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(stdoutReader)
	if err != nil {
		t.Fatal(err)
	}
	assertNoTerminalPresentation(t, string(raw))
	var decoded struct {
		OK      bool   `json:"ok"`
		Surface string `json:"surface"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("structured stdout did not parse: %v", err)
	}
	if !decoded.OK || decoded.Surface != "json" {
		t.Fatalf("decoded output = %+v", decoded)
	}
}

func TestProtocolPresentationIsDisabledWhenStderrIsRedirected(t *testing.T) {
	previousStderr := os.Stderr
	previousProgress := progressFormat
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = writer
	progressFormat = progressAuto
	t.Cleanup(func() {
		os.Stderr = previousStderr
		progressFormat = previousProgress
		_ = reader.Close()
		_ = writer.Close()
	})

	if enabled, interactive := protocolProgressPresentation(); enabled || interactive {
		t.Fatalf("redirected protocol presentation = enabled %v, interactive %v", enabled, interactive)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	data := make([]byte, 1)
	if n, err := reader.Read(data); n != 0 || err == nil {
		t.Fatalf("redirected protocol stderr unexpectedly contained data: %q, err=%v", data[:n], err)
	}
}

func TestPlainProgressUsesStderrAndLeavesRedirectedStdoutClean(t *testing.T) {
	previousOutput := outputFormat
	previousProgress := progressFormat
	previousStderr := os.Stderr
	outputFormat = "text"
	progressFormat = progressPlain
	stdoutReader, stdoutWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrReader, stderrWriter, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = stderrWriter
	t.Cleanup(func() {
		outputFormat = previousOutput
		progressFormat = previousProgress
		os.Stderr = previousStderr
		_ = stdoutReader.Close()
		_ = stdoutWriter.Close()
		_ = stderrReader.Close()
		_ = stderrWriter.Close()
	})

	progress, finish := commandProgressFor(stdoutWriter, "copy", "Copy file")
	progress(operation.ProgressEvent{Phase: "copying", Current: 1, Total: 2, ElapsedMs: 400})
	finish(nil)
	if err := stdoutWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := stderrWriter.Close(); err != nil {
		t.Fatal(err)
	}
	stdoutData, err := io.ReadAll(stdoutReader)
	if err != nil {
		t.Fatal(err)
	}
	stderrData, err := io.ReadAll(stderrReader)
	if err != nil {
		t.Fatal(err)
	}
	if len(stdoutData) != 0 {
		t.Fatalf("progress contaminated stdout: %q", stdoutData)
	}
	if len(stderrData) == 0 {
		t.Fatal("plain progress did not reach stderr")
	}
	assertNoTerminalPresentation(t, string(stderrData))
}

func assertNoTerminalPresentation(t *testing.T, data string) {
	t.Helper()
	if strings.Contains(data, "\x1b") {
		t.Fatalf("structured stream contains terminal controls: %q", data)
	}
	for _, frame := range progressSpinnerFrames {
		if strings.Contains(data, frame) {
			t.Fatalf("structured stream contains spinner frame %q: %q", frame, data)
		}
	}
}
