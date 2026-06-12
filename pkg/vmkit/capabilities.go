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
}

// BackendCapabilities returns the capability table entry for a backend.
// Unknown backends return the zero value, which grants nothing.
func BackendCapabilities(backend string) Capabilities {
	switch backend {
	case BackendFirecracker:
		return Capabilities{
			StructuredExec:       true,
			LiveNetworkApply:     true,
			OwnsRuntimeState:     true,
			DetachedStartCommand: "start",
			ShellNetwork:         "tcp",
			ShellReadinessProbe:  true,
		}
	case BackendAppleVF:
		return Capabilities{
			StructuredExec:         true,
			LiveNetworkApply:       true,
			DetachedStartCommand:   "run",
			DetachedHostSupervisor: true,
			ShellNetwork:           "tcp",
			ShellReadinessProbe:    true,
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
		}
	default:
		return Capabilities{}
	}
}
