package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestRegistryLookup(t *testing.T) {
	for name, want := range map[string]string{
		"run": "run", "ls": "list", "log": "logs", "rm": "delete", "inspect": "status", "stop": "halt",
	} {
		spec, ok := lookupCommand(name)
		if !ok || spec.Name != want {
			t.Errorf("lookupCommand(%q) = %v, %v; want spec %q", name, spec, ok, want)
		}
	}
	if _, ok := lookupCommand("frobnicate"); ok {
		t.Error("lookupCommand should miss unknown names")
	}
}

func TestStopIsHaltAlias(t *testing.T) {
	spec, ok := lookupCommand("stop")
	if !ok || spec.Name != "halt" {
		t.Fatalf("lookupCommand(%q) = %v, %v; want the halt spec", "stop", spec, ok)
	}
	for _, s := range commandRegistry {
		if s.Name == "stop" {
			t.Fatalf("stop must be an alias of halt, not a standalone command")
		}
	}
	found := false
	for _, a := range spec.Aliases {
		if a == "stop" {
			found = true
		}
	}
	if !found {
		t.Fatalf("halt spec must list stop as an alias, got %v", spec.Aliases)
	}
}

// TestLifecycleHelpInterception proves each lifecycle verb (and the stop alias)
// prints its hand-written when-to-use help on --help without touching a
// supervisor or the workspace state directory.
func TestLifecycleHelpInterception(t *testing.T) {
	cases := map[string]string{
		"halt":       "Park a workspace with a clean, disk-preserving shutdown",
		"stop":       "Park a workspace with a clean, disk-preserving shutdown",
		"kill":       "Force-terminate a workspace",
		"pause":      "Freeze a running workspace in place",
		"resume":     "Thaw a paused workspace back to running",
		"quarantine": "Freeze, sever, capture, and stop a workspace",
	}
	for verb, want := range cases {
		t.Run(verb, func(t *testing.T) {
			spec, ok := lookupCommand(verb)
			if !ok {
				t.Fatalf("lookupCommand(%q) missed", verb)
			}
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := spec.Run(t.Context(), []string{"--help"}, w); err != nil {
				t.Fatalf("%s --help: %v", verb, err)
			}
			w.Close()
			out, _ := io.ReadAll(r)
			if !strings.Contains(string(out), want) {
				t.Fatalf("%s --help output missing %q; got:\n%s", verb, want, out)
			}
		})
	}
}

// TestLifecycleRequestJSONRoutesToLowLevelPath is a regression test for a
// routing bug in hasWorkspaceStateTarget: its naive arg scan didn't know
// --request-json takes a value, so for `<verb> --request-json <path>` it
// walked straight into <path> (a bare file path with no "-" prefix) and
// misread it as a workspace-state target. That misrouted the invocation to
// the high-level workspace-state path, which doesn't define --request-json
// and died with an unknown-flag error before the request file was ever read.
// Both the space-separated and "="-joined forms must reach the low-level
// path (proven here by getting the file-open error, not an unknown-flag
// error) for every lifecycle verb that supports the request-file form.
func TestLifecycleRequestJSONRoutesToLowLevelPath(t *testing.T) {
	stateDir := t.TempDir()
	missing := filepath.Join(t.TempDir(), "nope.json")

	assertReachedLowLevel := func(t *testing.T, invocation string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s: want an error (missing request file), got nil", invocation)
		}
		if strings.Contains(err.Error(), "flag provided but not defined") {
			t.Fatalf("%s: misrouted to the high-level path (unknown-flag error): %v", invocation, err)
		}
		if !strings.Contains(err.Error(), "no such file or directory") {
			t.Fatalf("%s: err = %v, want the file-open error from the low-level request loader", invocation, err)
		}
	}

	for _, verb := range []string{"status", "halt", "delete"} {
		t.Run(verb+"/space-form", func(t *testing.T) {
			_, err := runMainForTest(t, verb, "--request-json", missing, "--state-dir", stateDir)
			assertReachedLowLevel(t, verb+" --request-json "+missing, err)
		})
		t.Run(verb+"/equals-form", func(t *testing.T) {
			_, err := runMainForTest(t, verb, "--request-json="+missing, "--state-dir", stateDir)
			assertReachedLowLevel(t, verb+" --request-json="+missing, err)
		})
	}

	// start shares the same class of bug via hasPositionalWorkspaceName,
	// which the same naive-scan defect affects for the same reason.
	t.Run("start/space-form", func(t *testing.T) {
		_, err := runMainForTest(t, "start", "--request-json", missing, "--state-dir", stateDir)
		assertReachedLowLevel(t, "start --request-json "+missing, err)
	})
}

func TestRegistryWellFormed(t *testing.T) {
	seen := map[string]string{}
	for _, spec := range commandRegistry {
		if spec.Summary == "" || spec.Group == "" {
			t.Errorf("%s: missing Summary or Group", spec.Name)
		}
		if spec.Hidden && spec.HiddenReason == "" {
			t.Errorf("%s: Hidden requires HiddenReason", spec.Name)
		}
		if spec.Run == nil {
			t.Errorf("%s: nil Run", spec.Name)
		}
		for _, n := range append([]string{spec.Name}, spec.Aliases...) {
			if prev, dup := seen[n]; dup {
				t.Errorf("name/alias %q claimed by both %s and %s", n, prev, spec.Name)
			}
			seen[n] = spec.Name
		}
	}
}

func TestPublicCommandRegistryMatchesLibraryOperations(t *testing.T) {
	for _, spec := range commandRegistry {
		if spec.Hidden || spec.NoDocs {
			continue
		}
		var canonical vmkit.OperationID
		for _, name := range append([]string{spec.Name}, spec.Aliases...) {
			operation, ok := vmkit.OperationForCLICommand(name)
			if !ok {
				t.Errorf("public CLI command %q has no library operation", name)
				continue
			}
			if operation.RequestType == "" || operation.ResultType == "" {
				t.Errorf("public CLI command %q operation %s has no request/result type", name, operation.ID)
			}
			if canonical == "" {
				canonical = operation.ID
			} else if operation.ID != canonical {
				t.Errorf("CLI alias %q maps to %s; canonical %q maps to %s", name, operation.ID, spec.Name, canonical)
			}
		}
	}
	for _, operation := range vmkit.OperationContracts() {
		for _, command := range operation.CLICommands {
			top := strings.Fields(command)
			if len(top) == 0 {
				t.Errorf("operation %s declares empty CLI command", operation.ID)
				continue
			}
			if _, ok := lookupCommand(top[0]); !ok {
				t.Errorf("operation %s declares CLI command %q with no public router", operation.ID, command)
			}
		}
	}
}

func TestHelpRendersFromRegistry(t *testing.T) {
	var full bytes.Buffer
	printCommandTable(&full, false)
	s := full.String()
	if !strings.Contains(s, "gc") || !strings.Contains(s, "Maintenance") {
		t.Error("full help must list gc under Maintenance")
	}
	if !strings.Contains(s, "delete, rm") {
		t.Error("aliases must render next to their command")
	}
	if strings.Contains(s, "compose") {
		t.Error("hidden commands must not render")
	}
	var curated bytes.Buffer
	printCommandTable(&curated, true)
	curatedStr := curated.String()
	if strings.Contains(curatedStr, "supervise") {
		t.Error("curated help must omit non-curated commands")
	}
	if !strings.Contains(curatedStr, "  volume ") {
		t.Error("curated help must list volume (parity with old curated help's Resources section)")
	}
	if !strings.Contains(curatedStr, "  secret ") {
		t.Error("curated help must list secret (parity with old curated help's Resources section)")
	}
	if strings.Contains(curatedStr, "  rootfs ") {
		t.Error("curated help must still omit rootfs")
	}
}

func TestFullHelpListsVersionAndHelp(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	printFullHelp(w)
	w.Close()
	out, _ := io.ReadAll(r)
	for _, want := range []string{"version", "help all"} {
		if !strings.Contains(string(out), want) {
			t.Errorf("full help missing %q", want)
		}
	}
}

func TestCanonicalSubverb(t *testing.T) {
	for in, want := range map[string]string{"ls": "list", "rm": "delete", "log": "logs", "inspect": "status", "create": "create"} {
		if got := canonicalSubverb(in); got != want {
			t.Errorf("canonicalSubverb(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRegistryDocsParity(t *testing.T) {
	indexBytes, err := os.ReadFile(filepath.Join("..", "..", "docs", "cli", "index.md"))
	if err != nil {
		t.Fatalf("read docs/cli/index.md: %v", err)
	}
	index := string(indexBytes)
	for _, spec := range commandRegistry {
		if spec.Hidden || spec.NoDocs {
			continue
		}
		page := filepath.Join("..", "..", "docs", "cli", spec.Name+".md")
		if _, err := os.Stat(page); err != nil {
			t.Errorf("%s: no docs page at docs/cli/%s.md", spec.Name, spec.Name)
		}
		if !strings.Contains(index, spec.Name) {
			t.Errorf("%s: missing from docs/cli/index.md", spec.Name)
		}
	}
}

func TestModelPolicyEvalAliasPreserved(t *testing.T) {
	// "eval" is a pre-existing spelling not covered by subverbAliases;
	// it must stay explicit in runModelPolicy's switch.
	if got := canonicalSubverb("eval"); got != "eval" {
		t.Fatalf("canonicalSubverb(eval) = %q; the switch must keep an explicit eval case", got)
	}
}
