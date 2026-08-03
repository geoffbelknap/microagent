package main

import (
	"fmt"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func cloneWorkspace(stateDir, source, target string) (workspaceResult, error) {
	return workspace.Clone(stateDir, source, target)
}

// validateWorkspaceName is the CLI adapter over the library's one name rule;
// keeping it a delegation means the CLI can never accept a name the library
// would refuse (or vice versa).
func validateWorkspaceName(name string) error {
	return workspace.ValidateName(name)
}

func validateSafeBasename(field, value string) error {
	if !vmkit.SafeIdentifier(value) {
		return fmt.Errorf("invalid %s: %s", field, value)
	}
	return nil
}
