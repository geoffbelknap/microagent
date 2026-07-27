package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
)

// newCommandFlagSet builds a FlagSet whose failure output is owned by
// parseCommandFlags instead of the flag package's raw usage dump.
func newCommandFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {}
	return fs
}

// parseCommandFlags parses args, turning flag errors into one actionable
// line and -h/--help into generated command help on stdout.
func parseCommandFlags(fs *flag.FlagSet, stdout *os.File, args []string) error {
	err := fs.Parse(args)
	if err == nil {
		return nil
	}
	if errors.Is(err, flag.ErrHelp) {
		printGeneratedCommandHelp(stdout, fs)
		return flag.ErrHelp
	}
	msg := fmt.Sprintf("%v\nRun 'microagent %s --help' for usage", err, strings.Fields(fs.Name())[0])
	// Go's flag package reports an unrecognized flag as exactly "flag provided
	// but not defined: -<name>", so matching the suffix "not defined: -json"
	// only fires for the flag literally named "json" - not "-jsonfile" or any
	// other flag that merely starts with "json".
	if strings.HasSuffix(err.Error(), "not defined: -json") {
		msg += "\nnote: post-command --json is the global output flag; use --request-json <path> for request files (see MIGRATION.md)"
	}
	return errors.New(msg)
}

func printGeneratedCommandHelp(w io.Writer, fs *flag.FlagSet) {
	top := strings.Fields(fs.Name())[0]
	if spec, ok := lookupCommand(top); ok {
		fmt.Fprintf(w, "microagent %s — %s\n", fs.Name(), spec.Summary)
	} else {
		fmt.Fprintf(w, "microagent %s\n", fs.Name())
	}
	printUsageBlock(w, fs.Name(), top)
	fmt.Fprint(w, "\nOptions:\n")
	for _, opt := range collapsedFlags(fs) {
		fmt.Fprintf(w, "  %-20s %s\n", opt.label, opt.usage)
	}
}

// printUsageBlock writes the invocation shapes for a command, saying what it
// takes rather than only which flags it accepts.
//
// A subcommand flagset is named for the full path ("model list"), so a group
// command shows only the shapes that match what was asked for — `model list
// --help` gets one line, not all fourteen of model's.
func printUsageBlock(w io.Writer, name, top string) {
	lines := usageLinesFor(name, top)
	if len(lines) == 0 {
		return
	}
	width := 0
	for _, l := range lines {
		if l.Desc != "" && len(l.Shape) > width {
			width = len(l.Shape)
		}
	}
	fmt.Fprint(w, "\nUsage:\n")
	for _, l := range lines {
		if l.Desc == "" {
			fmt.Fprintf(w, "  %s\n", l.Shape)
		} else {
			fmt.Fprintf(w, "  %-*s   %s\n", width, l.Shape, l.Desc)
		}
		// Continuations hang under the shape's own flag list, not under
		// "microagent", so a wrapped shape still reads as one invocation.
		indent := 2 + len(firstWord(l.Shape)) + 1
		for _, c := range l.Cont {
			fmt.Fprintf(w, "%s%s\n", strings.Repeat(" ", indent), c)
		}
	}
}

func firstWord(s string) string {
	if i := strings.IndexByte(s, ' '); i >= 0 {
		return s[:i]
	}
	return s
}

func usageLinesFor(name, top string) []usageLine {
	all := commandUsage[top]
	if name == top {
		return all
	}
	prefix := "microagent " + name
	matched := make([]usageLine, 0, len(all))
	for _, l := range all {
		if l.Shape == prefix || strings.HasPrefix(l.Shape, prefix+" ") {
			matched = append(matched, l)
		}
	}
	// A subcommand with no shape of its own is better served by the group's
	// full list than by no usage at all.
	if len(matched) == 0 {
		return all
	}
	return matched
}

// flagOption is one option as a reader sees it: every spelling that sets the
// same thing, on one line.
type flagOption struct {
	label string // "--force, -f"
	usage string
	sort  string // the long name, for stable ordering
}

// flagLabel spells a flag the way its length implies: one dash for a
// single-letter flag, two otherwise. The generator used to hardcode two, so an
// alias like -f rendered as the nonexistent "--f".
func flagLabel(name string) string {
	if len(name) == 1 {
		return "-" + name
	}
	return "--" + name
}

// collapsedFlags groups the flagset's options by the variable they set, so a
// flag registered under several names appears once with all of its spellings.
// Aliases are registered as separate flag.Flags bound to the same variable
// (fs.BoolVar(&force, "force", ...) then fs.BoolVar(&force, "f", ...)), which
// made a plain VisitAll list each spelling as if it were its own option —
// `delete --help` showed --f and --force, and --y and --yes, as four entries.
//
// Grouping is by bound-variable identity rather than by matching usage text: two
// unrelated flags could describe themselves the same way, and merging those
// would claim a spelling that does not exist.
//
// The long name leads. That is the canonical spelling, and it also keeps the
// docs-parity gate working, since it reads the first flag on each help line and
// the CLI pages document long names.
func collapsedFlags(fs *flag.FlagSet) []flagOption {
	byVar := map[uintptr][]string{}
	var order []uintptr
	usageByName := map[string]string{}

	fs.VisitAll(func(f *flag.Flag) {
		if strings.Contains(f.Usage, "(internal") {
			return // internal plumbing flags stay out of user help
		}
		key := boundVariable(f)
		if _, seen := byVar[key]; !seen {
			order = append(order, key)
		}
		byVar[key] = append(byVar[key], f.Name)
		usageByName[f.Name] = f.Usage
	})

	opts := make([]flagOption, 0, len(order))
	for _, key := range order {
		names := byVar[key]
		// Longest first: the canonical spelling leads, short aliases follow.
		sort.SliceStable(names, func(i, j int) bool { return len(names[i]) > len(names[j]) })
		labels := make([]string, 0, len(names))
		for _, n := range names {
			labels = append(labels, flagLabel(n))
		}
		// Describe the option the way its canonical spelling does. Taking the
		// first-visited flag's text instead showed --id's "Workspace ID" for the
		// pair rendered "--name, --id", because VisitAll is alphabetical.
		opts = append(opts, flagOption{
			label: strings.Join(labels, ", "),
			usage: usageByName[names[0]],
			sort:  names[0],
		})
	}
	sort.SliceStable(opts, func(i, j int) bool { return opts[i].sort < opts[j].sort })
	return opts
}

// boundVariable identifies the variable a flag writes to. Two spellings of the
// same option share it; distinct options do not. Values that are not pointers
// (a custom flag.Value holding state by value) fall back to a per-flag key, so
// they are never merged with anything.
func boundVariable(f *flag.Flag) uintptr {
	v := reflect.ValueOf(f.Value)
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		return v.Pointer()
	}
	return reflect.ValueOf(f).Pointer()
}

// printGroupHelpHeader writes the title line for a group command's help using
// the registry's own summary, so the summary is not restated (and left to
// drift) at each print site.
func printGroupHelpHeader(w io.Writer, command string) {
	if spec, ok := lookupCommand(command); ok && spec.Summary != "" {
		fmt.Fprintf(w, "microagent %s — %s\n", command, spec.Summary)
		return
	}
	fmt.Fprintf(w, "microagent %s\n", command)
}
