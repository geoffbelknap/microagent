package modelservice

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/internal/hostworker"
	"github.com/geoffbelknap/microagent/pkg/modelrunner"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

const (
	envModelMediation     = "MICROAGENT_MODEL_MEDIATION"
	envModelPolicyURL     = "MICROAGENT_MODEL_POLICY_URL"
	envModelPolicyFile    = "MICROAGENT_MODEL_POLICY_FILE"
	envModelPolicyTimeout = "MICROAGENT_MODEL_POLICY_TIMEOUT"
)

type modelMediationConfig struct {
	Enabled       bool
	Mode          hostworker.Mode
	PolicyURL     string
	PolicyFile    string
	PolicyTimeout time.Duration
}

func modelMediationConfigFromEnv() (modelMediationConfig, error) {
	return modelMediationConfigFromSpec(workspace.ModelMediationSpec{})
}

func modelMediationConfigFromSpec(spec workspace.ModelMediationSpec) (modelMediationConfig, error) {
	rawMode := strings.ToLower(strings.TrimSpace(firstNonEmpty(spec.Mode, os.Getenv(envModelMediation))))
	policyURL := strings.TrimSpace(firstNonEmpty(spec.PolicyURL, os.Getenv(envModelPolicyURL)))
	policyFile := strings.TrimSpace(firstNonEmpty(spec.PolicyFile, os.Getenv(envModelPolicyFile)))
	if rawMode == "" && (policyURL != "" || policyFile != "") {
		rawMode = "policy"
	}
	if rawMode == "" || rawMode == "off" || rawMode == "0" || rawMode == "false" || rawMode == "disabled" {
		return modelMediationConfig{}, nil
	}
	cfg := modelMediationConfig{Enabled: true, PolicyTimeout: 2 * time.Second}
	switch rawMode {
	case "local", "local-allow", "allow":
		cfg.Mode = hostworker.ModeLocalAllow
	case "policy":
		cfg.Mode = hostworker.ModePolicy
	default:
		return modelMediationConfig{}, fmt.Errorf("%s must be off, local-allow, or policy", envModelMediation)
	}
	timeout, err := durationValue(envModelPolicyTimeout, firstNonEmpty(spec.PolicyTimeout, os.Getenv(envModelPolicyTimeout)), cfg.PolicyTimeout)
	if err != nil {
		return modelMediationConfig{}, err
	}
	cfg.PolicyTimeout = timeout
	cfg.PolicyURL = policyURL
	cfg.PolicyFile = policyFile
	if cfg.Mode != hostworker.ModePolicy && (cfg.PolicyURL != "" || cfg.PolicyFile != "") {
		return modelMediationConfig{}, fmt.Errorf("model policy source requires model mediation policy mode")
	}
	if cfg.Mode == hostworker.ModePolicy {
		switch {
		case cfg.PolicyURL != "" && cfg.PolicyFile != "":
			return modelMediationConfig{}, fmt.Errorf("%s and %s are mutually exclusive", envModelPolicyURL, envModelPolicyFile)
		case cfg.PolicyURL == "" && cfg.PolicyFile == "":
			return modelMediationConfig{}, fmt.Errorf("%s=policy requires %s or %s", envModelMediation, envModelPolicyURL, envModelPolicyFile)
		}
	}
	return cfg, nil
}

func durationValue(name, raw string, fallback time.Duration) (time.Duration, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return fallback, nil
	}
	duration, err := time.ParseDuration(raw)
	if err != nil {
		seconds, parseErr := strconv.ParseFloat(raw, 64)
		if parseErr != nil {
			return 0, fmt.Errorf("%s must be a Go duration like 250ms or 2s, or a number of seconds", name)
		}
		duration = time.Duration(seconds * float64(time.Second))
	}
	if duration <= 0 {
		return 0, fmt.Errorf("%s must be positive", name)
	}
	return duration, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func modelRunnerOverridesFromSpec(spec workspace.ModelRunnerSpec) modelrunner.RunnerOverrides {
	return modelrunner.RunnerOverrides{
		Backend:      spec.Backend,
		GPU:          spec.GPU,
		BackendModel: spec.BackendModel,
		ServedModel:  spec.ServedModel,
		Command:      append([]string{}, spec.Command...),
		Name:         spec.Name,
		HealthPath:   spec.HealthPath,
		Args:         append([]string{}, spec.Args...),
		Env:          append([]string{}, spec.Env...),
	}
}
