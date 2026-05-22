// Package protocol defines the transport-agnostic wire protocol for
// microagent structured workspace exec.
//
// This package is Phase 1 of the structured exec protocol described in:
// https://www.notion.so/368bc6319c93816ea234d637c6d0034b
//
// The protocol uses snake_case JSON field names. Byte slices such as stdin,
// stdout, and stderr are encoded by encoding/json as base64 strings; callers
// should treat those fields as binary data, not UTF-8 text. Messages are framed
// with a 4-byte big-endian length prefix followed by one JSON payload.
//
// This package intentionally contains no transport, Firecracker, vsock, guest
// service, host client, CLI, or MCP integration.
package protocol
