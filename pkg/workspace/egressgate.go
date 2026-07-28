package workspace

import (
	"fmt"
	"os"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/egressprereq"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// validateEgressHostPrereqs fails a mediated launch closed, at admission, when
// this host cannot deliver the mediation the request asks for: on Linux, UDP
// egress mediation steers datagrams through TPROXY, whose kernel modules a
// rootless boot cannot modprobe. Without this gate the same request fails
// mid-boot inside the supervisor's netns setup with a low-level nft error;
// with it, the refusal happens before any state is created and names the
// modules and both remediations (load them, or choose --egress off — the
// explicit, recorded way to run unmediated).
//
// The gate is request-aware on purpose: egress off and isolated/no-egress
// network modes pass untouched, because a host that cannot mediate UDP can
// still run everything that does not ask for mediation. Blocking those starts
// would be a false gate, and false gates teach operators to reach for the
// override reflexively.
//
// The supervisor's own prerequisite verification stays the authoritative
// fail-closed enforcement layer; this gate only refuses on a positive
// missing-module determination. A probe that cannot read /proc/modules
// returns nil rather than guessing — the boot path still fails closed.
func validateEgressHostPrereqs(backend, networkMode, egressMode string) error {
	return validateEgressHostPrereqsProbed(backend, networkMode, egressMode, os.ReadFile, func(path string) bool {
		_, err := os.Stat(path)
		return err == nil
	})
}

// validateEgressHostPrereqsProbed is the injectable core of
// validateEgressHostPrereqs. readFile and statModule are seams so the
// fail-closed decision is unit-testable without touching real host state.
func validateEgressHostPrereqsProbed(backend, networkMode, egressMode string, readFile func(string) ([]byte, error), statModule func(string) bool) error {
	if backend != vmkit.BackendLinuxKVM {
		return nil
	}
	mode := vmkit.ResolveEgressModeDefault(egressMode)
	if !vmkit.EgressMediationOn(mode) || !vmkit.NetworkModeMediates(networkMode) {
		return nil
	}
	procModules, err := readFile("/proc/modules")
	if err != nil {
		return nil
	}
	missing := egressprereq.MissingModules(egressprereq.ParseLoadedModules(procModules), func(name string) bool {
		return statModule("/sys/module/" + name)
	})
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("egress mode %q needs TPROXY kernel modules this host is missing: %s; load them (e.g. `modprobe nft_tproxy`) or build them into the kernel, or re-run with --egress off for explicit unmediated networking", mode, strings.Join(missing, ", "))
}
