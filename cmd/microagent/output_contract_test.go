package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// assertTextGolden owns the complete human-readable output contract in one
// fixture. Intentional changes can refresh fixtures with
// MICROAGENT_UPDATE_GOLDEN=1 go test ./cmd/microagent.
func assertTextGolden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", filepath.FromSlash(name))
	if os.Getenv("MICROAGENT_UPDATE_GOLDEN") == "1" {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatalf("update golden %s: %v", path, err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v", path, err)
	}
	if got != string(want) {
		t.Fatalf("%s changed:\n--- want ---\n%s--- got ---\n%s", path, want, got)
	}
}

// assertJSONOmits decodes structured output before checking its compact shape.
// This keeps JSON tests independent of whitespace and key ordering.
func assertJSONOmits(t *testing.T, data []byte, fields ...string) {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("decode JSON output: %v\n%s", err, data)
	}
	for _, field := range fields {
		if _, ok := raw[field]; ok {
			t.Errorf("JSON output retained %q: %s", field, data)
		}
	}
}
