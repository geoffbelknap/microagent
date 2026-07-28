//go:build !linux

package diagnostics

// defaultTProxyProbe is nil off Linux: TPROXY steering is a linux-kvm
// concern, and the derivation treats a nil probe as "decide from the module
// heuristic", which off Linux never runs either.
var defaultTProxyProbe func(supervisorPath string) (ran bool, err error)
