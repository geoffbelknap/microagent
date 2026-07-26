package workspace

import (
	"github.com/geoffbelknap/microagent/pkg/rootfs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type Result struct {
	Workspace    string                     `json:"workspace"`
	StateDir     string                     `json:"state_dir"`
	Profile      string                     `json:"profile,omitempty"`
	Restart      string                     `json:"restart"`
	Resources    Resources                  `json:"resources"`
	Network      NetworkSpec                `json:"network,omitempty"`
	Service      string                     `json:"service_command,omitempty"`
	ConsoleShell string                     `json:"shell,omitempty"`
	Hostname     string                     `json:"hostname,omitempty"`
	RootfsPath   string                     `json:"rootfs_path"`
	KernelPath   string                     `json:"kernel_path"`
	Disks        []Disk                     `json:"disks,omitempty"`
	Artifacts    Artifacts                  `json:"artifacts,omitempty"`
	SerialPath   string                     `json:"serial_path,omitempty"`
	SerialLog    string                     `json:"serial_log,omitempty"`
	FinalState   string                     `json:"final_state,omitempty"`
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
