package main

import (
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func TestEventFollowCompleteTreatsQuarantineAsTerminal(t *testing.T) {
	events := []workspace.EventFile{{State: vmkit.StateQuarantined}}
	if !eventFollowComplete(events) {
		t.Fatal("eventFollowComplete(quarantined) = false, want true after quarantine stops the runtime")
	}
}
