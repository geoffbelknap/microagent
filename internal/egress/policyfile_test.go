package egress

import (
	"os"
	"path/filepath"
	"testing"
)

func writePolicyFile(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestLoadPolicyFileYAML(t *testing.T) {
	path := writePolicyFile(t, "policy.yaml", `
allow:
  - api.github.com
  - .example.com
passthrough:
  - raw.example.com
`)
	pf, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatalf("LoadPolicyFile: %v", err)
	}
	if len(pf.Allow) != 2 || pf.Allow[0] != "api.github.com" || pf.Allow[1] != ".example.com" {
		t.Fatalf("Allow = %v", pf.Allow)
	}
	if len(pf.Passthrough) != 1 || pf.Passthrough[0] != "raw.example.com" {
		t.Fatalf("Passthrough = %v", pf.Passthrough)
	}
}

func TestLoadPolicyFileYMLExtension(t *testing.T) {
	path := writePolicyFile(t, "policy.yml", "allow: [a.com]\n")
	pf, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatalf("LoadPolicyFile: %v", err)
	}
	if len(pf.Allow) != 1 || pf.Allow[0] != "a.com" {
		t.Fatalf("Allow = %v", pf.Allow)
	}
}

func TestLoadPolicyFileJSON(t *testing.T) {
	path := writePolicyFile(t, "policy.json", `{"allow":["api.github.com"],"passthrough":["raw.example.com"]}`)
	pf, err := LoadPolicyFile(path)
	if err != nil {
		t.Fatalf("LoadPolicyFile: %v", err)
	}
	if len(pf.Allow) != 1 || pf.Allow[0] != "api.github.com" {
		t.Fatalf("Allow = %v", pf.Allow)
	}
	if len(pf.Passthrough) != 1 || pf.Passthrough[0] != "raw.example.com" {
		t.Fatalf("Passthrough = %v", pf.Passthrough)
	}
}

func TestLoadPolicyFileRejectsUnknownKeyYAML(t *testing.T) {
	// A typo (allowed vs allow) must be rejected, not silently ignored —
	// silently dropping it would leave the operator believing a host is
	// reachable when default-deny still blocks it.
	path := writePolicyFile(t, "policy.yaml", "allowed:\n  - api.github.com\n")
	if _, err := LoadPolicyFile(path); err == nil {
		t.Fatal("expected error for unknown top-level key in YAML")
	}
}

func TestLoadPolicyFileRejectsUnknownKeyJSON(t *testing.T) {
	path := writePolicyFile(t, "policy.json", `{"allowed":["api.github.com"]}`)
	if _, err := LoadPolicyFile(path); err == nil {
		t.Fatal("expected error for unknown top-level key in JSON")
	}
}

func TestLoadPolicyFileRejectsEmptyEntry(t *testing.T) {
	yamlPath := writePolicyFile(t, "policy.yaml", "allow:\n  - api.github.com\n  - \"  \"\n")
	if _, err := LoadPolicyFile(yamlPath); err == nil {
		t.Fatal("expected error for empty/whitespace allow entry")
	}
	jsonPath := writePolicyFile(t, "policy.json", `{"passthrough":["raw.example.com",""]}`)
	if _, err := LoadPolicyFile(jsonPath); err == nil {
		t.Fatal("expected error for empty passthrough entry")
	}
}

func TestLoadPolicyFileRejectsUnknownExtension(t *testing.T) {
	path := writePolicyFile(t, "policy.txt", "allow: [a.com]\n")
	if _, err := LoadPolicyFile(path); err == nil {
		t.Fatal("expected error for unsupported extension")
	}
}

func TestLoadPolicyFileRequiresPath(t *testing.T) {
	if _, err := LoadPolicyFile("   "); err == nil {
		t.Fatal("expected error for empty path")
	}
}
