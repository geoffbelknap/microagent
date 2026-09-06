package modelservice

import (
	"strings"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

// PendingRelease captures the workspace's paired model ref now (delete
// removes the manifest) and returns a release func to invoke once the
// lifecycle verb has succeeded. Cleanup is best-effort. A missing manifest
// still releases the host service; a known model also releases its runner hold.
func PendingRelease(stateDir, name, backend string) func() {
	manifest, err := workspace.ReadManifest(stateDir, name)
	if err != nil {
		return func() {
			_ = releaseModelService(stateDir, name)
		}
	}
	modelRef := strings.TrimSpace(manifest.Model)
	if modelRef == "" {
		return func() {
			_ = releaseModelService(stateDir, name)
		}
	}
	return func() {
		_ = releaseModelService(stateDir, name)
		_ = releaseModelRunner(stateDir, modelRef, name)
		_ = appendModelWorkerReleasedEvent(stateDir, name, backend, modelRef)
	}
}
