package main

import (
	"context"
	"os"
	"strings"
)

type outputMode string

const (
	outputModeUX outputMode = "ux"
	outputModeAX outputMode = "ax"
)

type outputModeContextKey struct{}

func contextWithOutputMode(ctx context.Context, mode outputMode) context.Context {
	return context.WithValue(ctx, outputModeContextKey{}, mode)
}

func currentOutputMode() outputMode {
	if globalOutputMode != "" {
		return globalOutputMode
	}
	return outputModeFromEnv()
}

func outputModeFromEnv() outputMode {
	return normalizeOutputMode(os.Getenv("MICROAGENT_MODE"))
}

func normalizeOutputMode(value string) outputMode {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "ux", "human", "text":
		return outputModeUX
	case "ax", "agent", "json":
		return outputModeAX
	default:
		return outputModeUX
	}
}

// isRecognizedOutputModeValue reports whether value names one of the modes
// normalizeOutputMode understands explicitly. normalizeOutputMode's return
// value alone can't be used for this: it falls back to outputModeUX for any
// unrecognized input, the same value an explicit "ux"/"human"/"text" input
// produces, so callers that need to tell "recognized mode" apart from
// "unknown value that happened to fall back" — such as parseGlobalFlags
// deciding whether a "--mode" token is really the global output mode flag —
// must check against the explicit case list here instead.
func isRecognizedOutputModeValue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "ux", "human", "text", "ax", "agent", "json":
		return true
	default:
		return false
	}
}
