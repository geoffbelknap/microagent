//go:build linux

package firecracker

import "time"

const startupPollInitial = time.Millisecond

// nextStartupPollDelay keeps local process/socket startup responsive without
// spinning for the full failure timeout. Callers begin at startupPollInitial,
// then back off to the pre-existing polling interval supplied as max.
func nextStartupPollDelay(current, max time.Duration) time.Duration {
	if max <= 0 {
		return 0
	}
	if current <= 0 {
		if startupPollInitial < max {
			return startupPollInitial
		}
		return max
	}
	next := current * 2
	if next <= 0 || next > max {
		return max
	}
	return next
}
