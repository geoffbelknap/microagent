package vmkit

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"
)

func MediationReadinessSignal(ctx context.Context, mediation MediationConfig, state VMState, observedAt *time.Time, probeTimeout time.Duration) ReadinessSignal {
	detail := fmt.Sprintf("mediation required=%t failClosed=%t port=%d target=%s", mediation.Required, mediation.FailClosed, mediation.Port, mediation.Target)
	if state != StateRunning {
		signal := ReadinessSignal{
			Ready:      false,
			ObservedAt: observedAt,
			Detail:     detail + "; workspace is not running",
		}
		if mediation.Required {
			signal.Error = "required mediation is not ready"
		}
		return signal
	}
	observed := time.Now().UTC()
	target, err := mediationTCPAddr(mediation.Target)
	if err != nil {
		signal := ReadinessSignal{
			Ready:      false,
			ObservedAt: &observed,
			Detail:     detail + "; mediation target is not tcp host:port",
		}
		if mediation.Required {
			signal.Error = "required mediation target is invalid"
		}
		return signal
	}
	if probeTimeout <= 0 {
		probeTimeout = 150 * time.Millisecond
	}
	dialCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	start := time.Now()
	conn, err := (&net.Dialer{}).DialContext(dialCtx, "tcp", target)
	elapsed := time.Since(start)
	if err != nil {
		signal := ReadinessSignal{
			Ready:      false,
			ObservedAt: &observed,
			Detail:     fmt.Sprintf("%s; mediation target unreachable at %s after %s: %v", detail, target, elapsed.Round(time.Millisecond), err),
		}
		if mediation.Required {
			signal.Error = "required mediation target is unreachable"
		}
		return signal
	}
	_ = conn.Close()
	return ReadinessSignal{
		Ready:      true,
		ObservedAt: &observed,
		Detail:     fmt.Sprintf("%s; mediation target reachable at %s in %s", detail, target, elapsed.Round(time.Millisecond)),
	}
}

func mediationTCPAddr(target string) (string, error) {
	host, port, err := net.SplitHostPort(strings.TrimSpace(target))
	if err != nil || host == "" || port == "" {
		return "", fmt.Errorf("mediation target must be tcp host:port")
	}
	return net.JoinHostPort(host, port), nil
}
