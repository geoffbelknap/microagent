package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/modelservice"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// The CLI selects the companion executable; pairing semantics live in the library.
func ensureModelPairing(ctx context.Context, opts *workspaceOptions, modelRef, token string) (func(), error) {
	if strings.TrimSpace(modelRef) == "" {
		return func() {}, nil
	}
	executable, err := modelCompanionExecutable()
	if err != nil {
		return nil, err
	}
	paired := *opts
	paired.Model = modelRef
	release, err := modelservice.Pair(ctx, &paired, modelservice.PairOptions{Token: token, ExecPath: executable})
	if err != nil {
		return nil, err
	}
	*opts = paired
	return release, nil
}

// An explicit path avoids discovering executable code through PATH or the CWD.
func modelCompanionExecutable() (string, error) {
	path := strings.TrimSpace(os.Getenv("MICROAGENT_MODEL_SERVICE_BIN"))
	if path == "" {
		return os.Executable()
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("MICROAGENT_MODEL_SERVICE_BIN must be an absolute executable path")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("MICROAGENT_MODEL_SERVICE_BIN: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0111 == 0 {
		return "", fmt.Errorf("MICROAGENT_MODEL_SERVICE_BIN must name an executable file: %s", path)
	}
	return path, nil
}

func pendingModelRelease(stateDir, name, backend string) func() {
	return modelservice.PendingRelease(stateDir, name, backend)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func mergeModelRunnerSpec(base, override workspace.ModelRunnerSpec) workspace.ModelRunnerSpec {
	out := base
	if strings.TrimSpace(override.Backend) != "" {
		out.Backend = override.Backend
	}
	if strings.TrimSpace(override.GPU) != "" {
		out.GPU = override.GPU
	}
	if strings.TrimSpace(override.BackendModel) != "" {
		out.BackendModel = override.BackendModel
	}
	if strings.TrimSpace(override.ServedModel) != "" {
		out.ServedModel = override.ServedModel
	}
	if len(override.Command) != 0 {
		out.Command = append([]string{}, override.Command...)
	}
	if strings.TrimSpace(override.Name) != "" {
		out.Name = override.Name
	}
	if strings.TrimSpace(override.HealthPath) != "" {
		out.HealthPath = override.HealthPath
	}
	if len(override.Args) != 0 {
		out.Args = append([]string{}, override.Args...)
	}
	if len(override.Env) != 0 {
		out.Env = append([]string{}, override.Env...)
	}
	return out
}

func mergeModelMediationSpec(base, override workspace.ModelMediationSpec) workspace.ModelMediationSpec {
	out := base
	if strings.TrimSpace(override.Mode) != "" {
		out.Mode = override.Mode
	}
	if strings.TrimSpace(override.PolicyURL) != "" {
		out.PolicyURL = override.PolicyURL
	}
	if strings.TrimSpace(override.PolicyFile) != "" {
		out.PolicyFile = override.PolicyFile
	}
	if strings.TrimSpace(override.PolicyTimeout) != "" {
		out.PolicyTimeout = override.PolicyTimeout
	}
	return out
}
