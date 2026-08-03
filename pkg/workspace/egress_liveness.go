package workspace

import (
	"fmt"
	"strings"
	"time"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const egressMediatorFailureDetail = "enforcement failure: "

func observeEgressCapture(opts Options, state RuntimeState, report *vmkit.EgressCaptureReport) {
	if report == nil || !report.MediatesAnyClass() {
		return
	}
	live, observed, detail := observedEgressMediatorLive(opts, state)
	report.LivenessDetail = detail
	if !observed {
		return
	}
	report.Live = &live
	if live || state.Event.State != vmkit.StateRunning {
		return
	}
	if err := recordEgressMediatorFailure(opts, state, detail); err != nil {
		report.LivenessDetail += "; persistent failure record could not be written: " + err.Error()
	}
}

func recordEgressMediatorFailure(opts Options, state RuntimeState, detail string) error {
	events, err := ReadEvents(opts.StateDir, opts.Name)
	if err == nil {
		needle := fmt.Sprintf("egress mediator process %d", state.EgressMediatorPID)
		for _, event := range events {
			if strings.HasPrefix(event.Detail, egressMediatorFailureDetail) && strings.Contains(event.Detail, needle) {
				return nil
			}
		}
	}
	return appendEvent(EventsPath(opts.StateDir, opts.Name), EventFile{
		Identity:   state.Event.Identity,
		State:      state.Event.State,
		Detail:     egressMediatorFailureDetail + detail,
		ObservedAt: time.Now().UTC().Format(time.RFC3339Nano),
	})
}
