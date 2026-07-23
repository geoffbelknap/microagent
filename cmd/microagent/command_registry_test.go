package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRegistryLookup(t *testing.T) {
	for name, want := range map[string]string{
		"run": "run", "ls": "list", "log": "logs", "rm": "delete", "inspect": "status",
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

func TestModelPolicyEvalAliasPreserved(t *testing.T) {
	// "eval" is a pre-existing spelling not covered by subverbAliases;
	// it must stay explicit in runModelPolicy's switch.
	if got := canonicalSubverb("eval"); got != "eval" {
		t.Fatalf("canonicalSubverb(eval) = %q; the switch must keep an explicit eval case", got)
	}
}
