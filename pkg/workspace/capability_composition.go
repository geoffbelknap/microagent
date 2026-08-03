package workspace

import (
	"strings"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

const capabilityCompositionInvariant = "private-data+external-content+unmediated-outbound"

// CapabilityComposition is the library-owned, backend-neutral summary of the
// complete grant set. Categories are derived from semantics, not option names.
type CapabilityComposition struct {
	Categories      []string `json:"categories"`
	Sources         []string `json:"sources,omitempty"`
	Invariant       string   `json:"invariant,omitempty"`
	Acknowledged    bool     `json:"acknowledged"`
	Acknowledgement string   `json:"acknowledgement,omitempty"`
}

// EvaluateCapabilityComposition classifies the effective workspace options as
// a set. Broker and credential-swap references are deliberately absent from
// private-data: their real values remain host-side and their use is mediated.
func EvaluateCapabilityComposition(opts Options) CapabilityComposition {
	report := CapabilityComposition{}
	privateData := len(opts.Secrets) > 0 || len(opts.SecretEnvFiles) > 0 || len(opts.OnDemandSecrets) > 0
	externalContent := len(opts.Files) > 0 || len(opts.Disks) > 0
	unmediatedOutbound := strings.ToLower(strings.TrimSpace(opts.Network.Mode)) != "isolated" &&
		vmkit.ResolveEgressModeDefault(opts.EgressMode) == vmkit.EgressModeOff
	if privateData {
		report.Categories = append(report.Categories, "private-data")
		report.Sources = append(report.Sources, "guest-secrets")
	}
	if externalContent {
		report.Categories = append(report.Categories, "external-content")
		report.Sources = append(report.Sources, "injected-files-or-disks")
	}
	if vmkit.EgressMediationOn(vmkit.ResolveEgressModeDefault(opts.EgressMode)) && vmkit.NetworkModeMediates(opts.Network.Mode) {
		report.Categories = append(report.Categories, "mediated-outbound")
	}
	if unmediatedOutbound {
		report.Categories = append(report.Categories, "unmediated-outbound")
		report.Sources = append(report.Sources, "egress-off")
	}
	if privateData && externalContent && unmediatedOutbound {
		report.Invariant = capabilityCompositionInvariant
		report.Acknowledgement = strings.TrimSpace(opts.CapabilityRiskAcknowledgement)
		report.Acknowledged = report.Acknowledgement != ""
	}
	return report
}

func validateCapabilityComposition(opts Options) error {
	report := EvaluateCapabilityComposition(opts)
	if report.Invariant == "" || report.Acknowledged {
		return nil
	}
	return operation.New(operation.ErrorPolicyDenied,
		"capability composition %q requires an acknowledgement: provide --acknowledge-capability-risk with the operator's reason, or use mediated egress", report.Invariant)
}
