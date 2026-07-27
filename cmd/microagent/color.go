package main

import (
	"os"
)

// ANSI wraps for state words. Color is a redundant channel only: the word
// itself never changes, only whether it is additionally wrapped here.
const (
	ansiRed    = "\033[31m"
	ansiGreen  = "\033[32m"
	ansiYellow = "\033[33m"
	ansiReset  = "\033[0m"
)

// stateColor maps unambiguous state/status words to their ANSI color.
// Neutral lifecycle states (prepared, starting, halted, stopped) are left
// out on purpose: parked is not bad.
var stateColor = map[string]string{
	"failed":      ansiRed,
	"running":     ansiGreen,
	"ready":       ansiGreen,
	"ok":          ansiGreen,
	"PASS":        ansiGreen,
	"WARN":        ansiYellow,
	"degraded":    ansiYellow,
	"quarantined": ansiYellow,
	"paused":      ansiYellow,
}

// colorEnabled reports whether human output may use color: stdout must be a
// TTY, NO_COLOR must be unset, and --no-color must not have been passed.
func colorEnabled(stdout *os.File) bool {
	_, noColorSet := os.LookupEnv("NO_COLOR")
	return fileIsTerminal(stdout) && !noColorSet && !noColorFlag
}

// colorizeState wraps word in its mapped ANSI color when color is enabled
// and the word has an unambiguous mapping; otherwise it returns word as-is.
func colorizeState(stdout *os.File, word string) string {
	color, ok := stateColor[word]
	if !ok || !colorEnabled(stdout) {
		return word
	}
	return color + word + ansiReset
}
