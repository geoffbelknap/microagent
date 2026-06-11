package scaffold

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// TestGeneratedSpecParses asserts that every provider's generated
// microagent.yaml is accepted by the real spec parser (KnownFields strict),
// so init never emits a spec that create would reject.
func TestGeneratedSpecParses(t *testing.T) {
	for _, p := range Providers() {
		t.Run(string(p), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := Generate(Options{Name: "parsecheck", Dir: dir, Provider: p}); err != nil {
				t.Fatalf("Generate: %v", err)
			}
			spec, err := workspace.ReadSpec(filepath.Join(dir, "microagent.yaml"))
			if err != nil {
				t.Fatalf("ReadSpec rejected generated spec: %v", err)
			}
			if spec.Name != "parsecheck" {
				t.Errorf("spec.Name = %q, want parsecheck", spec.Name)
			}
			if spec.Entrypoint == "" || len(spec.Files) == 0 || len(spec.Outputs) == 0 {
				t.Errorf("generated spec missing entrypoint/files/outputs: %+v", spec)
			}
		})
	}
}

func TestGenerateDefaultProvider(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "agent")
	res, err := Generate(Options{Name: "triage", Dir: dir})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.Provider != ProviderAnthropic {
		t.Fatalf("default provider = %q, want %q", res.Provider, ProviderAnthropic)
	}
	if res.APIKey != "ANTHROPIC_API_KEY" {
		t.Fatalf("APIKey = %q, want ANTHROPIC_API_KEY", res.APIKey)
	}
	wantFiles := []string{
		"README.md",
		"agent.py",
		"demo/constraints.json",
		"demo/input-001.json",
		"demo/system_prompt.md",
		"microagent.yaml",
		"protocol.py",
	}
	if strings.Join(res.Files, ",") != strings.Join(wantFiles, ",") {
		t.Fatalf("Files = %v, want %v", res.Files, wantFiles)
	}
	for _, rel := range wantFiles {
		if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			t.Errorf("expected file %s: %v", rel, err)
		}
	}

	yaml := readFile(t, dir, "microagent.yaml")
	if !strings.Contains(yaml, "name: triage") {
		t.Errorf("microagent.yaml missing name: %s", yaml)
	}
	if !strings.Contains(yaml, "anthropic>=0.40") {
		t.Errorf("microagent.yaml missing anthropic SDK spec: %s", yaml)
	}
	// The mediation wiring is templated in (commented) so the agent's protocol
	// side has a documented host-channel counterpart to uncomment.
	if !strings.Contains(yaml, "# mediation:") {
		t.Errorf("microagent.yaml missing mediation template block: %s", yaml)
	}

	agent := readFile(t, dir, "agent.py")
	if !strings.Contains(agent, `AGENT_ID = "triage-1"`) {
		t.Errorf("agent.py AGENT_ID not templated: %s", firstLines(agent, 40))
	}
	if strings.Contains(agent, "{{") {
		t.Errorf("agent.py still contains an unrendered template directive")
	}
	if !strings.Contains(agent, "from anthropic import Anthropic") {
		t.Errorf("anthropic agent.py missing SDK import")
	}
}

func TestGenerateProviderVariants(t *testing.T) {
	cases := []struct {
		provider   Provider
		sdk        string
		apiKey     string
		agentMatch string
	}{
		{ProviderAnthropic, "anthropic>=0.40", "ANTHROPIC_API_KEY", "from anthropic import Anthropic"},
		{ProviderOpenAI, "openai>=1.50", "OPENAI_API_KEY", "from openai import OpenAI"},
		{ProviderGemini, "google-genai>=0.3", "GEMINI_API_KEY", "from google import genai"},
	}
	for _, tc := range cases {
		t.Run(string(tc.provider), func(t *testing.T) {
			dir := t.TempDir()
			res, err := Generate(Options{Name: "svc", Dir: dir, Provider: tc.provider})
			if err != nil {
				t.Fatalf("Generate: %v", err)
			}
			if res.APIKey != tc.apiKey {
				t.Errorf("APIKey = %q, want %q", res.APIKey, tc.apiKey)
			}
			yaml := readFile(t, dir, "microagent.yaml")
			if !strings.Contains(yaml, tc.sdk) {
				t.Errorf("microagent.yaml missing SDK %q", tc.sdk)
			}
			agent := readFile(t, dir, "agent.py")
			if !strings.Contains(agent, tc.agentMatch) {
				t.Errorf("agent.py missing %q", tc.agentMatch)
			}
			if strings.Contains(agent, "{{") {
				t.Errorf("agent.py contains unrendered template directive")
			}
			readme := readFile(t, dir, "README.md")
			if !strings.Contains(readme, tc.apiKey) {
				t.Errorf("README.md missing API key env %q", tc.apiKey)
			}
		})
	}
}

func TestGenerateDefaultsDirToName(t *testing.T) {
	base := t.TempDir()
	// Run from inside base so the default relative dir lands under it.
	t.Chdir(base)
	res, err := Generate(Options{Name: "scout"})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if res.Dir != "scout" {
		t.Fatalf("Dir = %q, want scout", res.Dir)
	}
	if _, err := os.Stat(filepath.Join(base, "scout", "microagent.yaml")); err != nil {
		t.Fatalf("expected scout/microagent.yaml: %v", err)
	}
}

func TestGenerateFailsClosedOnExisting(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "protocol.py"), []byte("custom"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(Options{Name: "x", Dir: dir}); err == nil {
		t.Fatal("expected error overwriting existing file without --force")
	}
	// The conflicting file must be untouched, and no partial scaffold written.
	if got := readFile(t, dir, "protocol.py"); got != "custom" {
		t.Errorf("protocol.py was modified: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "agent.py")); !os.IsNotExist(err) {
		t.Errorf("agent.py should not have been written on fail-closed: %v", err)
	}

	// Force overwrites.
	if _, err := Generate(Options{Name: "x", Dir: dir, Force: true}); err != nil {
		t.Fatalf("Generate with Force: %v", err)
	}
	if got := readFile(t, dir, "protocol.py"); got == "custom" {
		t.Errorf("protocol.py should have been overwritten with --force")
	}
}

func TestGenerateRejectsBadInput(t *testing.T) {
	if _, err := Generate(Options{Name: ""}); err == nil {
		t.Error("expected error for empty name")
	}
	if _, err := Generate(Options{Name: "a/b", Dir: t.TempDir()}); err == nil {
		t.Error("expected error for name with slash")
	}
	if _, err := Generate(Options{Name: "ok", Dir: t.TempDir(), Provider: "cohere"}); err == nil {
		t.Error("expected error for unsupported provider")
	}
}

func readFile(t *testing.T, dir, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func firstLines(s string, n int) string {
	lines := strings.SplitN(s, "\n", n+1)
	if len(lines) > n {
		lines = lines[:n]
	}
	return strings.Join(lines, "\n")
}
