//go:build !linux

package workspace

func observedEgressMediatorLive(_ Options, _ RuntimeState) (bool, bool, string) {
	return false, false, "egress mediator liveness is not observed by this backend"
}
