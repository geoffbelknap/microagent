package main

import (
	"fmt"
	"strings"

	"github.com/geoffbelknap/microagent/pkg/workspace"
)

func summarizeWorkspaceInspect(result any, stateDir, name string) any {
	resp, ok := result.(map[string]any)
	if !ok {
		return result
	}
	summary := map[string]any{
		"format":    "summary",
		"ok":        resp["ok"],
		"backend":   resp["backend"],
		"error":     resp["error"],
		"error_cnt": 0,
	}
	if text, ok := resp["error"].(string); ok && strings.TrimSpace(text) != "" {
		summary["error_cnt"] = 1
	}
	if event, ok := resp["event"].(map[string]any); ok {
		summary["state"] = event["state"]
		if identity, ok := event["identity"].(map[string]any); ok {
			summary["workspace"] = identity["runtimeID"]
			if name == "" {
				if id, ok := identity["runtimeID"].(string); ok {
					name = id
				}
			}
		}
	}
	if eg := egressSummary(stateDir, name); eg != nil {
		summary["egress_summary"] = eg
	}
	summary["next_decision_points"] = workspaceNextDecisionPoints(fmt.Sprint(summary["state"]))
	return summary
}

// egressSummary reads the egress mediator's audit log and folds it into a
// compact overview: a total decision count, a count for each event type, and a
// per-host allow-vs-deny tally. It returns nil when the audit log is absent or
// empty (mediation off / no decision yet) so the inspect summary omits the
// egress_summary key cleanly rather than carrying an empty object. The counts
// stay generic over the mediator's open-ended event vocabulary — every event
// type the log contains is tallied under by_event, allow/deny are recognized by
// suffix so DNS/UDP allow/deny variants fold into the per-host verdict view.
func egressSummary(stateDir, name string) map[string]any {
	if name == "" {
		return nil
	}
	events, err := workspace.ReadEgressAudit(stateDir, name)
	if err != nil || len(events) == 0 {
		return nil
	}
	byEvent := map[string]int{}
	allowByHost := map[string]int{}
	denyByHost := map[string]int{}
	for _, ev := range events {
		byEvent[ev.Event]++
		host := ev.Host
		if host == "" {
			host = ev.Dst
		}
		if host == "" {
			continue
		}
		switch {
		case strings.HasSuffix(ev.Event, "_allow"):
			allowByHost[host]++
		case strings.HasSuffix(ev.Event, "_deny"):
			denyByHost[host]++
		}
	}
	summary := map[string]any{
		"decision_count": len(events),
		"by_event":       byEvent,
	}
	if len(allowByHost) > 0 {
		summary["allow_by_host"] = allowByHost
	}
	if len(denyByHost) > 0 {
		summary["deny_by_host"] = denyByHost
	}
	return summary
}

func summarizeWorkspaceLifecycle(result any, outcome string) any {
	resp, ok := result.(map[string]any)
	if !ok {
		return result
	}
	response, _ := resp["response"].(map[string]any)
	summary := map[string]any{
		"format":    "summary",
		"outcome":   outcome,
		"ok":        response["ok"],
		"backend":   response["backend"],
		"workspace": resp["workspace"],
		"state":     resp["final_state"],
		"error":     response["error"],
		"error_cnt": 0,
	}
	if text, ok := response["error"].(string); ok && strings.TrimSpace(text) != "" {
		summary["error_cnt"] = 1
	}
	if event, ok := response["event"].(map[string]any); ok {
		summary["state"] = event["state"]
		if identity, ok := event["identity"].(map[string]any); ok && summary["workspace"] == nil {
			summary["workspace"] = identity["runtimeID"]
		}
		if detail, ok := event["detail"].(string); ok && strings.TrimSpace(detail) != "" {
			summary["detail"] = detail
		}
	}
	if summary["ok"] == true && outcome == "created" && fmt.Sprint(summary["state"]) == "stopped" {
		summary["ready"] = true
		summary["state_meaning"] = "created and ready to start"
	}
	if rootfs, ok := resp["rootfs_path"].(string); ok && strings.TrimSpace(rootfs) != "" {
		summary["rootfs_path"] = rootfs
	}
	summary["next_decision_points"] = workspaceNextDecisionPoints(fmt.Sprint(summary["state"]))
	return summary
}

func workspaceNextDecisionPoints(state string) []string {
	switch state {
	case "running", "starting":
		return []string{"workspace.exec", "workspace.halt", "workspace.delete"}
	case "prepared", "halted", "stopped":
		return []string{"workspace.start", "workspace.delete"}
	case "failed", "quarantined":
		return []string{"workspace.inspect", "workspace.delete"}
	default:
		return []string{"workspace.inspect"}
	}
}

func summarizeWorkspaceLogs(result any, tailLimit int) any {
	resp, ok := result.(map[string]any)
	if !ok {
		return result
	}
	if tailLimit <= 0 {
		tailLimit = 8
	}
	logs, _ := resp["logs"].(string)
	lines := strings.Split(strings.TrimRight(logs, "\n"), "\n")
	if len(lines) == 1 && lines[0] == "" {
		lines = nil
	}
	tail := lines
	if len(tail) > tailLimit {
		tail = tail[len(tail)-tailLimit:]
	}
	return map[string]any{
		"format":      "summary",
		"workspace":   resp["workspace"],
		"byte_count":  len(logs),
		"line_count":  len(lines),
		"tail_count":  len(tail),
		"tail_lines":  tail,
		"full_output": "call workspace.logs with format=full to retrieve the complete serial log buffer",
	}
}

func summarizeWorkspaceEvents(result any, limit int, afterIndex int) any {
	resp, ok := result.(map[string]any)
	if !ok {
		return result
	}
	if limit <= 0 {
		limit = 5
	}
	events, _ := resp["events"].([]any)
	startIndex := 0
	if afterIndex > 0 && afterIndex < len(events) {
		startIndex = afterIndex
	}
	recent := events[startIndex:]
	if len(recent) > limit {
		recent = recent[len(recent)-limit:]
	}
	summary := map[string]any{
		"format":             "summary",
		"workspace":          resp["workspace"],
		"event_count":        len(events),
		"after_index":        afterIndex,
		"next_after_index":   len(events),
		"returned_count":     len(recent),
		"recent":             recent,
		"full_output":        "call workspace.events with format=full to retrieve all lifecycle events",
		"polling_contract":   "pass next_after_index as after_index on the next call to fetch newer events without a long-running follow call",
		"truncated_by_limit": len(events[startIndex:]) > len(recent),
	}
	if len(events) > 0 {
		if latest, ok := events[len(events)-1].(map[string]any); ok {
			summary["latest_state"] = latest["state"]
			summary["latest_observed_at"] = latest["observedAt"]
			summary["latest_detail"] = latest["detail"]
		}
	}
	return summary
}
