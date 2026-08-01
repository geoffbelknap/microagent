package workspace

import (
	"github.com/geoffbelknap/microagent/internal/ext4fs"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/volume"
)

// VolumeDiskUsage measures a named volume's backing image the same way the
// rootfs is measured on inspect. Nil when the volume image is missing or
// unreadable — usage is advisory, never an error.
func VolumeDiskUsage(stateDir, backend, name string) *vmkit.DiskUsage {
	return diskUsageForImage(volume.DiskPath(stateDir, backend, name))
}

// rootfsUsage measures the workspace rootfs image, returning nil when the
// image is missing or unreadable — usage is advisory context on inspect and
// status, never a reason for either to fail.
func rootfsUsage(opts Options) *vmkit.DiskUsage {
	return diskUsageForImage(WorkspaceRootfsPath(opts.StateDir, opts.Name, opts.Backend))
}

func diskUsageForImage(path string) *vmkit.DiskUsage {
	usage, err := ext4fs.ReadUsage(path)
	if err != nil {
		return nil
	}
	const mib = 1024 * 1024
	out := &vmkit.DiskUsage{
		SizeMiB:          usage.TotalBytes / mib,
		FSUsedMiB:        usage.UsedBytes / mib,
		FSFreeMiB:        usage.FreeBytes / mib,
		HostAllocatedMiB: usage.HostAllocatedBytes / mib,
	}
	if usage.TotalBytes > 0 {
		out.UsedPercent = int((usage.UsedBytes*100 + usage.TotalBytes/2) / usage.TotalBytes)
	}
	out.Assessment = assessDiskUsage(out)
	return out
}

// assessDiskUsage turns the numbers into the advisory verdict. Thresholds:
// a disk of 4 GiB or more using under 15% is overprovisioned (resize or
// rebuild smaller); 90% or more used is nearly full (resize or clean up).
// The wide middle stays unjudged.
func assessDiskUsage(usage *vmkit.DiskUsage) string {
	switch {
	case usage.UsedPercent >= 90:
		return vmkit.DiskAssessmentNearlyFull
	case usage.SizeMiB >= 4096 && usage.UsedPercent < 15:
		return vmkit.DiskAssessmentOverprovisioned
	}
	return ""
}
