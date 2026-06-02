//go:build linux

package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func decodeAPIResponse(t *testing.T, line string) (bool, string, string) {
	t.Helper()
	var resp struct {
		OK    bool   `json:"ok"`
		Value string `json:"value"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("response not JSON: %v (%q)", err, line)
	}
	return resp.OK, resp.Value, resp.Error
}

func TestAnswerWorkloadRequestOK(t *testing.T) {
	fetch := func(name string) ([]byte, error) {
		if name != "DB" {
			t.Fatalf("name = %q", name)
		}
		return []byte("sekret"), nil
	}
	line := answerWorkloadRequest("GET DB\n", fetch)
	ok, valueB64, _ := decodeAPIResponse(t, line)
	if !ok {
		t.Fatal("expected ok")
	}
	decoded, _ := base64.StdEncoding.DecodeString(valueB64)
	if string(decoded) != "sekret" {
		t.Fatalf("decoded value = %q", decoded)
	}
}

func TestAnswerWorkloadRequestError(t *testing.T) {
	fetch := func(string) ([]byte, error) { return nil, errors.New("denied") }
	line := answerWorkloadRequest("GET NOPE\n", fetch)
	ok, _, errMsg := decodeAPIResponse(t, line)
	if ok || errMsg == "" {
		t.Fatalf("expected error response, got ok=%v err=%q", ok, errMsg)
	}
}

func TestAnswerWorkloadRequestMalformed(t *testing.T) {
	called := false
	fetch := func(string) ([]byte, error) { called = true; return nil, nil }
	line := answerWorkloadRequest("HELLO\n", fetch)
	ok, _, errMsg := decodeAPIResponse(t, line)
	if ok || errMsg == "" {
		t.Fatalf("expected error for malformed request, got ok=%v", ok)
	}
	if called {
		t.Fatal("fetch must not be called for a malformed request")
	}
	if !strings.Contains(errMsg, "GET") {
		t.Fatalf("error should hint the GET form: %q", errMsg)
	}
}
