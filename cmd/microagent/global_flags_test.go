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
		fmt  string
	}{
		{"before", []string{"--json", "list"}, []string{"list"}, "json"},
		{"after", []string{"list", "--json"}, []string{"list"}, "json"},
		{"removed mode untouched", []string{"list", "--mode", "ax"}, []string{"list", "--mode", "ax"}, ""},
		{"output text extracts", []string{"list", "--output", "text"}, []string{"list"}, "text"},
		{"stops at double dash", []string{"exec", "web", "--", "tool", "--json"}, []string{"exec", "web", "--", "tool", "--json"}, ""},
		{"trailing args protect guest", []string{"run", "alpine", "mytool", "--json"}, []string{"run", "alpine", "mytool", "--json"}, ""},
		{"run flags before image", []string{"run", "--json", "alpine", "echo", "hi"}, []string{"run", "alpine", "echo", "hi"}, "json"},
		{"flag-form run tail", []string{"run", "--image", "alpine", "--exec", "echo hi", "--json"}, []string{"run", "--image", "alpine", "--exec", "echo hi"}, "json"},
		{"create artifact output untouched", []string{"create", "--name", "x", "--output", "res=/out.txt"}, []string{"create", "--name", "x", "--output", "res=/out.txt"}, ""},
		{"list output json", []string{"list", "--output", "json"}, []string{"list"}, "json"},
		{"list output text", []string{"list", "--output=text"}, []string{"list"}, "text"},
		{"removed alias left for loud failure", []string{"create", "--json", "request.json"}, []string{"create", "--json", "request.json"}, ""},
		{"stdin alias left for loud failure", []string{"create", "--json", "-"}, []string{"create", "--json", "-"}, ""},
		{"status json with name", []string{"status", "--json", "web"}, []string{"status", "web"}, "json"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outputFormat = ""
			got := parseGlobalFlags(tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("args: got %v want %v", got, tc.want)
			}
			if outputFormat != tc.fmt {
				t.Errorf("format: got %q want %q", outputFormat, tc.fmt)
			}
		})
	}
	outputFormat = ""
}

func TestParseGlobalFlagsNoColor(t *testing.T) {
	cases := []struct {
		in          []string
		want        []string
		wantNoColor bool
	}{
		{[]string{"--no-color", "list"}, []string{"list"}, true},
		{[]string{"list", "--no-color"}, []string{"list"}, true},
		{[]string{"run", "alpine", "mytool", "--no-color"}, []string{"run", "alpine", "mytool", "--no-color"}, false},
	}
	for _, tc := range cases {
		noColorFlag = false
		got := parseGlobalFlags(tc.in)
		if !reflect.DeepEqual(got, tc.want) || noColorFlag != tc.wantNoColor {
			t.Errorf("parseGlobalFlags(%v) = %v/no-color=%v, want %v/%v", tc.in, got, noColorFlag, tc.want, tc.wantNoColor)
		}
	}
	noColorFlag = false
}

func TestParseGlobalFlagsProgress(t *testing.T) {
	cases := []struct {
		in           []string
		want         []string
		wantProgress string
	}{
		{[]string{"--progress", "plain", "list"}, []string{"list"}, progressPlain},
		{[]string{"list", "--progress=off"}, []string{"list"}, progressOff},
		{[]string{"run", "alpine", "mytool", "--progress", "off"}, []string{"run", "alpine", "mytool", "--progress", "off"}, ""},
		{[]string{"list", "--progress", "invalid"}, []string{"list", "--progress", "invalid"}, ""},
	}
	for _, tc := range cases {
		progressFormat = ""
		got := parseGlobalFlags(tc.in)
		if !reflect.DeepEqual(got, tc.want) || progressFormat != tc.wantProgress {
			t.Errorf("parseGlobalFlags(%v) = %v/progress=%q, want %v/%q", tc.in, got, progressFormat, tc.want, tc.wantProgress)
		}
	}
	progressFormat = ""
}

func TestResolvedProgressFormatPrecedence(t *testing.T) {
	progressFormat = ""
	t.Cleanup(func() { progressFormat = "" })
	t.Setenv("MICROAGENT_PROGRESS", "plain")
	if got := resolvedProgressFormat(); got != progressPlain {
		t.Fatalf("environment progress = %q", got)
	}
	progressFormat = progressOff
	if got := resolvedProgressFormat(); got != progressOff {
		t.Fatalf("explicit progress = %q", got)
	}
}

func TestParseGlobalFlagsLeavesSpecialModeArgvUntouched(t *testing.T) {
	for _, in := range [][]string{
		{"--host-worker-mediator", "--socket", "x", "--mode", "policy"},
		{"--egress-datapath", "--mode", "json"},
	} {
		outputFormat = ""
		if got := parseGlobalFlags(in); !reflect.DeepEqual(got, in) {
			t.Errorf("args: got %v want %v", got, in)
		}
	}
}

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

func TestOutputJSONPrecedence(t *testing.T) {
	t.Run("explicit text", func(t *testing.T) {
		outputFormat = "text"
		t.Cleanup(func() { outputFormat = "" })
		if outputJSON(pipeStdoutForTest(t)) {
			t.Fatal("explicit text output resolved to JSON")
		}
	})
	t.Run("environment text", func(t *testing.T) {
		outputFormat = ""
		t.Setenv("MICROAGENT_OUTPUT", "text")
		if outputJSON(pipeStdoutForTest(t)) {
			t.Fatal("MICROAGENT_OUTPUT=text resolved to JSON")
		}
	})
	t.Run("pipe defaults to json", func(t *testing.T) {
		outputFormat = ""
		t.Setenv("MICROAGENT_OUTPUT", "")
		if !outputJSON(pipeStdoutForTest(t)) {
			t.Fatal("non-terminal pipe did not default to JSON")
		}
	})
}
