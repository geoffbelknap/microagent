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

func outputModeFromContext(ctx context.Context) outputMode {
	if mode, ok := ctx.Value(outputModeContextKey{}).(outputMode); ok && mode != "" {
		return mode
	}
	return currentOutputMode()
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
