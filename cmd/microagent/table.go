package main

import (
	"fmt"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/term"
)

// tableColumn declares one column of a human-readable list table.
//
// Legacy is the fixed width used for non-TTY output. Encoding the
// pre-redesign widths here (rather than deriving them) is what keeps piped
// output byte-identical to the original fmt.Fprintf("%-Ns ...") calls it
// replaces — the awk/cut compatibility bar. The column that was
// positionally last in the original format string carries Legacy 0: the
// original code never padded the final field (a bare "%s"), and this
// renderer preserves that by never right-padding whichever column is last,
// in either non-TTY or TTY mode.
//
// Min, Max, and Flex apply only to TTY rendering. Flex marks the column
// (NAME/IMAGE/TAG — the row's primary identifier) that absorbs whatever
// terminal width remains after every other column has been sized to its
// content (clamped to its own Min/Max) and the inter-column spaces are
// subtracted. Non-flex columns size to the widest of their header or cell
// content, clamped to [Min, Max]; Max == 0 means "no cap besides content".
type tableColumn struct {
	Header string
	Legacy int
	Min    int
	Max    int
	Flex   bool
}

// tableCell is one row value. Colorize, when set, is applied to the
// (possibly truncated) raw text after all width math, so ANSI escape bytes
// never enter padding or truncation calculations and can never be cut off
// mid-escape-sequence. Header cells are never colorized.
type tableCell struct {
	Text     string
	Colorize func(string) string
}

// cell builds a plain, uncolored table cell.
func cell(text string) tableCell { return tableCell{Text: text} }

// terminalWidth reports stdout's current width and whether stdout is a TTY.
// This is the sole seam for golang.org/x/term.GetSize; tests override this
// var to inject a fake width without needing a real terminal.
var terminalWidth = func(stdout *os.File) (width int, isTTY bool) {
	if !fileIsTerminal(stdout) {
		return 0, false
	}
	w, _, err := term.GetSize(int(stdout.Fd()))
	if err != nil || w <= 0 {
		return 0, false
	}
	return w, true
}

const ellipsisRune = "…"

// truncateCell shortens s to at most width runes, replacing the last rune
// with an ellipsis when it must cut. It operates on raw (uncolored) text
// only; callers colorize after truncating.
func truncateCell(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	r := []rune(s)
	if width == 1 {
		return ellipsisRune
	}
	return string(r[:width-1]) + ellipsisRune
}

// renderTable writes cols/rows to stdout. Non-TTY output uses each column's
// declared Legacy width, reproducing the exact byte layout of the
// fmt.Fprintf calls it replaces (only the digest column's shortened content
// changes what those bytes hold, never the column widths). TTY output
// measures the real terminal width, autosizes non-flex columns to content
// within [Min, Max], and lets the Flex column absorb the remainder,
// truncating over-long values with an ellipsis.
func renderTable(stdout *os.File, cols []tableColumn, rows [][]tableCell) {
	width, isTTY := terminalWidth(stdout)
	if !isTTY {
		renderLegacyTable(stdout, cols, rows)
		return
	}
	renderTTYTable(stdout, cols, rows, width)
}

func headerRow(cols []tableColumn) []tableCell {
	row := make([]tableCell, len(cols))
	for i, c := range cols {
		row[i] = cell(c.Header)
	}
	return row
}

func renderLegacyTable(stdout *os.File, cols []tableColumn, rows [][]tableCell) {
	writeLegacyRow(stdout, cols, headerRow(cols))
	for _, row := range rows {
		writeLegacyRow(stdout, cols, row)
	}
}

func writeLegacyRow(stdout *os.File, cols []tableColumn, row []tableCell) {
	n := len(cols)
	parts := make([]string, n)
	for i, c := range cols {
		raw := row[i].Text
		display := raw
		if row[i].Colorize != nil {
			display = row[i].Colorize(raw)
		}
		if i == n-1 {
			parts[i] = display
			continue
		}
		pad := c.Legacy - utf8.RuneCountInString(raw)
		if pad < 0 {
			pad = 0
		}
		parts[i] = display + strings.Repeat(" ", pad)
	}
	fmt.Fprintln(stdout, strings.Join(parts, " "))
}

func renderTTYTable(stdout *os.File, cols []tableColumn, rows [][]tableCell, width int) {
	n := len(cols)
	colWidth := make([]int, n)
	flexIdx := -1
	fixedTotal := 0
	for i, c := range cols {
		if c.Flex {
			flexIdx = i
			continue
		}
		w := utf8.RuneCountInString(c.Header)
		for _, row := range rows {
			if l := utf8.RuneCountInString(row[i].Text); l > w {
				w = l
			}
		}
		if c.Min > 0 && w < c.Min {
			w = c.Min
		}
		if c.Max > 0 && w > c.Max {
			w = c.Max
		}
		colWidth[i] = w
		fixedTotal += w
	}
	if flexIdx >= 0 {
		fc := cols[flexIdx]
		avail := width - fixedTotal - (n - 1) // one separating space per gap
		if fc.Min > 0 && avail < fc.Min {
			avail = fc.Min
		}
		if fc.Max > 0 && avail > fc.Max {
			avail = fc.Max
		}
		if avail < 0 {
			avail = 0
		}
		colWidth[flexIdx] = avail
	}

	writeTTYRow(stdout, cols, headerRow(cols), colWidth)
	for _, row := range rows {
		writeTTYRow(stdout, cols, row, colWidth)
	}
}

func writeTTYRow(stdout *os.File, cols []tableColumn, row []tableCell, colWidth []int) {
	n := len(cols)
	parts := make([]string, n)
	for i := range cols {
		w := colWidth[i]
		truncated := truncateCell(row[i].Text, w)
		display := truncated
		if row[i].Colorize != nil {
			display = row[i].Colorize(truncated)
		}
		if i == n-1 {
			parts[i] = display
			continue
		}
		pad := w - utf8.RuneCountInString(truncated)
		if pad < 0 {
			pad = 0
		}
		parts[i] = display + strings.Repeat(" ", pad)
	}
	fmt.Fprintln(stdout, strings.Join(parts, " "))
}
