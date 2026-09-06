package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelPolicyValidateAndEvaluate(t *testing.T) {
	policyPath := writeModelPolicyTestFile(t, `{
		"schema_version": "microagent.model_policy.v1",
		"default": "deny",
		"rules": [
			{
				"id": "models",
				"effect": "allow",
				"match": {"methods": ["GET"], "paths": ["/v1/models"]}
			},
			{
				"id": "chat",
				"effect": "allow",
				"match": {"methods": ["POST"], "paths": ["/v1/chat/completions"], "models": ["tiny"]},
				"limits": {
					"max_text_bytes": 16,
					"max_messages": 2,
					"max_tokens": 16,
					"stream": false,
					"allowed_tool_names": ["shell"]
				}
			}
		]
	}`)

	validateOut, err := runMainForTest(t, "--json", "model", "policy", "validate", policyPath)
	if err != nil {
		t.Fatalf("policy validate: %v\n%s", err, validateOut)
	}
	var validation modelPolicyValidationOutput
	if err := json.Unmarshal(validateOut, &validation); err != nil {
		t.Fatalf("decode validation output: %v\n%s", err, validateOut)
	}
	if !validation.OK || validation.Rules != 2 || validation.SHA256 == "" || validation.Path == "" {
		t.Fatalf("validation = %+v", validation)
	}

	allowOut, err := runMainForTest(t,
		"--json", "model", "policy", "evaluate", policyPath,
		"--method", "POST",
		"--path", "/v1/chat/completions",
		"--model", "tiny",
		"--max-tokens", "8",
		"--stream", "false",
		"--tool", "shell",
		"--text-bytes", "5",
		"--messages", "1",
		"--expect", "allow",
	)
	if err != nil {
		t.Fatalf("policy evaluate allow: %v\n%s", err, allowOut)
	}
	var allowEval modelPolicyEvaluationOutput
	if err := json.Unmarshal(allowOut, &allowEval); err != nil {
		t.Fatalf("decode allow output: %v\n%s", err, allowOut)
	}
	if allowEval.Decision != "allow" || allowEval.RuleID != "chat" || !allowEval.MatchedExpect {
		t.Fatalf("allow evaluation = %+v", allowEval)
	}

	denyOut, err := runMainForTest(t,
		"--json", "model", "policy", "evaluate", policyPath,
		"--method", "POST",
		"--path", "/v1/chat/completions",
		"--model", "tiny",
		"--max-tokens", "8",
		"--stream", "false",
		"--tool", "network",
		"--text-bytes", "5",
		"--messages", "1",
		"--expect", "deny",
	)
	if err != nil {
		t.Fatalf("policy evaluate deny: %v\n%s", err, denyOut)
	}
	var denyEval modelPolicyEvaluationOutput
	if err := json.Unmarshal(denyOut, &denyEval); err != nil {
		t.Fatalf("decode deny output: %v\n%s", err, denyOut)
	}
	if denyEval.Decision != "deny" || denyEval.Reason != "file_policy_limit_tool_name" || !denyEval.MatchedExpect {
		t.Fatalf("deny evaluation = %+v", denyEval)
	}

	mismatchOut, err := runMainForTest(t,
		"--json", "model", "policy", "evaluate", policyPath,
		"--method", "POST",
		"--path", "/v1/chat/completions",
		"--model", "tiny",
		"--max-tokens", "32",
		"--stream", "false",
		"--expect", "allow",
	)
	if err == nil || !strings.Contains(err.Error(), "did not match expected") {
		t.Fatalf("expected mismatch error, got err=%v out=%s", err, mismatchOut)
	}
	var mismatchEval modelPolicyEvaluationOutput
	if err := json.Unmarshal(mismatchOut, &mismatchEval); err != nil {
		t.Fatalf("decode mismatch output: %v\n%s", err, mismatchOut)
	}
	if mismatchEval.Decision != "deny" || mismatchEval.MatchedExpect {
		t.Fatalf("mismatch evaluation = %+v", mismatchEval)
	}
}

func TestModelPolicyValidateRejectsInvalidPolicy(t *testing.T) {
	policyPath := writeModelPolicyTestFile(t, `{"schema_version":"wrong","default":"allow"}`)
	out, err := runMainForTest(t, "model", "policy", "validate", policyPath)
	if err == nil || !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("expected schema error, got err=%v out=%s", err, out)
	}
}

func TestModelPolicyEvalSpellingWorks(t *testing.T) {
	t.Cleanup(func() { outputFormat = "" })
	// "eval" is the pre-existing short spelling; verify it reaches evaluate behavior.
	policyPath := writeModelPolicyTestFile(t, `{
		"schema_version": "microagent.model_policy.v1",
		"default": "deny",
		"rules": [
			{
				"id": "allow_all",
				"effect": "allow",
				"match": {"methods": ["GET"], "paths": ["*"]}
			}
		]
	}`)

	evalOut, err := runMainForTest(t,
		"--json", "model", "policy", "eval", policyPath,
		"--method", "GET",
		"--path", "/v1/models",
		"--expect", "allow",
	)
	if err != nil {
		t.Fatalf("policy eval (using 'eval' alias): %v\n%s", err, evalOut)
	}
	var evalResult modelPolicyEvaluationOutput
	if err := json.Unmarshal(evalOut, &evalResult); err != nil {
		t.Fatalf("decode eval output: %v\n%s", err, evalOut)
	}
	if evalResult.Decision != "allow" || !evalResult.MatchedExpect {
		t.Fatalf("eval result = %+v", evalResult)
	}
}

func runMainForTest(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	stdoutPath := filepath.Join(t.TempDir(), "stdout")
	stdout, err := os.Create(stdoutPath)
	if err != nil {
		t.Fatal(err)
	}
	runErr := run(t.Context(), args, stdout)
	if closeErr := stdout.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	out, readErr := os.ReadFile(stdoutPath)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return out, runErr
}

func writeModelPolicyTestFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "policy.json")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write policy: %v", err)
	}
	return path
}
