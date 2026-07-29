package main

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func readWorkspaceManifest(stateDir, name string) (workspaceManifest, error) {
	data, err := os.ReadFile(filepath.Join(stateDir, "workspaces", name, "workspace.json"))
	if err != nil {
		return workspaceManifest{}, err
	}
	var manifest workspaceManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return workspaceManifest{}, err
	}
	return manifest, nil
}

func workspaceOptionsFromRequest(req vmkit.Request, supervisorPath string) (workspaceOptions, error) {
	return workspace.OptionsFromRequest(req, supervisorPath)
}

func writeWorkspaceManifest(opts workspaceOptions) error {
	return workspace.WriteManifest(opts)
}
