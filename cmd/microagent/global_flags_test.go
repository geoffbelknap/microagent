package main

import (
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
		{"list unrecognized mode value untouched", []string{"list", "--mode", "policy"},
			[]string{"list", "--mode", "policy"}, "", ""},
		{"create request-json compat alias untouched", []string{"create", "--json", "request.json"},
			[]string{"create", "--json", "request.json"}, "", ""},
		{"create request-json stdin compat alias untouched", []string{"create", "--json", "-"},
			[]string{"create", "--json", "-"}, "", ""},
		{"status request-json compat alias untouched", []string{"status", "--json", "req.json"},
			[]string{"status", "--json", "req.json"}, "", ""},
		{"halt request-json compat alias untouched", []string{"halt", "--json", "req.json"},
			[]string{"halt", "--json", "req.json"}, "", ""},
		{"rm alias inherits delete request-json guard", []string{"rm", "--json", "req.json"},
			[]string{"rm", "--json", "req.json"}, "", ""},
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
