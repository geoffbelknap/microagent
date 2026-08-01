package workspace

import (
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type Result struct {
	Workspace    string      `json:"workspace"`
	StateDir     string      `json:"state_dir"`
	Profile      string      `json:"profile,omitempty"`
	Restart      string      `json:"restart"`
	Resources    Resources   `json:"resources"`
	SizeDerived  bool        `json:"size_derived,omitempty"`
	Network      NetworkSpec `json:"network,omitempty"`
	Service      string      `json:"service_command,omitempty"`
	ConsoleShell string      `json:"shell,omitempty"`
	Hostname     string      `json:"hostname,omitempty"`
	RootfsPath   string      `json:"rootfs_path"`
	KernelPath   string      `json:"kernel_path"`
	Disks        []Disk      `json:"disks,omitempty"`
	Artifacts    Artifacts   `json:"artifacts,omitempty"`
	SerialPath   string      `json:"serial_path,omitempty"`
	// SerialLog is a bounded tail of the guest console log (see
	// Options.SerialLogMaxBytes); SerialLogBytes is the full log's size and
	// SerialLogTruncated marks a tail, so a consumer knows it is holding an
	// excerpt and where the rest lives (SerialPath, `microagent logs`).
	SerialLog          string `json:"serial_log,omitempty"`
	SerialLogBytes     int    `json:"serial_log_bytes,omitempty"`
	SerialLogTruncated bool   `json:"serial_log_truncated,omitempty"`
	FinalState         string `json:"final_state,omitempty"`
	// GuestCommand reports the command a dry run resolved, so validating a
	// configuration also shows what it would execute. Empty outside a dry run,
	// where the guest result carries what actually ran.
	GuestCommand string                     `json:"guest_command,omitempty"`
	Result       *GuestResult               `json:"result,omitempty"`
	Image        rootfs.Provenance          `json:"image"`
	Verification *vmkit.RuntimeVerification `json:"verification,omitempty"`
	Response     vmkit.Response             `json:"response"`
}

type GuestResult struct {
	StartedAt       string `json:"started_at"`
	ExitedAt        string `json:"exited_at"`
	ExitCode        int    `json:"exit_code"`
	Stdout          string `json:"stdout,omitempty"`
	Stderr          string `json:"stderr,omitempty"`
	StdoutTruncated bool   `json:"stdout_truncated,omitempty"`
	StderrTruncated bool   `json:"stderr_truncated,omitempty"`
	Error           string `json:"error,omitempty"`
	// StartError is non-empty exactly when the workload never ran — guest
	// setup failed, the command could not be resolved, or exec itself failed.
	// It carries guest-init's own diagnosis ("fork/exec /bin/sh: no such file
	// or directory"), which names the environment, never the workload's
	// output. Callers that attribute failures should branch on this before
	// reading ExitCode: a run with StartError set did not fail, it never
	// happened, and "fix your code and retry" is the wrong advice for it.
	StartError string `json:"start_error,omitempty"`
}

type EventFile struct {
	Identity   vmkit.Identity `json:"identity"`
	State      vmkit.VMState  `json:"state"`
	Detail     string         `json:"detail,omitempty"`
	ObservedAt string         `json:"observedAt"`
}

type RuntimeState struct {
	Event                  EventFile              `json:"event"`
	Config                 vmkit.Config           `json:"config"`
	PID                    int                    `json:"pid,omitempty"`
	ComputeSystemRuntimeID string                 `json:"computeSystemRuntimeID,omitempty"`
	VsockListenerPID       int                    `json:"vsockListenerPid,omitempty"`
	SerialLogPath          string                 `json:"serialLogPath"`
	SerialInputPath        string                 `json:"serialInputPath,omitempty"`
	StartedAt              string                 `json:"startedAt,omitempty"`
	UpdatedAt              string                 `json:"updatedAt"`
	Readiness              vmkit.RuntimeReadiness `json:"readiness,omitempty"`
	Error                  string                 `json:"error,omitempty"`
}

type ListEntry struct {
	Name       string `json:"name"`
	State      string `json:"state"`
	Backend    string `json:"backend,omitempty"`
	Profile    string `json:"profile,omitempty"`
	Restart    string `json:"restart,omitempty"`
	Network    string `json:"network,omitempty"`
	ObservedAt string `json:"observed_at,omitempty"`
	RootfsPath string `json:"rootfs_path,omitempty"`
	SerialPath string `json:"serial_path,omitempty"`
}
