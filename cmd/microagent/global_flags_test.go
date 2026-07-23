package main

import (
	"os"
	"reflect"
	"testing"
)

func TestParseGlobalFlagsAnywhere(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
		mode outputMode
		fmt_ string
	}{
		{"before", []string{"--json", "list"}, []string{"list"}, "", "json"},
		{"after", []string{"list", "--json"}, []string{"list"}, "", "json"},
		{"mode after", []string{"list", "--mode", "ax"}, []string{"list"}, outputModeAX, ""},
		{"text no longer global", []string{"list", "--text"}, []string{"list", "--text"}, "", ""},
		{"human no longer global", []string{"--human", "list"}, []string{"--human", "list"}, "", ""},
		{"output text extracts", []string{"list", "--output", "text"}, []string{"list"}, "", "text"},
		{"mode synonym no longer recognized", []string{"list", "--mode", "json"}, []string{"list", "--mode", "json"}, "", ""},
		{"stops at double dash", []string{"exec", "web", "--", "tool", "--json"},
			[]string{"exec", "web", "--", "tool", "--json"}, "", ""},
		{"trailing args protect guest", []string{"run", "alpine", "mytool", "--json"},
			[]string{"run", "alpine", "mytool", "--json"}, "", ""},
		{"run flags before image ok", []string{"run", "--json", "alpine", "echo", "hi"},
			[]string{"run", "alpine", "echo", "hi"}, "", "json"},
		{"flag-form run tail extracted", []string{"run", "--image", "alpine", "--exec", "echo hi", "--json"},
			[]string{"run", "--image", "alpine", "--exec", "echo hi"}, "", "json"},
		{"flag-form dispatch tail extracted", []string{"dispatch", "--image", "alpine", "--exec", "echo hi", "--json"},
			[]string{"dispatch", "--image", "alpine", "--exec", "echo hi"}, "", "json"},
		{"flag-form run global after command word", []string{"run", "--json", "--image", "alpine", "--exec", "echo hi"},
			[]string{"run", "--image", "alpine", "--exec", "echo hi"}, "", "json"},
		{"flag-form run guest positional still protects tail", []string{"run", "--image", "alpine", "mytool", "--json"},
			[]string{"run", "--image", "alpine", "mytool", "--json"}, "", ""},
		{"flag-form run inline value flag extracts mode", []string{"run", "--image=alpine", "--exec", "echo hi", "--mode", "ax"},
			[]string{"run", "--image=alpine", "--exec", "echo hi"}, outputModeAX, ""},
		{"bare positional image with trailing --json untouched", []string{"run", "alpine", "--json"},
			[]string{"run", "alpine", "--json"}, "", ""},
		{"create output artifact flag untouched", []string{"create", "--name", "x", "--output", "res=/out.txt"},
			[]string{"create", "--name", "x", "--output", "res=/out.txt"}, "", ""},
		{"run output artifact flag untouched", []string{"run", "--image", "alpine", "--output", "res=/o.txt", "--exec", "echo hi"},
			[]string{"run", "--image", "alpine", "--output", "res=/o.txt", "--exec", "echo hi"}, "", ""},
		{"list output json extracted", []string{"list", "--output", "json"},
			[]string{"list"}, "", "json"},
		{"list output=text extracted", []string{"list", "--output=text"},
			[]string{"list"}, "", "text"},
		{"output human value no longer recognized", []string{"list", "--output", "human"},
			[]string{"list", "--output", "human"}, "", ""},
		{"list unrecognized mode value untouched", []string{"list", "--mode", "policy"},
			[]string{"list", "--mode", "policy"}, "", ""},
		{"post-command json on request-json command extracts (alias removed)", []string{"create", "--json", "request.json"},
			[]string{"create", "request.json"}, "", "json"},
		{"post-command json on request-json command extracts (alias removed, stdin shape)", []string{"create", "--json", "-"},
			[]string{"create", "-"}, "", "json"},
		{"global json before request-json command extracts", []string{"--json", "create", "--name", "x"},
			[]string{"create", "--name", "x"}, "", "json"},
		{"list post-command json still extracts", []string{"list", "--json"},
			[]string{"list"}, "", "json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			globalOutputMode = ""
			outputFormat = ""
			got := parseGlobalFlags(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("args: got %v want %v", got, tc.want)
			}
			if globalOutputMode != tc.mode || outputFormat != tc.fmt_ {
				t.Errorf("mode/format: got %q/%q want %q/%q", globalOutputMode, outputFormat, tc.mode, tc.fmt_)
			}
		})
	}
	globalOutputMode = ""
	outputFormat = ""
}

// TestParseGlobalFlagsLeavesSpecialModeArgvUntouched pins C1: parseGlobalFlags
// must not walk a special-mode re-exec argv (windows-hyperv-listener,
// windows-hyperv-deadman, host-worker-mediator, egress-datapath) looking for
// global flags. Those argvs are built and spawned internally with their own
// "--mode"/"--output"-shaped flags (see internal/hostworker/process.go),
// which are not the global output flags and must reach that mode's own flag
// parsing verbatim.
func TestParseGlobalFlagsLeavesSpecialModeArgvUntouched(t *testing.T) {
	cases := [][]string{
		{"--host-worker-mediator", "--socket", "x", "--mode", "policy"},
		{"--windows-hyperv-listener", "--state-dir", "d", "--name", "n"},
		{"--windows-hyperv-deadman", "--state-dir", "d", "--name", "n"},
		{"--egress-datapath", "--mode", "json"},
	}
	for _, in := range cases {
		t.Run(in[0], func(t *testing.T) {
			globalOutputMode = ""
			outputFormat = ""
			got := parseGlobalFlags(in)
			if !reflect.DeepEqual(got, in) {
				t.Errorf("args: got %v want %v (verbatim)", got, in)
			}
			if globalOutputMode != "" || outputFormat != "" {
				t.Errorf("globals must stay unset: mode=%q format=%q", globalOutputMode, outputFormat)
			}
		})
	}
	globalOutputMode = ""
	outputFormat = ""
}

// pipeStdoutForTest returns the write end of an anonymous pipe, which
// outputJSON's TTY fallback sees as a non-terminal (like a redirected/piped
// process stdout), so the fallback path resolves to "json".
func pipeStdoutForTest(t *testing.T) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	t.Cleanup(func() {
		r.Close()
		w.Close()
	})
	return w
}

// TestOutputJSONPrecedence pins the precedence order required after AX no
// longer unconditionally forces JSON: explicit format flag > MICROAGENT_OUTPUT
// env > (mode==AX -> json) > TTY detection.
func TestOutputJSONPrecedence(t *testing.T) {
	resetGlobals := func() {
		globalOutputMode = ""
		outputFormat = ""
	}

	t.Run("AX with no explicit format defaults to json", func(t *testing.T) {
		resetGlobals()
		t.Cleanup(resetGlobals)
		t.Setenv("MICROAGENT_OUTPUT", "")
		globalOutputMode = outputModeAX
		if !outputJSON(pipeStdoutForTest(t)) {
			t.Fatal("want true (AX defaults to json)")
		}
	})

	t.Run("AX with explicit outputFormat text wins over AX default", func(t *testing.T) {
		resetGlobals()
		t.Cleanup(resetGlobals)
		t.Setenv("MICROAGENT_OUTPUT", "")
		globalOutputMode = outputModeAX
		outputFormat = "text"
		if outputJSON(pipeStdoutForTest(t)) {
			t.Fatal("want false (explicit --output text wins even under AX)")
		}
	})

	t.Run("AX with MICROAGENT_OUTPUT=text wins over AX default", func(t *testing.T) {
		resetGlobals()
		t.Cleanup(resetGlobals)
		t.Setenv("MICROAGENT_OUTPUT", "text")
		globalOutputMode = outputModeAX
		if outputJSON(pipeStdoutForTest(t)) {
			t.Fatal("want false (MICROAGENT_OUTPUT=text wins even under AX)")
		}
	})

	t.Run("MICROAGENT_OUTPUT=human is no longer recognized, falls through to TTY", func(t *testing.T) {
		resetGlobals()
		t.Cleanup(resetGlobals)
		t.Setenv("MICROAGENT_OUTPUT", "human")
		t.Setenv("MICROAGENT_MODE", "")
		if !outputJSON(pipeStdoutForTest(t)) {
			t.Fatal("want true (non-terminal pipe, human no longer a recognized MICROAGENT_OUTPUT value)")
		}
	})
}
