package main

import (
	"context"
	"time"

	"github.com/geoffbelknap/microagent/pkg/operation"
)

func runMCPTool(ctx context.Context, name string, args map[string]any) (map[string]any, error) {
	start := time.Now()
	if preview := previewDestructiveMCPTool(name, args); preview != nil {
		return preview, nil
	}
	if preview, err := requireConfirmedMCPHostMutation(name, args); preview != nil || err != nil {
		return preview, err
	}
	if mcpIdempotencyCacheKey(name, args) != "" {
		envelope, err, replay := mcpIdempotencyCache.Do(ctx, name, args, func() (map[string]any, error) {
			return runMCPToolOnce(ctx, name, args, start)
		})
		if envelope == nil {
			envelope = mcpErrorEnvelope(mcpStructuredErrorFor(err), mcpMeta(args, start))
		}
		return mcpMarkReplayForArgs(envelope, replay, args), err
	}
	return runMCPToolOnce(ctx, name, args, start)
}

func runMCPToolOnce(ctx context.Context, name string, args map[string]any, start time.Time) (map[string]any, error) {
	if name == "workspace.exec" {
		return runMCPWorkspaceExec(ctx, args, start)
	}
	if name == "workspace.start" {
		result, err := runMCPWorkspaceStart(ctx, args)
		meta := mcpMeta(args, start)
		if err != nil {
			return mcpErrorEnvelope(mcpStructuredErrorFor(err), meta), err
		}
		return mcpSuccessEnvelope(result, meta), nil
	}
	if name == "workspace.dispatch" {
		result, err := runMCPWorkspaceDispatch(ctx, args)
		meta := mcpMeta(args, start)
		if err != nil {
			return mcpErrorEnvelope(mcpStructuredErrorFor(err), meta), err
		}
		return mcpSuccessEnvelope(result, meta), nil
	}
	if name == "workspace.create" {
		result, err := runMCPWorkspaceCreate(ctx, args)
		meta := mcpMeta(args, start)
		if err != nil {
			return mcpErrorEnvelope(mcpStructuredErrorFor(err), meta), err
		}
		return mcpSuccessEnvelope(result, meta), nil
	}
	if result, handled, directErr := runDirectMCPTool(ctx, name, args); handled {
		meta := mcpMeta(args, start)
		var envelope map[string]any
		if directErr != nil {
			envelope = mcpErrorEnvelope(mcpStructuredErrorFor(directErr), meta)
			if name == "workspace.quarantine" && result != nil {
				envelope["partial_result"] = result
			}
		} else {
			envelope = mcpSuccessEnvelope(result, meta)
		}
		return envelope, directErr
	}
	err := operation.New(operation.ErrorUnsupported, "unsupported MCP tool %s", name)
	meta := mcpMeta(args, start)
	return mcpErrorEnvelope(mcpStructuredErrorFor(err), meta), err
}
