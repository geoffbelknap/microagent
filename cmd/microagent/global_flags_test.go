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
