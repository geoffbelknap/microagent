package diagnostics

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	firecracker "github.com/geoffbelknap/microagent/pkg/supervisors/firecracker"
)

// defaultPastaStartProbe runs pasta the way a workspace boot will: writing
// its pid file under the state directory, then exiting with a trivial
// command. Finding the binary is not the capability — on hosts whose SELinux
// policy confines pasta_t, a present, executable pasta fails at start against
// the state dir under $HOME, and a doctor that only ran LookPath reported a
// green host that cannot boot. The probe performs the failing operation
// itself, so it fails exactly where the boot would.
func defaultPastaStartProbe(pastaPath, stateDir string) error {
	dir := filepath.Join(stateDir, "doctor-pasta-probe")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("prepare probe dir: %w", err)
	}
	defer os.RemoveAll(dir)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, pastaPath, "--pid", filepath.Join(dir, "pasta.pid"), "--", "/bin/true")
	out, err := cmd.CombinedOutput()
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = err.Error()
		}
		return fmt.Errorf("%s", detail)
	}
	return nil
}

// defaultSELinuxConfinedPasta explains a failed probe on confined hosts; see
// the firecracker supervisor's SELinuxConfinedPastaDetail for why it is only
// consulted after a real failure.
var defaultSELinuxConfinedPasta = firecracker.SELinuxConfinedPastaDetail
