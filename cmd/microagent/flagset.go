package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
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
		fmt.Fprintf(w, "microagent %s — %s\n\nOptions:\n", fs.Name(), spec.Summary)
	} else {
		fmt.Fprintf(w, "microagent %s\n\nOptions:\n", fs.Name())
	}
	fs.VisitAll(func(f *flag.Flag) {
		if strings.Contains(f.Usage, "(internal") {
			return // internal plumbing flags stay out of user help
		}
		fmt.Fprintf(w, "  --%-18s %s\n", f.Name, f.Usage)
	})
}
