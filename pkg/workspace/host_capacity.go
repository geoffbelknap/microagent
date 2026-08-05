package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/fsutil"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

// boundedOperationsStatus computes the ASK tenet 8 bounds in force for opts,
// attached by Inspect/Status. LeaseSeconds and the egress caps come from the
// recorded runtime state — a resolved decision, not a hypothetical one — and
// are left at zero (omitted) when no runtime state has been recorded yet.
// WorkspaceCeiling/ActiveWorkspaces are host-wide facts, always available.
func boundedOperationsStatus(opts Options) *vmkit.BoundedOperationsStatus {
	out := &vmkit.BoundedOperationsStatus{}
	if state, err := ReadRuntimeState(opts); err == nil {
		out.LeaseSeconds = state.Config.LeaseSeconds
		out.EgressMaxBytesPerSec = state.Config.EgressMaxBytesPerSec
		out.EgressMaxTotalBytes = state.Config.EgressMaxTotalBytes
		out.EgressMaxConcurrentConns = state.Config.EgressMaxConcurrentConns
	}
	limit, source := MaxWorkspaces()
	out.WorkspaceCeiling = limit
	out.WorkspaceCeilingSource = string(source)
	if active, err := CountActiveWorkspaces(opts.StateDir); err == nil {
		out.ActiveWorkspaces = active
	}
	return out
}

// MaxWorkspacesEnv overrides the computed workspace-count ceiling when set to
// a positive integer. An operator who wants a different bound sets this
// rather than a per-workspace flag, because the ceiling is a host-wide
// policy, not a property of any one workspace.
const MaxWorkspacesEnv = "MICROAGENT_MAX_WORKSPACES"

// fallbackMaxWorkspaces is the ceiling applied when host memory cannot be
// detected (an unsupported platform, or the probe itself failing). It is
// still a bound — ASK tenet 8 requires one to exist even when the ideal
// input for computing it is unavailable — deliberately conservative rather
// than generous, since an operator who needs more can always set
// MaxWorkspacesEnv explicitly once they notice.
const fallbackMaxWorkspaces = 10

// minWorkspaceCeiling and maxWorkspaceCeiling clamp the memory-derived
// ceiling so a very small host is not left uselessly restrictive and a very
// large one does not get a number so high it stops meaning "bounded".
const (
	minWorkspaceCeiling = 4
	maxWorkspaceCeiling = 100
)

// hostReservedMemoryMiB is set aside for the host OS and microagent's own
// processes before the remainder is divided into workspace-sized slots.
const hostReservedMemoryMiB = 2048

// DefaultMaxWorkspaces computes the workspace-count ceiling from total host
// memory: the memory left after hostReservedMemoryMiB, divided into
// DefaultWorkspaceMemoryMiB (the small-profile size, the lightest default)
// slots, clamped to [minWorkspaceCeiling, maxWorkspaceCeiling]. It answers
// "how many minimal workspaces could this host plausibly hold", not how many
// of the caller's actual (possibly larger-profile) workspaces fit — a
// deliberately simple, legible number over a precise capacity plan.
func DefaultMaxWorkspaces(totalMemoryMiB int64) int {
	usable := totalMemoryMiB - hostReservedMemoryMiB
	if usable < 0 {
		usable = 0
	}
	n := int(usable / DefaultWorkspaceMemoryMiB)
	if n < minWorkspaceCeiling {
		return minWorkspaceCeiling
	}
	if n > maxWorkspaceCeiling {
		return maxWorkspaceCeiling
	}
	return n
}

// MaxWorkspacesSource names where a resolved ceiling came from, for
// status/doctor to explain the number rather than just state it.
type MaxWorkspacesSource string

const (
	MaxWorkspacesSourceOperator MaxWorkspacesSource = "operator"
	MaxWorkspacesSourceComputed MaxWorkspacesSource = "computed"
	MaxWorkspacesSourceFallback MaxWorkspacesSource = "fallback"
)

// MaxWorkspaces resolves the workspace-count ceiling in force on this host:
// MaxWorkspacesEnv when set to a positive integer, otherwise
// DefaultMaxWorkspaces from detected host memory, falling back to
// fallbackMaxWorkspaces when memory detection fails (never an error the
// caller must handle — a bound is always returned).
func MaxWorkspaces() (int, MaxWorkspacesSource) {
	if raw := strings.TrimSpace(os.Getenv(MaxWorkspacesEnv)); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			return n, MaxWorkspacesSourceOperator
		}
	}
	totalMiB, err := hostTotalMemoryMiB()
	if err != nil {
		return fallbackMaxWorkspaces, MaxWorkspacesSourceFallback
	}
	return DefaultMaxWorkspaces(totalMiB), MaxWorkspacesSourceComputed
}

// activeWorkspaceStates are the resource-holding states that count against
// the workspace-count ceiling. Halted/Stopped/Failed workspaces sit inert on
// disk — a disk-quota concern, not this dimension of bounded operations.
var activeWorkspaceStates = map[vmkit.VMState]bool{
	vmkit.StateStarting: true,
	vmkit.StateRunning:  true,
	vmkit.StatePaused:   true,
}

// CountActiveWorkspaces reports how many workspaces under stateDir are
// currently in a resource-holding state.
func CountActiveWorkspaces(stateDir string) (int, error) {
	entries, err := List(stateDir)
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if activeWorkspaceStates[vmkit.VMState(entry.State)] {
			count++
		}
	}
	return count, nil
}

// EnsureWorkspaceCapacity fails closed when starting opts.Name would push the
// host's active-workspace count past the resolved ceiling (ASK tenet 8:
// operations are bounded). Lifecycle starts use reserveWorkspaceCapacity so
// concurrent callers cannot all pass this check before any records Starting.
func EnsureWorkspaceCapacity(opts Options) error {
	releaseGlobal, err := capacityGlobalLock(opts.StateDir)
	if err != nil {
		return err
	}
	defer releaseGlobal()
	active, err := countActiveAndReservedWorkspaces(opts.StateDir)
	if err != nil {
		return err
	}
	return checkWorkspaceCapacity(active)
}

const capacityReservationDir = ".capacity-reservations"

func capacityGlobalLock(stateDir string) (func() error, error) {
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, err
	}
	return fsutil.Lock(filepath.Join(stateDir, ".capacity.lock"))
}

// reserveWorkspaceCapacity atomically claims one host-wide workspace slot.
// The per-workspace reservation remains locked until release, while the global
// lock is held only across count+claim. Other processes count a live reservation
// and an active runtime of the same name once, so there is no gap or double
// count as a start transitions to Running. A crashed process drops its flock;
// the next reservation prunes the stale file.
func reserveWorkspaceCapacity(opts Options) (func(), error) {
	releaseGlobal, err := capacityGlobalLock(opts.StateDir)
	if err != nil {
		return nil, err
	}
	defer releaseGlobal()

	active, err := countActiveAndReservedWorkspaces(opts.StateDir)
	if err != nil {
		return nil, err
	}
	if err := checkWorkspaceCapacity(active); err != nil {
		return nil, err
	}
	dir := filepath.Join(opts.StateDir, capacityReservationDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, opts.Name+".lock")
	releaseReservation, acquired, err := fsutil.TryLock(path)
	if err != nil {
		return nil, err
	}
	if !acquired {
		return nil, fmt.Errorf("workspace %s already has a capacity reservation", opts.Name)
	}
	return func() {
		// Close+unlink under the same global lock used by count+claim. Without
		// this, another process could lock the old inode between close and unlink,
		// then a third could reserve a newly-created file at the same path.
		releaseGlobal, lockErr := capacityGlobalLock(opts.StateDir)
		if lockErr != nil {
			_ = releaseReservation()
			return // leave a stale file for the next successful claimant to prune
		}
		defer releaseGlobal()
		_ = releaseReservation()
		_ = os.Remove(path)
	}, nil
}

func countActiveAndReservedWorkspaces(stateDir string) (int, error) {
	entries, err := List(stateDir)
	if err != nil {
		return 0, err
	}
	counted := make(map[string]bool)
	for _, entry := range entries {
		if activeWorkspaceStates[vmkit.VMState(entry.State)] {
			counted[entry.Name] = true
		}
	}
	dir := filepath.Join(stateDir, capacityReservationDir)
	reservations, err := os.ReadDir(dir)
	if err != nil && !os.IsNotExist(err) {
		return 0, err
	}
	for _, reservation := range reservations {
		if reservation.IsDir() || !strings.HasSuffix(reservation.Name(), ".lock") {
			continue
		}
		path := filepath.Join(dir, reservation.Name())
		release, acquired, lockErr := fsutil.TryLock(path)
		if lockErr != nil {
			return 0, lockErr
		}
		if acquired {
			_ = release()
			_ = os.Remove(path)
			continue
		}
		name := strings.TrimSuffix(reservation.Name(), ".lock")
		counted[name] = true
	}
	return len(counted), nil
}

func checkWorkspaceCapacity(active int) error {
	limit, source := MaxWorkspaces()
	if active < limit {
		return nil
	}
	return fmt.Errorf(
		"workspace capacity reached: %d workspace(s) already running/starting/paused, limit %d (%s); "+
			"stop or delete an existing workspace, or raise the limit with %s=<n>",
		active, limit, source, MaxWorkspacesEnv,
	)
}
