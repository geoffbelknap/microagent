//go:build linux

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"

	firecrackersupervisor "github.com/geoffbelknap/microagent-kit/pkg/supervisors/firecracker"
	"github.com/geoffbelknap/microagent-kit/pkg/vmkit"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdout *os.File) error {
	req, err := readRequest(args)
	if err != nil {
		resp := vmkit.Response{OK: false, Backend: vmkit.BackendFirecracker, Error: err.Error()}
		_ = writeResponse(stdout, resp)
		return err
	}
	resp, err := firecrackersupervisor.Supervisor{}.Do(ctx, req)
	if writeErr := writeResponse(stdout, resp); writeErr != nil && err == nil {
		return writeErr
	}
	return err
}

func readRequest(args []string) (vmkit.Request, error) {
	switch {
	case len(args) == 2 && args[0] == "--request":
		data, err := os.ReadFile(args[1])
		if err != nil {
			return vmkit.Request{}, err
		}
		return decodeRequest(data)
	case len(args) == 2 && args[0] == "--request-json":
		return decodeRequest([]byte(args[1]))
	case len(args) == 0:
		data, err := os.ReadFile("/dev/stdin")
		if err != nil {
			return vmkit.Request{}, err
		}
		if len(bytes.TrimSpace(data)) == 0 {
			return vmkit.Request{}, fmt.Errorf("request JSON is required on stdin or with --request")
		}
		return decodeRequest(data)
	default:
		return vmkit.Request{}, fmt.Errorf("usage: microagent-firecracker-supervisor [--request <path>|--request-json <json>]")
	}
}

func decodeRequest(data []byte) (vmkit.Request, error) {
	var req vmkit.Request
	if err := json.Unmarshal(data, &req); err != nil {
		return vmkit.Request{}, err
	}
	return req, nil
}

func writeResponse(stdout *os.File, resp vmkit.Response) error {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(resp)
}
