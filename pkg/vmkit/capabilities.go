package vmkit

// Capabilities describes the backend-specific behavior the workspace layer
// needs to know about, so backend differences live in one declarative table
// instead of scattered conditionals. The zero value grants nothing: an
// unknown backend fails closed.
type Capabilities struct {
	// StructuredExec reports whether the guest structured exec service is
	// reachable from the host, enabling exec health probes.
	StructuredExec bool
	// LiveNetworkApply reports whether host-bind port-forward changes can
	// be applied to a running workspace without a restart.
	LiveNetworkApply bool
	// VHDRootfs selects the VHD rootfs and disk format instead of ext4.
	VHDRootfs bool
	// OwnsRuntimeState reports whether the backend supervisor maintains
	// runtime state itself, so the workspace layer must not overwrite it
	// around detached starts.
	OwnsRuntimeState bool
	// DetachedStartCommand is the supervisor command used for detached
	// starts: "start" when the supervisor backgrounds itself, "run" when
	// the host keeps a foreground supervisor process.
	DetachedStartCommand string
	// DetachedHostSupervisor reports whether a detached start spawns a
	// long-lived supervisor process on the host (Apple VF).
	DetachedHostSupervisor bool
	// ShellNetwork is the transport for the guest shell console: "tcp" or
	// "hvsock".
	ShellNetwork string
	// ShellReadinessProbe reports whether shell readiness can be probed
	// from the host over ShellNetwork.
	ShellReadinessProbe bool
	// SCSIBlockDevices reports whether guest disks enumerate as SCSI
	// devices (/dev/sdX) instead of virtio (/dev/vdX).
	SCSIBlockDevices bool
	// GuestMediatedCopy reports that the host cannot read the workspace
	// filesystem directly (no ext4 tooling for the disk format) and copy,
	// artifact extraction, and commit ride the guest's structured exec
	// channel instead, using a transient maintenance boot for stopped
	// workspaces.
	GuestMediatedCopy bool
	// Snapshot reports whether the backend can checkpoint a workspace's guest
	// memory + device state to disk (snapshot create/restore/fork). windows-hyperv
	// cannot: its HCS-direct (LinuxKernelDirect) compute systems have no
	// guest-memory save-state; HcsSaveComputeSystem captures only device
	// state, and the working Hyper-V mechanisms (Save-VM/checkpoints) belong
	// to VMMS, which the HCS-direct backend deliberately does not use.
	Snapshot bool
	// PauseResume reports whether the backend can freeze and thaw a running
	// workspace in place. Snapshot remains the aggregate full-feature capability.
	PauseResume bool
	// SnapshotCreate reports whether the backend can create a snapshot artifact.
	// Restore/fork support is still represented by Snapshot until split out and
	// validated end-to-end.
	SnapshotCreate bool
}

// BackendCapabilities returns the capability table entry for a backend.
// Unknown backends return the zero value, which grants nothing.
func BackendCapabilities(backend string) Capabilities {
	switch backend {
	case BackendLinuxKVM:
		return Capabilities{
			StructuredExec:       true,
			LiveNetworkApply:     true,
			OwnsRuntimeState:     true,
			DetachedStartCommand: "start",
			ShellNetwork:         "tcp",
			ShellReadinessProbe:  true,
			Snapshot:             true,
			PauseResume:          true,
			SnapshotCreate:       true,
		}
	case BackendAppleVF:
		return Capabilities{
			StructuredExec:         true,
			LiveNetworkApply:       true,
			DetachedStartCommand:   "run",
			DetachedHostSupervisor: true,
			ShellNetwork:           "tcp",
			ShellReadinessProbe:    true,
			Snapshot:               true,
			PauseResume:            true,
			SnapshotCreate:         true,
		}
	case BackendWindowsHyperV:
		return Capabilities{
			StructuredExec:       true,
			LiveNetworkApply:     true,
			VHDRootfs:            true,
			OwnsRuntimeState:     true,
			DetachedStartCommand: "start",
			ShellNetwork:         "hvsock",
			ShellReadinessProbe:  true,
			SCSIBlockDevices:     true,
			GuestMediatedCopy:    true,
		}
	default:
		return Capabilities{}
	}
}
