package workspace

import (
	"fmt"
	"sort"
	"time"

	"github.com/geoffbelknap/microagent/pkg/secretxfer"
)

// IncidentReceipt is the self-contained, host-observed summary returned by
// quarantine. It identifies the affected runtime session and summarizes the
// effects recorded during that session without copying request bodies or
// secret values into the result.
type IncidentReceipt struct {
	RuntimeID       string             `json:"runtime_id"`
	SessionID       string             `json:"session_id,omitempty"`
	ObservedFrom    string             `json:"observed_from,omitempty"`
	ObservedThrough string             `json:"observed_through"`
	LifecycleEvents int                `json:"lifecycle_events"`
	Egress          EgressAuditSummary `json:"egress"`
	Broker          BrokerAuditSummary `json:"broker"`
	Secrets         SecretAuditSummary `json:"secrets"`
	Complete        bool               `json:"complete"`
	Errors          []string           `json:"errors,omitempty"`
}

// BrokerAuditSummary reports brokered effects and the names (never values) of
// secret references involved in them.
type BrokerAuditSummary struct {
	RequestCount int            `json:"request_count"`
	AllowByHost  map[string]int `json:"allow_by_host,omitempty"`
	DenyByHost   map[string]int `json:"deny_by_host,omitempty"`
	BytesOut     int64          `json:"bytes_out"`
	BytesIn      int64          `json:"bytes_in"`
	SecretRefs   []string       `json:"secret_refs,omitempty"`
}

// SecretAuditSummary reports secret names and access outcomes. Secret values
// cannot appear because the underlying access-record schema never stores them.
type SecretAuditSummary struct {
	AccessCount int            `json:"access_count"`
	ByName      map[string]int `json:"by_name,omitempty"`
	ByResult    map[string]int `json:"by_result,omitempty"`
}

func buildIncidentReceipt(stateDir, name, sessionID, observedFrom string, now time.Time) IncidentReceipt {
	receipt := IncidentReceipt{
		RuntimeID: name, SessionID: sessionID, ObservedFrom: observedFrom,
		ObservedThrough: now.UTC().Format(time.RFC3339Nano), Complete: true,
	}
	if sessionID == "" {
		receipt.addError("identity", fmt.Errorf("runtime session is unavailable"))
	}
	lifecycle, err := ReadEvents(stateDir, name)
	if err != nil {
		receipt.addError("lifecycle", err)
	} else {
		for _, event := range lifecycle {
			if sameIncidentSession(event.Identity.SessionID, sessionID) {
				receipt.LifecycleEvents++
			}
		}
	}
	egress, err := ReadEgressAudit(stateDir, name)
	if err != nil {
		receipt.addError("egress", err)
	} else {
		receipt.Egress = SummarizeEgressAudit(filterSessionEvents(egress, sessionID))
	}
	brokerEvents, err := ReadBrokerAccess(stateDir, name)
	if err != nil {
		receipt.addError("broker", err)
	} else {
		receipt.Broker = summarizeBrokerAudit(filterSessionEvents(brokerEvents, sessionID))
	}
	secretEvents, err := secretxfer.ReadAccessRecords(secretxfer.AccessLogPath(stateDir, name))
	if err != nil {
		receipt.addError("secrets", err)
	} else {
		receipt.Secrets = summarizeSecretAudit(secretEvents, sessionID)
	}
	return receipt
}

func (r *IncidentReceipt) addError(stream string, err error) {
	r.Complete = false
	r.Errors = append(r.Errors, fmt.Sprintf("%s: %v", stream, err))
}

func sameIncidentSession(recordSession, incidentSession string) bool {
	return incidentSession != "" && recordSession == incidentSession
}

func filterSessionEvents(events []EgressEvent, sessionID string) []EgressEvent {
	filtered := make([]EgressEvent, 0, len(events))
	for _, event := range events {
		if sameIncidentSession(event.SessionID, sessionID) {
			filtered = append(filtered, event)
		}
	}
	return filtered
}

func summarizeBrokerAudit(events []EgressEvent) BrokerAuditSummary {
	summary := BrokerAuditSummary{}
	allow, deny, refs := map[string]int{}, map[string]int{}, map[string]struct{}{}
	for _, event := range events {
		summary.RequestCount++
		host := event.Host
		if host == "" {
			host = event.Dst
		}
		if host != "" {
			switch stringValue(event.Raw["verdict"]) {
			case "allow":
				allow[host]++
			case "deny":
				deny[host]++
			}
		}
		summary.BytesOut += int64Value(event.Raw["bytes_out"])
		summary.BytesIn += int64Value(event.Raw["bytes_in"])
		if values, ok := event.Raw["secret_refs"].([]any); ok {
			for _, value := range values {
				if ref := stringValue(value); ref != "" {
					refs[ref] = struct{}{}
				}
			}
		}
	}
	if len(allow) > 0 {
		summary.AllowByHost = allow
	}
	if len(deny) > 0 {
		summary.DenyByHost = deny
	}
	for ref := range refs {
		summary.SecretRefs = append(summary.SecretRefs, ref)
	}
	sort.Strings(summary.SecretRefs)
	return summary
}

func summarizeSecretAudit(events []secretxfer.AccessRecord, sessionID string) SecretAuditSummary {
	summary := SecretAuditSummary{}
	byName, byResult := map[string]int{}, map[string]int{}
	for _, event := range events {
		if !sameIncidentSession(event.SessionID, sessionID) {
			continue
		}
		summary.AccessCount++
		byName[event.Name]++
		byResult[event.Result]++
	}
	if len(byName) > 0 {
		summary.ByName = byName
	}
	if len(byResult) > 0 {
		summary.ByResult = byResult
	}
	return summary
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func int64Value(value any) int64 {
	switch number := value.(type) {
	case float64:
		return int64(number)
	case int64:
		return number
	case int:
		return int64(number)
	default:
		return 0
	}
}
