// Package protocol defines the transport-agnostic wire protocol for
// microagent structured workspace exec.
//
// A client sends one ExecRequest naming the command argv along with optional
// env, cwd, stdin, a timeout, and per-stream output limits. In
// single_response mode the server replies with one ExecResult carrying the
// exit code or termination status, the captured stdout and stderr, and
// truncation flags. In stream mode the server instead replies with a sequence
// of ExecStreamMessage frames: zero or more stdout/stderr chunk frames as the
// command runs, then exactly one result frame carrying the final ExecResult.
//
// The protocol uses snake_case JSON field names. Byte slices such as stdin,
// stdout, and stderr are encoded by encoding/json as base64 strings; callers
// should treat those fields as binary data, not UTF-8 text. Messages are framed
// with a 4-byte big-endian length prefix followed by one JSON payload.
//
// This package intentionally contains no transport, Firecracker, vsock, guest
// service, host client, CLI, or MCP integration.
package protocol
