package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// outputStructured reports whether a command should render structured JSON.
func outputStructured() bool {
	return outputFormat == "json"
}

// outputJSON decides whether a command should render JSON or text.
// Precedence is explicit format flag, MICROAGENT_OUTPUT, then TTY detection.
func outputJSON(stdout *os.File) bool {
	switch outputFormat {
	case "json":
		return true
	case "text":
		return false
	}
	switch strings.ToLower(strings.TrimSpace(os.Getenv("MICROAGENT_OUTPUT"))) {
	case "json":
		return true
	case "text":
		return false
	}
	return !fileIsTerminal(stdout)
}

func formatBytes(value int64) string {
	const unit = 1024
	if value < unit {
		return fmt.Sprintf("%dB", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	size := float64(value)
	for _, suffix := range units {
		size /= unit
		if size < unit {
			return fmt.Sprintf("%.1f%s", size, suffix)
		}
	}
	return fmt.Sprintf("%.1fPiB", size/unit)
}

// fileIsTerminal reports whether the stream is attached to a terminal.
//
// A character-device check is not enough. /dev/null is a character device, so
// every redirection to it read as a terminal: `delete < /dev/null` prompted a
// stdin nobody was on instead of taking the fail-closed branch, `connect <
// /dev/null` failed trying to put /dev/null into raw mode, and color codes and
// table borders were written to a stream that discards them.
func fileIsTerminal(file *os.File) bool {
	return term.IsTerminal(int(file.Fd()))
}

// requestJSONAliasFamily is the set of canonical command names that used to
// accept the removed `--json <path>` request-alias (create/start and the
// lifecycle verbs backed by runLowLevelRequest). Only these commands trigger
// the --json tripwire in parseGlobalFlags.
var requestJSONAliasFamily = map[string]bool{
	"create":     true,
	"start":      true,
	"status":     true,
	"halt":       true,
	"kill":       true,
	"pause":      true,
	"resume":     true,
	"quarantine": true,
	"delete":     true,
	"result":     true,
}

// parseGlobalFlags extracts the global output flags (--json, --output,
// --mode, --no-color) wherever they appear in an ordinary command line. --text and
// --human are no longer global flags: they are left in args untouched, where
// they fail as an unrecognized flag at the command's own flagset (see
// MIGRATION.md). Use "--output text" instead.
//
// It first checks whether args is actually a special-mode re-exec line —
// "--host-worker-mediator" or "--egress-datapath" as the first token — and
// if so returns args verbatim, untouched, with no globals set. Those argvs
// are built and consumed internally (see internal/hostworker/process.go) and
// are not ordinary microagent command
// lines; walking them looking for "--mode"/"--output" would silently corrupt
// a value meant for that special mode (e.g. the mediator's own "--mode
// policy") rather than any global output flag.
//
// For everything else, extraction always stops at a literal "--". "--output
// v" / "--output=v" is only extracted when v normalizes to a known output
// format, and "--mode v" / "--mode=v" only when v names a known output mode;
// an unrecognized value leaves both the flag and its value token in args
// untouched, so a command-owned flag that happens to be spelled "--output"
// or "--mode" (e.g. create/start's own "--output name=/guest/path" artifact
// declaration) is never mistaken for the global flag. For commands that
// carry a guest payload (TrailingArgs), known workspace value flags
// (workspaceValueFlags) are skipped over together with their value token
// once past the command word, so a value like "alpine" in "--image alpine"
// is never mistaken for the guest/payload positional; this mirrors (but does
// not fully replicate) reorderArgsStopAtGuestCommand in main.go, which
// additionally distinguishes an image given as a bare positional from one
// given via --image. The first true positional after the command word
// starts guest/payload territory — nothing from there on is touched.
func parseGlobalFlags(args []string) []string {
	if len(args) > 0 {
		switch args[0] {
		case "--host-worker-mediator", "--egress-datapath":
			return args
		}
	}
	out := make([]string, 0, len(args))
	commandSeen := false
	canonicalCommand := ""
	trailing := false
	skipNextAsValue := false
	valueFlags := workspaceValueFlags()
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			out = append(out, args[i:]...)
			return out
		}
		if trailing && commandSeen {
			if skipNextAsValue {
				// Value of a preceding known workspace value flag (e.g.
				// "alpine" in "--image alpine"); keep it verbatim, it is not
				// the guest/payload positional.
				out = append(out, a)
				skipNextAsValue = false
				continue
			}
			if !strings.HasPrefix(a, "-") {
				// First true positional after the command word: guest/payload
				// territory begins here. Nothing after this point is touched.
				out = append(out, args[i:]...)
				return out
			}
		}
		switch a {
		case "--json":
			// Tripwire for the removed request-alias shape (`create --json
			// request.json`, `delete --json req.json`, `create --json -`,
			// ...): on a request-JSON-family command, a following bare
			// token ending in ".json", or the bare stdin marker "-", is
			// almost certainly the old request-file path, not a
			// workspace name/ID. Leave both tokens untouched so the
			// command's own flagset rejects "--json" as unknown and fails
			// loudly, instead of silently treating the filename (or "-")
			// as a positional workspace name. "status --json <name>" (no
			// .json suffix, not a bare "-") is the legitimate new form
			// and still extracts below.
			if commandSeen && requestJSONAliasFamily[canonicalCommand] && i+1 < len(args) {
				next := args[i+1]
				if next == "-" || (!strings.HasPrefix(next, "-") && strings.HasSuffix(strings.ToLower(next), ".json")) {
					out = append(out, a)
					continue
				}
			}
			outputFormat = "json"
		case "--output":
			if i+1 < len(args) && normalizeOutputFormat(args[i+1]) != "" {
				outputFormat = normalizeOutputFormat(args[i+1])
				i++
			} else {
				out = append(out, a)
			}
		case "--no-color":
			noColorFlag = true
		default:
			switch {
			case strings.HasPrefix(a, "--output=") && normalizeOutputFormat(strings.TrimPrefix(a, "--output=")) != "":
				outputFormat = normalizeOutputFormat(strings.TrimPrefix(a, "--output="))
			default:
				out = append(out, a)
				if !commandSeen && !strings.HasPrefix(a, "-") {
					commandSeen = true
					if spec, ok := lookupCommand(a); ok {
						trailing = spec.TrailingArgs
						canonicalCommand = spec.Name
					}
				} else if trailing && commandSeen && strings.HasPrefix(a, "-") {
					// Not a global flag (handled above) but a dash-prefixed
					// token in the trailing region. If it's a known
					// workspace value flag (and not one of the ambiguous
					// names that is also a bool flag, e.g. -json, which is
					// the global output-format alias rather than a
					// value-taking flag here), its value token must be
					// skipped too so it isn't mistaken for the guest/payload
					// positional. Unknown dash-prefixed flags are kept as-is
					// without skipping a value — conservative, since their
					// value (if any) will simply hit the positional stop.
					norm := a
					if strings.HasPrefix(norm, "--") {
						norm = "-" + strings.TrimPrefix(norm, "--")
					}
					flagName := norm
					hasInlineValue := false
					if name, _, ok := strings.Cut(norm, "="); ok {
						flagName = name
						hasInlineValue = true
					}
					if !hasInlineValue && valueFlags[flagName] && !isBoolReorderFlag(flagName) {
						skipNextAsValue = true
					}
				}
			}
		}
	}
	return out
}

func normalizeOutputFormat(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "json":
		return "json"
	case "text":
		return "text"
	default:
		return ""
	}
}
