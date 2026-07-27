package main

import (
	"bytes"
	"flag"
	"strings"
	"testing"
)

// TestAliasesCollapseOntoOneLine pins the fix for help that listed every
// spelling of an option as if it were a separate option.
//
// Aliases are registered as distinct flag.Flags bound to the same variable, so a
// plain VisitAll showed `delete --help` four entries for two options — --f and
// --force, --y and --yes — and rendered the single-letter ones as the
// nonexistent "--f" and "--y".
func TestAliasesCollapseOntoOneLine(t *testing.T) {
	var force, yes bool
	var stateDir string
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.BoolVar(&force, "force", false, "Kill a running workspace before deleting")
	fs.BoolVar(&force, "f", false, "Kill a running workspace before deleting")
	fs.BoolVar(&yes, "yes", false, "Confirm workspace deletion without prompting")
	fs.BoolVar(&yes, "y", false, "Confirm workspace deletion without prompting")
	fs.StringVar(&stateDir, "state-dir", "", "State directory")

	opts := collapsedFlags(fs)
	if len(opts) != 3 {
		t.Fatalf("got %d options, want 3 (force, state-dir, yes): %+v", len(opts), opts)
	}

	labels := make([]string, 0, len(opts))
	for _, o := range opts {
		labels = append(labels, o.label)
	}
	joined := strings.Join(labels, " | ")
	for _, want := range []string{"--force, -f", "--yes, -y", "--state-dir"} {
		if !strings.Contains(joined, want) {
			t.Errorf("labels %q missing %q", joined, want)
		}
	}
	// The long name must lead: it is canonical, and docs-parity reads the first
	// flag on each help line against the CLI pages, which document long names.
	for _, o := range opts {
		if strings.HasPrefix(o.label, "-") && !strings.HasPrefix(o.label, "--") {
			t.Errorf("label %q leads with a short alias; the canonical spelling must come first", o.label)
		}
	}
}

// TestSingleLetterFlagsRenderWithOneDash covers the spelling half. The generator
// hardcoded two dashes, so an alias like -f appeared as "--f", which is not a
// flag the command accepts.
func TestSingleLetterFlagsRenderWithOneDash(t *testing.T) {
	if got := flagLabel("f"); got != "-f" {
		t.Errorf("flagLabel(\"f\") = %q, want \"-f\"", got)
	}
	if got := flagLabel("force"); got != "--force" {
		t.Errorf("flagLabel(\"force\") = %q, want \"--force\"", got)
	}
}

// TestDistinctFlagsAreNeverMerged is the safety property. Grouping is by the
// variable a flag writes to, not by matching description text — two unrelated
// options could describe themselves identically, and merging those would claim a
// spelling the command does not accept.
func TestDistinctFlagsAreNeverMerged(t *testing.T) {
	var a, b bool
	fs := flag.NewFlagSet("twins", flag.ContinueOnError)
	fs.BoolVar(&a, "alpha", false, "identical description")
	fs.BoolVar(&b, "beta", false, "identical description")

	opts := collapsedFlags(fs)
	if len(opts) != 2 {
		t.Fatalf("got %d options, want 2; flags sharing a description are not aliases: %+v", len(opts), opts)
	}
	for _, o := range opts {
		if strings.Contains(o.label, ",") {
			t.Errorf("label %q merged two distinct options", o.label)
		}
	}
}

// TestCollapsedUsageComesFromTheCanonicalName guards a subtlety worth keeping:
// VisitAll is alphabetical, so taking the first-visited flag's text described
// the "--name, --id" pair as "Workspace ID" rather than "Workspace name".
func TestCollapsedUsageComesFromTheCanonicalName(t *testing.T) {
	var target string
	fs := flag.NewFlagSet("delete", flag.ContinueOnError)
	fs.StringVar(&target, "id", "", "Workspace ID")
	fs.StringVar(&target, "name", "", "Workspace name")

	opts := collapsedFlags(fs)
	if len(opts) != 1 {
		t.Fatalf("got %d options, want 1: %+v", len(opts), opts)
	}
	if opts[0].usage != "Workspace name" {
		t.Errorf("usage = %q, want the canonical name's description %q", opts[0].usage, "Workspace name")
	}
}

// TestInternalFlagsStayHidden keeps the existing exclusion working through the
// rewrite.
func TestInternalFlagsStayHidden(t *testing.T) {
	var shown, hidden string
	fs := flag.NewFlagSet("cmd", flag.ContinueOnError)
	fs.StringVar(&shown, "visible", "", "A documented option")
	fs.StringVar(&hidden, "plumbing", "", "(internal) not for users")

	var buf bytes.Buffer
	printGeneratedCommandHelp(&buf, fs)
	if strings.Contains(buf.String(), "plumbing") {
		t.Errorf("internal flag leaked into help:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "--visible") {
		t.Errorf("documented flag missing from help:\n%s", buf.String())
	}
}
