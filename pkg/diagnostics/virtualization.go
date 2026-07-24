package diagnostics

import "strings"

// cpuHasVirtualizationFlags reports whether the CPU advertises hardware
// virtualization (Intel vmx or AMD svm) in /proc/cpuinfo. It is a best-effort
// x86 signal used to distinguish "the CPU cannot virtualize" from "/dev/kvm is
// not set up": on arm64 (and when cpuinfo is unreadable) it returns false, and
// callers fall back to KVM availability as proof that virtualization works.
func cpuHasVirtualizationFlags(readFile func(string) ([]byte, error)) bool {
	if readFile == nil {
		return false
	}
	data, err := readFile("/proc/cpuinfo")
	if err != nil {
		return false
	}
	for _, line := range strings.Split(string(data), "\n") {
		lower := strings.ToLower(line)
		// x86 uses "flags"; some kernels label the field "features".
		if !strings.HasPrefix(lower, "flags") && !strings.HasPrefix(lower, "features") {
			continue
		}
		for _, field := range strings.Fields(lower) {
			if field == "vmx" || field == "svm" {
				return true
			}
		}
	}
	return false
}
