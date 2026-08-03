package workspace

import (
	"os"
	"strings"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/operation"
	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func hazardousCompositionOptions() Options {
	return Options{
		Secrets:    map[string]string{"token": "env:TOKEN"},
		Files:      []File{{SourcePath: "input.txt", Path: "/input.txt"}},
		Network:    vmkit.NetworkConfig{Mode: "user"},
		EgressMode: vmkit.EgressModeOff,
	}
}

func TestCapabilityCompositionRequiresRecordedAcknowledgement(t *testing.T) {
	opts := hazardousCompositionOptions()
	report := EvaluateCapabilityComposition(opts)
	if report.Invariant != capabilityCompositionInvariant || report.Acknowledged {
		t.Fatalf("report = %#v", report)
	}
	if err := validateCapabilityComposition(opts); err == nil || !operation.IsKind(err, operation.ErrorPolicyDenied) {
		t.Fatalf("error = %v, want policy denial", err)
	}
	opts.CapabilityRiskAcknowledgement = "reviewed for this trusted import"
	if err := validateCapabilityComposition(opts); err != nil {
		t.Fatalf("acknowledged composition rejected: %v", err)
	}
	report = EvaluateCapabilityComposition(opts)
	if !report.Acknowledged || !strings.Contains(report.Acknowledgement, "trusted import") {
		t.Fatalf("acknowledgement not recorded: %#v", report)
	}
}

func TestCapabilityCompositionIsDeclarativeNotAHostBlocklist(t *testing.T) {
	for _, mutate := range []func(*Options){
		func(o *Options) { o.Secrets = nil },
		func(o *Options) { o.Files = nil },
		func(o *Options) { o.EgressMode = vmkit.EgressModeBroker },
		func(o *Options) { o.Network.Mode = "isolated" },
	} {
		opts := hazardousCompositionOptions()
		mutate(&opts)
		if err := validateCapabilityComposition(opts); err != nil {
			t.Errorf("non-matching capability set rejected: %v", err)
		}
	}
}

func TestCapabilityCompositionDoesNotTreatMediatedCredentialsAsGuestSecrets(t *testing.T) {
	opts := hazardousCompositionOptions()
	opts.Secrets = nil
	opts.Broker = &vmkit.BrokerConfig{Upstream: "https://api.example.test"}
	opts.EgressSwapConfigPath = "/operator/swap.yaml"
	if report := EvaluateCapabilityComposition(opts); report.Invariant != "" {
		t.Fatalf("host-side credentials produced guest composition finding: %#v", report)
	}
}

func TestCapabilityCompositionPersistsInManifest(t *testing.T) {
	opts := hazardousCompositionOptions()
	opts.Name = "composition"
	opts.StateDir = t.TempDir()
	opts.RestartPolicy = "no"
	opts.CapabilityRiskAcknowledgement = "reviewed trusted import"
	if err := WriteManifest(opts); err != nil {
		t.Fatal(err)
	}
	manifest, err := ReadManifest(opts.StateDir, opts.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !manifest.CapabilityComposition.Acknowledged || manifest.CapabilityComposition.Invariant != capabilityCompositionInvariant {
		t.Fatalf("manifest report = %#v", manifest.CapabilityComposition)
	}
	restored := OptionsFromManifest(DefaultOptions(), manifest)
	if restored.CapabilityRiskAcknowledgement != opts.CapabilityRiskAcknowledgement {
		t.Fatalf("restored acknowledgement = %q", restored.CapabilityRiskAcknowledgement)
	}
}

func TestCreateRejectsCapabilityCompositionBeforeSideEffects(t *testing.T) {
	stateDir := t.TempDir()
	opts := DefaultOptions()
	opts.Name = "composition-denied"
	opts.StateDir = stateDir
	opts.ImageRef = "docker.io/library/alpine:3.20"
	opts.DryRun = true
	opts.Secrets = hazardousCompositionOptions().Secrets
	opts.Files = hazardousCompositionOptions().Files
	opts.EgressMode = vmkit.EgressModeOff
	result, err := Create(t.Context(), opts)
	if err == nil || !operation.IsKind(err, operation.ErrorPolicyDenied) {
		t.Fatalf("Create error = %v, want policy denial", err)
	}
	if result.CapabilityComposition.Invariant != capabilityCompositionInvariant {
		t.Fatalf("structured denial = %#v", result.CapabilityComposition)
	}
	entries, readErr := os.ReadDir(stateDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("denied create wrote state: %v", entries)
	}
}
