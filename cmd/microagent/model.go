package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/model"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
)

func runModel(args []string, stdout *os.File) error {
	if wantsHelp(args) {
		fmt.Fprintln(stdout, "usage: microagent model <pull|list|delete|prune|serve|stop|runners|policy> ...")
		return nil
	}
	if len(args) > 0 {
		switch args[0] {
		case "pull":
			return runModelPull(args[1:], stdout)
		case "list":
			return runModelList(args[1:], stdout)
		case "delete":
			return runModelRemove(args[1:], stdout)
		case "prune":
			return runModelPrune(args[1:], stdout)
		case "serve":
			return runModelServe(args[1:], stdout)
		case "stop":
			return runModelStop(args[1:], stdout)
		case "runners":
			return runModelRunners(args[1:], stdout)
		case "policy":
			return runModelPolicy(args[1:], stdout)
		}
	}
	return fmt.Errorf("usage: microagent model <pull|list|delete|prune|serve|stop|runners|policy> [args]")
}

func runModelPolicy(args []string, stdout *os.File) error {
	if wantsHelp(args) || len(args) == 0 {
		printModelPolicyHelp(stdout)
		return nil
	}
	switch args[0] {
	case "validate":
		return runModelPolicyValidate(args[1:], stdout)
	case "evaluate", "eval":
		return runModelPolicyEvaluate(args[1:], stdout)
	default:
		return fmt.Errorf("usage: microagent model policy <validate|evaluate> args")
	}
}

type modelPolicyValidationOutput struct {
	OK            bool   `json:"ok"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	SchemaVersion string `json:"schema_version"`
	Default       string `json:"default"`
	Rules         int    `json:"rules"`
}

type modelPolicyEvaluationOutput struct {
	OK            bool   `json:"ok"`
	Path          string `json:"path"`
	SHA256        string `json:"sha256"`
	Decision      string `json:"decision"`
	Reason        string `json:"reason"`
	RuleID        string `json:"rule_id,omitempty"`
	AuditEventID  string `json:"audit_event_id,omitempty"`
	Expected      string `json:"expected,omitempty"`
	MatchedExpect bool   `json:"matched_expect,omitempty"`
}

func runModelPolicyValidate(args []string, stdout *os.File) error {
	fs := flag.NewFlagSet("model policy validate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model policy validate <policy.json>")
	}
	policy, source, err := hostworker.LoadFilePolicy(fs.Arg(0))
	if err != nil {
		return err
	}
	out := modelPolicyValidationOutput{
		OK:            true,
		Path:          source.Path,
		SHA256:        source.SHA256,
		SchemaVersion: policy.SchemaVersion,
		Default:       policy.Default,
		Rules:         len(policy.Rules),
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, out)
	}
	fmt.Fprintf(stdout, "Policy valid: %s (%d rule(s), sha256 %s)\n", out.Path, out.Rules, out.SHA256)
	return nil
}

func runModelPolicyEvaluate(args []string, stdout *os.File) error {
	maxTokensSet := hasFlagValue(args, "max-tokens")
	streamSet := hasFlagValue(args, "stream")
	fs := flag.NewFlagSet("model policy evaluate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	method := fs.String("method", http.MethodGet, "Request method")
	requestPath := fs.String("path", "/v1/models", "Request path as seen by the mediator")
	workspaceID := fs.String("workspace-id", "", "Workspace ID")
	capability := fs.String("capability", hostworker.DefaultCapability, "Capability")
	workerID := fs.String("worker-id", "policy-evaluate", "Worker ID")
	modelName := fs.String("model", "", "Declared request model")
	requestBytes := fs.Int64("request-bytes", 0, "Request body size in bytes")
	textBytes := fs.Int64("text-bytes", 0, "Aggregate prompt/message text byte count")
	messages := fs.Int("messages", 0, "Message count")
	maxTokens := fs.Int("max-tokens", 0, "Declared max_tokens value")
	streamRaw := fs.String("stream", "", "Declared stream mode: true or false")
	expect := fs.String("expect", "", "Expected decision: allow or deny")
	var tools multiFlag
	fs.Var(&tools, "tool", "Declared tool/function name (repeatable)")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model policy evaluate <policy.json> [--method <method>] [--path <path>] [--model <model>] [--max-tokens <n>] [--stream true|false] [--tool <name>] [--expect allow|deny]")
	}
	policy, source, err := hostworker.LoadFilePolicy(fs.Arg(0))
	if err != nil {
		return err
	}
	body := &hostworker.DecisionRequestBody{
		Model:        strings.TrimSpace(*modelName),
		MessageCount: *messages,
		TextBytes:    *textBytes,
		ToolNames:    append([]string{}, tools...),
	}
	if streamSet {
		parsed, err := strconv.ParseBool(strings.TrimSpace(*streamRaw))
		if err != nil {
			return fmt.Errorf("--stream must be true or false")
		}
		body.Stream = &parsed
	}
	if maxTokensSet {
		value := *maxTokens
		body.MaxTokens = &value
	}
	envelope := hostworker.DecisionEnvelope{
		SchemaVersion: 1,
		RequestID:     "policy-evaluate",
		Workspace:     hostworker.DecisionWorkspace{ID: strings.TrimSpace(*workspaceID)},
		Capability:    strings.TrimSpace(*capability),
		Worker: hostworker.DecisionWorker{
			ID:       strings.TrimSpace(*workerID),
			Protocol: "openai-compatible",
		},
		Request: hostworker.DecisionRequest{
			Method: strings.ToUpper(strings.TrimSpace(*method)),
			Path:   strings.TrimSpace(*requestPath),
			Bytes:  *requestBytes,
			Body:   body,
		},
	}
	decision := policy.Decide(envelope, source, "policy-evaluate")
	expected := strings.ToLower(strings.TrimSpace(*expect))
	if expected != "" && expected != "allow" && expected != "deny" {
		return fmt.Errorf("--expect must be allow or deny")
	}
	out := modelPolicyEvaluationOutput{
		OK:            true,
		Path:          source.Path,
		SHA256:        source.SHA256,
		Decision:      decision.Decision,
		Reason:        decision.Reason,
		RuleID:        decision.PolicyRuleID,
		AuditEventID:  decision.AuditEventID,
		Expected:      expected,
		MatchedExpect: expected == "" || expected == decision.Decision,
	}
	if outputJSON(stdout) {
		if err := writeJSON(stdout, out); err != nil {
			return err
		}
	} else {
		fmt.Fprintf(stdout, "%s\t%s", out.Decision, out.Reason)
		if out.RuleID != "" {
			fmt.Fprintf(stdout, "\t%s", out.RuleID)
		}
		fmt.Fprintln(stdout)
	}
	if !out.MatchedExpect {
		return fmt.Errorf("policy decision %s did not match expected %s", out.Decision, expected)
	}
	return nil
}

func printModelPolicyHelp(stdout io.Writer) {
	fmt.Fprint(stdout, `microagent model policy

Validate or dry-run experimental model mediation policy files.

Usage:
  microagent model policy validate <policy.json>
  microagent model policy evaluate <policy.json> [options]

Evaluate options:
  --method <method>       Request method (default GET)
  --path <path>           Request path as seen by the mediator (default /v1/models)
  --workspace-id <id>     Workspace ID
  --capability <name>     Capability (default model.openai)
  --worker-id <id>        Worker ID
  --model <model>         Declared request model
  --request-bytes <n>     Request body byte count
  --text-bytes <n>        Aggregate prompt/message text byte count
  --messages <n>          Message count
  --max-tokens <n>        Declared max_tokens value
  --stream true|false     Declared stream mode
  --tool <name>           Declared tool/function name (repeatable)
  --expect allow|deny     Fail if the evaluated decision differs
`)
}

func runModelPull(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model pull", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	token := fs.String("token", "", "HuggingFace bearer token (else HF_TOKEN/HUGGING_FACE_HUB_TOKEN)")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model pull <hf-ref> [--token <t>] [--state-dir <dir>]")
	}
	record, err := model.Pull(context.Background(), model.PullOptions{StateDir: stateDir, ModelRef: fs.Arg(0), Token: *token})
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Pulled %s (%d bytes, %s)\n", record.ModelRef, record.SizeBytes, record.Digest)
	return nil
}

func runModelList(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model ls", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	list, err := model.List(stateDir)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"models": list})
	}
	for _, m := range list {
		fmt.Fprintf(stdout, "%s\t%d\t%s\n", m.ModelRef, m.SizeBytes, m.Digest)
	}
	return nil
}

func runModelRemove(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model rm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	keepFiles := fs.Bool("keep-files", false, "Remove the index entry but keep the blob on disk")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model delete <ref> [--keep-files] [--state-dir <dir>]")
	}
	res, err := model.Remove(stateDir, fs.Arg(0), !*keepFiles)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, res)
	}
	fmt.Fprintf(stdout, "Removed %d model(s)\n", len(res.Removed))
	return nil
}

func runModelPrune(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model prune", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	deleteFiles := fs.Bool("delete-files", false, "Also delete blob files for pruned entries")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	res, err := model.Prune(stateDir, *deleteFiles)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, res)
	}
	fmt.Fprintf(stdout, "Pruned %d model(s)\n", len(res.Removed))
	return nil
}

func runModelServe(args []string, stdout *os.File) error {
	if wantsHelp(args) {
		printModelServeHelp(stdout)
		return nil
	}
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dedicated := fs.Bool("dedicated", false, "Start a dedicated runner instead of sharing one")
	token := fs.String("token", "", "HuggingFace token for auto-pull (else HF_TOKEN/HUGGING_FACE_HUB_TOKEN)")
	runnerBackend := fs.String("runner", "", "Model runner backend: llamacpp, vllm, or custom")
	runnerGPU := fs.String("runner-gpu", "", "Model runner GPU intent: off, on, or auto")
	runnerModel := fs.String("runner-model", "", "Backend model id for runners such as vLLM")
	runnerServedModel := fs.String("runner-served-model", "", "OpenAI-compatible served model name for runners such as vLLM")
	runnerCommand := fs.String("runner-command", "", "Host model runner command template")
	runnerName := fs.String("runner-name", "", "Host model runner name for state output")
	runnerHealthPath := fs.String("runner-health-path", "", "Host model runner health probe path")
	var runnerArgs multiFlag
	var runnerEnv multiFlag
	fs.Var(&runnerArgs, "runner-arg", "Extra model runner argument (repeatable)")
	fs.Var(&runnerEnv, "runner-env", "Extra model runner environment KEY=VALUE (repeatable)")
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model serve <hf-ref> [--dedicated] [--runner <llamacpp|vllm|custom>] [--runner-gpu <off|on|auto>] [--runner-model <id>] [--runner-served-model <name>] [--runner-command <template>] [--runner-name <name>] [--runner-health-path <path>] [--runner-arg <arg>] [--runner-env KEY=VALUE] [--token <t>] [--state-dir <dir>]")
	}
	ref := fs.Arg(0)
	canonical, _, err := model.Resolve(ref)
	if err != nil {
		return err
	}
	rec, err := model.Find(stateDir, canonical)
	if err != nil {
		// Not in the store yet — auto-pull, like run does for images.
		rec, err = model.Pull(context.Background(), model.PullOptions{StateDir: stateDir, ModelRef: ref, Token: *token})
		if err != nil {
			return err
		}
	}
	engine, runnerConfig, err := resolveModelRunner(modelRunnerOverrides{
		Backend:      *runnerBackend,
		GPU:          *runnerGPU,
		BackendModel: *runnerModel,
		ServedModel:  *runnerServedModel,
		CommandRaw:   *runnerCommand,
		Name:         *runnerName,
		HealthPath:   *runnerHealthPath,
		Args:         runnerArgs,
		Env:          runnerEnv,
	})
	if err != nil {
		return err
	}
	runner, err := modelrunner.Ensure(context.Background(), modelrunner.EnsureOptions{
		StateDir:     stateDir,
		ModelRef:     rec.ModelRef,
		ModelPath:    rec.OutputPath,
		Engine:       engine,
		Pinned:       true,
		Dedicated:    *dedicated,
		RunnerConfig: runnerConfig,
	})
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, runner)
	}
	fmt.Fprintf(stdout, "Serving %s on %s:%d (pid %d)\n", runner.ModelRef, runner.Host, runner.Port, runner.PID)
	return nil
}

func printModelServeHelp(stdout io.Writer) {
	fmt.Fprint(stdout, `microagent model serve <hf-ref>
microagent model serve <hf-ref>

Start or reuse a pinned host model runner process for a HuggingFace GGUF model.

Options:
  --dedicated                    Start a dedicated runner instead of sharing one
  --runner <backend>             Model runner backend: llamacpp, vllm, or custom
  --runner-gpu <mode>            Model runner GPU intent: off, on, or auto
  --runner-model <id>            Backend model id for runners such as vLLM
  --runner-served-model <name>   OpenAI-compatible served model name
  --runner-command <template>    Host model runner command template
  --runner-name <name>           Host model runner name for state output
  --runner-health-path <path>    Host model runner health probe path
  --runner-arg <arg>             Extra model runner argument (repeatable)
  --runner-env KEY=VALUE         Extra model runner environment override (repeatable)
  --token <t>                    HuggingFace token for auto-pull
  --state-dir <dir>              State directory
`)
}

func runModelStop(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model stop", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: microagent model stop <hf-ref> [--state-dir <dir>]")
	}
	canonical, _, err := model.Resolve(fs.Arg(0))
	if err != nil {
		return err
	}
	n, err := modelrunner.Stop(stateDir, canonical)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"stopped": n})
	}
	fmt.Fprintf(stdout, "Stopped %d runner(s)\n", n)
	return nil
}

func runModelRunners(args []string, stdout *os.File) error {
	stateDir := defaultStateDir()
	fs := flag.NewFlagSet("model runners", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.StringVar(&stateDir, "state-dir", stateDir, "State directory")
	if err := fs.Parse(reorderFlagArgs(args)); err != nil {
		return err
	}
	list, err := modelrunner.List(stateDir)
	if err != nil {
		return err
	}
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"runners": list})
	}
	for _, r := range list {
		fmt.Fprintf(stdout, "%s\t%s:%d\tpid=%d\tholders=%d\tpinned=%t\n", r.ModelRef, r.Host, r.Port, r.PID, len(r.Holders), r.Pinned)
	}
	return nil
}
