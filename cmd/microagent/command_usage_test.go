package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docsSynopsisBlock returns the first ```text fence on a CLI page, which is
// where every page states how the command is invoked.
var docsSynopsisBlock = regexp.MustCompile("(?s)```text\n(.*?)```")

// parseDocsSynopsis reads a docs page the way a reader does: one shape per
// line, an indented line continues the shape above it, and two or more spaces
// separate a shape from its description.
func parseDocsSynopsis(t *testing.T, command string) []usageLine {
	t.Helper()
	path := filepath.Join("..", "..", "docs", "cli", command+".md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	m := docsSynopsisBlock.FindSubmatch(body)
	if m == nil {
		t.Fatalf("%s has no ```text synopsis block", path)
	}
	gap := regexp.MustCompile(`\s{2,}`)
	var out []usageLine
	for _, raw := range strings.Split(strings.TrimRight(string(m[1]), "\n"), "\n") {
		if strings.TrimSpace(raw) == "" {
			continue
		}
		if strings.HasPrefix(raw, "    ") && len(out) > 0 {
			last := &out[len(out)-1]
			last.Cont = append(last.Cont, strings.TrimSpace(raw))
			continue
		}
		parts := gap.Split(strings.TrimSpace(raw), 2)
		line := usageLine{Shape: strings.TrimSpace(parts[0])}
		if len(parts) > 1 {
			line.Desc = strings.TrimSpace(parts[1])
		}
		out = append(out, line)
	}
	return out
}

// TestUsageMatchesTheDocsSynopsis is what makes commandUsage safe to keep as a
// second copy of the invocation shapes.
//
// The CLI cannot read docs/ at runtime, so the shapes have to be compiled in.
// Two copies of the same fact drift — that is how the help got into the state
// this fixes. Pinning them means the docs page stays the one place to edit a
// shape, and changing it without updating help fails here rather than shipping
// help that describes a command that no longer exists.
func TestUsageMatchesTheDocsSynopsis(t *testing.T) {
	for command, got := range commandUsage {
		t.Run(command, func(t *testing.T) {
			want := parseDocsSynopsis(t, command)
			if len(got) != len(want) {
				t.Fatalf("%s: help has %d shapes, docs/cli/%s.md has %d",
					command, len(got), command, len(want))
			}
			for i := range want {
				if got[i].Shape != want[i].Shape {
					t.Errorf("shape %d:\n help: %s\n docs: %s", i, got[i].Shape, want[i].Shape)
				}
				if got[i].Desc != want[i].Desc {
					t.Errorf("shape %d description:\n help: %q\n docs: %q", i, got[i].Desc, want[i].Desc)
				}
				if strings.Join(got[i].Cont, "|") != strings.Join(want[i].Cont, "|") {
					t.Errorf("shape %d continuations:\n help: %v\n docs: %v", i, got[i].Cont, want[i].Cont)
				}
			}
		})
	}
}

// TestEveryDocumentedCommandHasUsage catches the other direction: a command
// added to the registry with a docs page but no entry here would silently fall
// back to help with no usage line, which is the defect being fixed.
func TestEveryDocumentedCommandHasUsage(t *testing.T) {
	for _, spec := range commandRegistry {
		if spec.NoDocs || spec.Hidden {
			continue
		}
		if _, ok := commandUsage[spec.Name]; !ok {
			t.Errorf("%s has no usage shapes; add them from docs/cli/%s.md", spec.Name, spec.Name)
		}
	}
}

// TestSubcommandUsageIsFiltered keeps a group's help from dumping every sibling
// shape at someone who asked about one subcommand. `model list --help` showing
// all of model's shapes is barely better than the single-line usage it replaced.
func TestSubcommandUsageIsFiltered(t *testing.T) {
	got := usageLinesFor("model list", "model")
	if len(got) != 1 {
		t.Fatalf("model list matched %d shapes, want 1: %+v", len(got), got)
	}
	if !strings.HasPrefix(got[0].Shape, "microagent model list") {
		t.Errorf("matched the wrong shape: %s", got[0].Shape)
	}
}

// TestUnknownSubcommandFallsBackToTheGroup covers the case the filter cannot
// satisfy. Showing the group's shapes is how a reader finds the subcommand they
// meant; showing nothing is not.
func TestUnknownSubcommandFallsBackToTheGroup(t *testing.T) {
	got := usageLinesFor("model nonesuch", "model")
	if len(got) != len(commandUsage["model"]) {
		t.Errorf("got %d shapes, want the group's full %d", len(got), len(commandUsage["model"]))
	}
}

// TestUsageFieldsAreActuallySeparate is the lint the kernel entries needed
// before they shipped. The pinning test above proves help matches the docs —
// it cannot prove either is well-formed, and the kernel page was once
// realigned to FIT the extraction parser instead of the parser being fixed:
// flags plus eighty spaces of docs padding landed inside Desc, the pinning
// test stayed green, and both `kernel --help` and the public docs page
// rendered broken. Shape, continuation, and description being separate
// FIELDS is the design; this asserts they hold separate CONTENT.
func TestUsageFieldsAreActuallySeparate(t *testing.T) {
	for command, lines := range commandUsage {
		for i, l := range lines {
			if strings.Contains(l.Shape, "  ") {
				t.Errorf("%s shape %d contains a column gap; padding belongs to the renderer: %q", command, i, l.Shape)
			}
			if strings.Contains(l.Desc, "  ") {
				t.Errorf("%s desc %d contains embedded padding — a mis-split synopsis: %q", command, i, l.Desc)
			}
			if strings.HasPrefix(l.Desc, "[") || strings.HasPrefix(l.Desc, "-") {
				t.Errorf("%s desc %d starts with flag syntax — flags belong in the shape: %q", command, i, l.Desc)
			}
			for j, c := range l.Cont {
				if strings.Contains(c, "  ") {
					t.Errorf("%s continuation %d.%d contains a column gap: %q", command, i, j, c)
				}
			}
		}
	}
}

// docsLead extracts the first paragraph after the synopsis fence, the way the
// description generator did — links flattened to their text, whitespace
// collapsed.
func docsLead(t *testing.T, command string) string {
	t.Helper()
	body, err := os.ReadFile(filepath.Join("..", "..", "docs", "cli", command+".md"))
	if err != nil {
		t.Fatalf("read docs page: %v", err)
	}
	m := regexp.MustCompile("(?s)```text\n.*?```\n+(.*?)(\n\n|\n#)").FindSubmatch(body)
	if m == nil {
		t.Fatalf("docs/cli/%s.md has no lead paragraph after its synopsis", command)
	}
	para := string(m[1])
	para = regexp.MustCompile(`\[([^\]]+)\]\([^)]*\)`).ReplaceAllString(para, "$1")
	return strings.Join(strings.Fields(para), " ")
}

// TestDescriptionMatchesTheDocsLead is commandUsage's contract applied to the
// prose: the docs page stays the one place to edit a command's description,
// and editing it without regenerating help fails here instead of shipping
// help that describes a command the docs no longer do.
func TestDescriptionMatchesTheDocsLead(t *testing.T) {
	for command, got := range commandDescription {
		t.Run(command, func(t *testing.T) {
			if want := docsLead(t, command); got != want {
				t.Errorf("description drifted from the docs lead:\n help: %s\n docs: %s", got, want)
			}
		})
	}
}

// TestEveryDocumentedCommandHasADescription mirrors the usage-side guard: a
// documented command missing from the map silently reverts to prose-less help,
// which is the defect being fixed.
func TestEveryDocumentedCommandHasADescription(t *testing.T) {
	for _, spec := range commandRegistry {
		if spec.NoDocs || spec.Hidden {
			continue
		}
		if commandDescription[spec.Name] == "" {
			t.Errorf("%s has no description; regenerate from docs/cli/%s.md", spec.Name, spec.Name)
		}
	}
}
