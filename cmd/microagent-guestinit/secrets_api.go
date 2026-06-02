//go:build linux

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
)

const secretsAPISock = "/run/secrets-api.sock"
const secretsAPIConnectTimeout = 30 * time.Second

type workloadResponse struct {
	OK    bool   `json:"ok"`
	Value []byte `json:"value,omitempty"`
	Error string `json:"error,omitempty"`
}

// answerWorkloadRequest parses one workload request line ("GET <name>") and
// returns the JSON response line. fetch resolves a name to a value (it dials the
// host in production). Pure, so it is unit-testable.
func answerWorkloadRequest(reqLine string, fetch func(name string) ([]byte, error)) string {
	fields := strings.Fields(strings.TrimSpace(reqLine))
	if len(fields) != 2 || fields[0] != "GET" {
		return marshalWorkloadResponse(workloadResponse{Error: "malformed request; expected: GET <name>"})
	}
	value, err := fetch(fields[1])
	if err != nil {
		return marshalWorkloadResponse(workloadResponse{Error: err.Error()})
	}
	return marshalWorkloadResponse(workloadResponse{OK: true, Value: value})
}

func marshalWorkloadResponse(resp workloadResponse) string {
	data, err := json.Marshal(resp)
	if err != nil {
		return `{"ok":false,"error":"internal marshal error"}`
	}
	return string(data)
}

// fetchFromHost dials the host secrets vsock port and fetches one secret.
func fetchFromHost(hostPort uint16, name string) ([]byte, error) {
	fd, err := dialHostVsock(uint32(hostPort), secretsAPIConnectTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect host secrets port: %w", err)
	}
	conn := os.NewFile(uintptr(fd), "secrets-api-vsock")
	defer conn.Close()
	return secretxfer.FetchOne(conn, name)
}

// serveSecretsAPI listens on the workload-facing UNIX socket and answers GET
// requests by proxying to the host. It starts the accept loop in a goroutine
// and returns once the socket is ready.
func serveSecretsAPI(sockPath string, hostPort uint16) error {
	if err := os.Remove(sockPath); err != nil && !os.IsNotExist(err) {
		return err
	}
	listener, err := net.Listen("unix", sockPath)
	if err != nil {
		return fmt.Errorf("listen %s: %w", sockPath, err)
	}
	if err := os.Chmod(sockPath, 0o600); err != nil {
		_ = listener.Close()
		return fmt.Errorf("chmod %s: %w", sockPath, err)
	}
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleWorkloadConn(conn, hostPort)
		}
	}()
	return nil
}

func handleWorkloadConn(conn net.Conn, hostPort uint16) {
	defer conn.Close()
	reqLine, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil && reqLine == "" {
		return
	}
	resp := answerWorkloadRequest(reqLine, func(name string) ([]byte, error) {
		return fetchFromHost(hostPort, name)
	})
	_, _ = conn.Write([]byte(resp + "\n"))
}
