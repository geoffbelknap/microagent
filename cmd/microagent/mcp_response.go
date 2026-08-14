package main

import (
	"encoding/json"
	"time"
)

// structuredErrorMap renders a structuredError as a plain JSON object so the
// MCP layer can attach a sibling `meta` transport block to it inside a
// JSON-RPC error.data payload.
func structuredErrorMap(e structuredError) map[string]any {
	data, err := json.Marshal(e)
	if err != nil {
		return map[string]any{}
	}
	out := map[string]any{}
	_ = json.Unmarshal(data, &out)
	return out
}

// mcpErrorData builds a JSON-RPC error.data payload from a raw error: it
// classifies err into a structuredError and attaches an optional sibling `meta`
// transport block. Protocol-level errors (parse, method not found, invalid
// params) pass a nil meta.
func mcpErrorData(err error, meta map[string]any) map[string]any {
	return mcpStructuredErrorData(mapStructuredError(err, newRequestID()), meta)
}

// mcpStructuredErrorData renders an already-classified structuredError plus an
// optional meta transport block as a JSON-RPC error.data payload.
func mcpStructuredErrorData(e structuredError, meta map[string]any) map[string]any {
	data := structuredErrorMap(e)
	if len(meta) > 0 {
		data["meta"] = meta
	}
	return data
}

func structuredErrorKindRetryable(kind structuredErrorKind) bool {
	switch kind {
	case errorKindTransient, errorKindResourceExhausted:
		return true
	default:
		return false
	}
}

// mcpToolCallErrorData maps a failed tool envelope ({ok:false, error, meta})
// onto a JSON-RPC error.data payload: the structuredError fields flattened at
// the top with the transport `meta` block (timing_ms, principal_context, retry
// metadata) attached as a sibling.
func mcpToolCallErrorData(err error, envelope map[string]any) any {
	var (
		structured structuredError
		haveError  bool
		meta       map[string]any
	)
	if envelope != nil {
		structured, haveError = envelope["error"].(structuredError)
		if m, ok := envelope["meta"].(map[string]any); ok {
			meta = m
		}
	}
	if !haveError {
		structured = mapStructuredError(err, newRequestID())
	}
	data := mcpStructuredErrorData(structured, meta)
	if envelope != nil && envelope["partial_result"] != nil {
		data["partial_result"] = envelope["partial_result"]
	}
	return data
}

// mcpMeta builds the transport `meta` block carried by every MCP tool envelope:
// wall-clock timing plus the caller's principal context.
func mcpMeta(args map[string]any, start time.Time) map[string]any {
	return map[string]any{
		"timing_ms":         time.Since(start).Milliseconds(),
		"principal_context": principalContextArg(args),
	}
}

// mcpZeroMeta is mcpMeta for responses produced without doing timed work
// (previews, cost estimates): timing_ms is reported as 0.
func mcpZeroMeta(args map[string]any) map[string]any {
	return map[string]any{
		"timing_ms":         int64(0),
		"principal_context": principalContextArg(args),
	}
}

// mcpSuccessEnvelope is the unified success envelope: {ok:true, result, meta}.
func mcpSuccessEnvelope(result any, meta map[string]any) map[string]any {
	return map[string]any{"ok": true, "result": result, "meta": meta}
}

// mcpErrorEnvelope is the unified failure envelope: {ok:false, error, meta}.
// The transport meta rides alongside the structuredError so both surface
// through the JSON-RPC error.data path (see mcpToolCallErrorData).
func mcpErrorEnvelope(e structuredError, meta map[string]any) map[string]any {
	return map[string]any{"ok": false, "error": e, "meta": meta}
}

// mcpMarkReplay returns a copy of envelope whose meta block records the
// idempotency replay flag, cloning the meta map so the cached original is never
// mutated (the cache stores replay-flag-free envelopes; the flag is stamped per
// response).
func mcpMarkReplay(envelope map[string]any, replay bool) map[string]any {
	out := cloneMCPMap(envelope)
	meta := map[string]any{}
	if existing, ok := out["meta"].(map[string]any); ok {
		meta = cloneMCPMap(existing)
	}
	meta["idempotency_replay"] = replay
	out["meta"] = meta
	return out
}

func mcpMarkReplayForArgs(envelope map[string]any, replay bool, args map[string]any) map[string]any {
	out := mcpMarkReplay(envelope, replay)
	meta := cloneMCPMap(out["meta"].(map[string]any))
	meta["principal_context"] = principalContextArg(args)
	out["meta"] = meta
	return out
}

// jsonCompatible gives MCP summaries their natural map/slice representation
// while keeping the operation boundary typed.
func jsonCompatible(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var result any
	if err := json.Unmarshal(data, &result); err != nil {
		return value
	}
	return result
}

// mcpStructuredErrorFor maps an operation failure into the agent-facing MCP
// error contract. MCP owns this classification; it does not inherit a CLI
// rendering profile.
func mcpStructuredErrorFor(err error) structuredError {
	return mapStructuredError(err, newRequestID())
}

func cloneMCPMap(value map[string]any) map[string]any {
	out := make(map[string]any, len(value))
	for key, item := range value {
		out[key] = item
	}
	return out
}

func mcpToolResult(value any) map[string]any {
	data, _ := json.Marshal(value)
	return map[string]any{"content": []any{map[string]any{"type": "text", "text": string(data)}}}
}
