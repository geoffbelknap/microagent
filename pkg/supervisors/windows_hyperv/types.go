package windows_hyperv

import (
	"context"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

type Options struct {
	Name     string
	StateDir string
}

type Supervisor struct {
	Options Options
	adapter runtimeAdapter
}

type runtimeAdapter interface {
	Host(ctx context.Context) (vmkit.HostSupport, error)
	Check(ctx context.Context) error
	PrepareNetwork(ctx context.Context, spec computeSystemSpec) (networkAttachment, error)
	CleanupNetwork(ctx context.Context, state runtimeState) error
	Create(ctx context.Context, spec computeSystemSpec) (computeSystemHandle, error)
	Start(ctx context.Context, id string) error
	Shutdown(ctx context.Context, id string) error
	Kill(ctx context.Context, id string) error
	Delete(ctx context.Context, id string) error
	// Pause freezes a running compute system's vCPUs in place; Resume thaws
	// them. Both preserve the compute system registration, its memory, and its
	// disk — only execution is suspended.
	Pause(ctx context.Context, id string) error
	Resume(ctx context.Context, id string) error
	// Save writes a paused compute system's guest memory and device state to a
	// save-state file. The system must already be paused. saveType selects the
	// save flavor ("ToFile" for a plain restorable save).
	Save(ctx context.Context, id, stateFilePath, saveType string) error
	// GrantAccess adds an ACE for the compute system's VM worker identity to a
	// host path, so HCS can write the save-state file there (the worker process
	// runs under a VM-specific SID and cannot write an arbitrary user directory
	// by default). vmID is the compute system runtime ID.
	GrantAccess(ctx context.Context, vmID, path string) error
	Wait(ctx context.Context, id string) error
	// Exists reports whether the compute system is still registered with HCS.
	// A guest that exits on its own takes its compute system with it, so this
	// is the liveness probe behind honest inspect state.
	Exists(ctx context.Context, id string) (bool, error)
}

type computeSystemSpec struct {
	Name              string
	StateDir          string
	Identity          vmkit.Identity
	Config            vmkit.Config
	NetworkID         string
	NetworkEndpointID string
	// RestoreStateFilePath, when set, makes Create build a VirtualMachine with
	// VirtualMachine.RestoreState pointing at this save-state file so the
	// compute system boots from a saved snapshot rather than cold-booting the
	// kernel. It is the absolute host path of the snapshot's vmstate file.
	RestoreStateFilePath string
}

type computeSystemHandle struct {
	ID                string
	RuntimeID         string
	NetworkID         string
	NetworkEndpointID string
	RuntimeNetwork    *vmkit.NetworkConfig
}

type networkAttachment struct {
	NetworkID         string
	NetworkEndpointID string
	RuntimeNetwork    *vmkit.NetworkConfig
}

type runtimeListenerSet interface {
	Wait(ctx context.Context) error
	Close() error
}

type hcsClient interface {
	CreateComputeSystem(ctx context.Context, id string, document []byte) (computeSystemHandle, error)
	GrantVMAccess(ctx context.Context, vmID, path string) error
	StartComputeSystem(ctx context.Context, id string) error
	ShutdownComputeSystem(ctx context.Context, id string) error
	PauseComputeSystem(ctx context.Context, id string) error
	ResumeComputeSystem(ctx context.Context, id string) error
	SaveComputeSystem(ctx context.Context, id, stateFilePath, saveType string) error
	KillComputeSystem(ctx context.Context, id string) error
	DeleteComputeSystem(ctx context.Context, id string) error
	WaitComputeSystem(ctx context.Context, id string) error
	ProbeComputeSystem(ctx context.Context, id string) error
	GetComputeSystemStatistics(ctx context.Context, id string) (string, error)
	// DescribeComputeSystem returns the base HCS properties document for a
	// compute system (its State in particular) for teardown diagnostics.
	DescribeComputeSystem(ctx context.Context, id string) (string, error)
}

func (s Supervisor) runtimeAdapter() runtimeAdapter {
	if s.adapter != nil {
		return s.adapter
	}
	return defaultAdapter{}
}
