package workspace

import (
	"context"
	"os"
	"strings"
	"testing"
)

// depthProbeOptions reuses the existing dryRunOptions fixture with the flag
// set, renamed target so failures read distinctly from the older suite's.
func depthProbeOptions(t *testing.T) Options {
	t.Helper()
	opts := dryRunOptions(t)
	opts.Name = "depth-probe"
	opts.DryRun = true
	return opts
}

// TestDryRunRejectsAMalformedImageRef pins the depth contract: a dry run may
// not bless a configuration whose real run fails before pulling a byte.
//
// The image-ref parse is pure and offline, yet it sat after the dry-run
// return, so `run --dry-run --image 'IN VALID//ref!!'` printed "dry run
// validated workspace config" for a config the real run rejects at its first
// parse. The docs say dry-run validates the configuration; a validation
// shallower than the real path's own offline checks makes that claim false in
// exactly the case the flag exists for.
func TestDryRunRejectsAMalformedImageRef(t *testing.T) {
	for _, entry := range []struct {
		name string
		call func(Options) (Result, error)
	}{
		{"create", func(o Options) (Result, error) { return Create(context.Background(), o) }},
		{"run", func(o Options) (Result, error) { return Run(context.Background(), o) }},
	} {
		t.Run(entry.name, func(t *testing.T) {
			opts := depthProbeOptions(t)
			opts.ImageRef = "IN VALID//ref!!"

			_, err := entry.call(opts)
			if err == nil {
				t.Fatal("dry run blessed a ref the real build's first parse refuses")
			}
			if !strings.Contains(err.Error(), "parse OCI image ref") {
				t.Errorf("rejection is not the builder's own parse error: %v", err)
			}
		})
	}
}

// TestRealRunRejectsTheRefBeforeSideEffects is the other half: the check runs
// on the real path too, so a doomed config now fails before EnsureKernel can
// download anything for it. The state dir staying empty is the evidence.
func TestRealRunRejectsTheRefBeforeSideEffects(t *testing.T) {
	opts := depthProbeOptions(t)
	opts.DryRun = false
	opts.ImageRef = "IN VALID//ref!!"

	_, err := Create(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "parse OCI image ref") {
		t.Fatalf("real create did not fail on the parse: %v", err)
	}
	if entries := readDirNames(t, opts.StateDir); len(entries) != 0 {
		t.Errorf("rejected config still wrote state: %v", entries)
	}
}

// TestDryRunStillAcceptsAValidRef is the control: deepening validation must
// not start rejecting the configs the dry run exists to bless.
func TestDryRunStillAcceptsAValidRef(t *testing.T) {
	opts := depthProbeOptions(t)
	opts.ImageRef = "docker.io/library/python:3.13-slim"

	res, err := Create(context.Background(), opts)
	if err != nil {
		t.Fatalf("dry run rejected a valid config: %v", err)
	}
	if res.Workspace != "depth-probe" {
		t.Errorf("prepared result names %q", res.Workspace)
	}
	if entries := readDirNames(t, opts.StateDir); len(entries) != 0 {
		t.Errorf("dry run wrote state: %v", entries)
	}
}

// readDirNames lists a directory, tolerating its absence — an absent state dir
// counts as "nothing written".
func readDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
