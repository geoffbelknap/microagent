//go:build linux

package main

import (
	"os"
	"strings"
	"testing"
)

// withElevationStubs swaps the interactivity + elevation seams for the test and
// restores them afterward. Returns pointers to the recorded call state.
func withElevationStubs(t *testing.T) (*bool, *[]string) {
	t.Helper()
	oldTerm, oldConfirm, oldElevate, oldMode := stdinIsTerminal, readConfirmation, elevateWithSudo, globalOutputMode
	t.Cleanup(func() {
		stdinIsTerminal, readConfirmation, elevateWithSudo, globalOutputMode = oldTerm, oldConfirm, oldElevate, oldMode
	})
	globalOutputMode = outputModeUX
	called := false
	var gotArgs []string
	elevateWithSudo = func(_ string, args []string) error {
		called = true
		gotArgs = args
		return nil
	}
	return &called, &gotArgs
}

func TestSetupNetworkingConfirmThenElevates(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("asserts the non-root elevation path")
	}
	called, gotArgs := withElevationStubs(t)
	stdinIsTerminal = func() bool { return true }
	readConfirmation = func(string) (bool, error) { return true, nil }

	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	if err := runHostSetupNetworking(nil, f); err != nil {
		t.Fatalf("expected elevation via stub, got %v", err)
	}
	if !*called {
		t.Fatal("expected elevateWithSudo to be called after confirmation")
	}
	if strings.Join(*gotArgs, " ") != "host setup-networking" {
		t.Fatalf("child argv = %v, want [host setup-networking]", *gotArgs)
	}
}

func TestSetupNetworkingDeclineDoesNotElevate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("asserts the non-root elevation path")
	}
	called, _ := withElevationStubs(t)
	stdinIsTerminal = func() bool { return true }
	readConfirmation = func(string) (bool, error) { return false, nil }

	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	err := runHostSetupNetworking(nil, f)
	if err == nil || !strings.Contains(err.Error(), "cancelled") {
		t.Fatalf("err = %v, want cancellation", err)
	}
	if *called {
		t.Fatal("declined confirmation must not elevate")
	}
}

func TestSetupNetworkingYesSkipsPromptWithoutTTY(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("asserts the non-root elevation path")
	}
	called, _ := withElevationStubs(t)
	stdinIsTerminal = func() bool { return false }
	readConfirmation = func(string) (bool, error) {
		t.Fatal("--yes must not prompt")
		return false, nil
	}

	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	if err := runHostSetupNetworking([]string{"--yes"}, f); err != nil {
		t.Fatalf("err = %v, want elevation via stub", err)
	}
	if !*called {
		t.Fatal("--yes should elevate without prompting")
	}
}

func TestSetupNetworkingNonInteractiveRefuses(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("asserts the non-root elevation path")
	}
	called, _ := withElevationStubs(t)
	stdinIsTerminal = func() bool { return false }

	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	err := runHostSetupNetworking(nil, f)
	if err == nil || !strings.Contains(err.Error(), "host setup-networking") {
		t.Fatalf("err = %v, want manual-command instruction", err)
	}
	if *called {
		t.Fatal("non-TTY without --yes must not elevate")
	}
}

func TestSetupNetworkingAXModeDoesNotElevate(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("asserts the non-root elevation path")
	}
	called, _ := withElevationStubs(t)
	stdinIsTerminal = func() bool { return true }
	readConfirmation = func(string) (bool, error) { return true, nil }
	globalOutputMode = outputModeAX

	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	err := runHostSetupNetworking(nil, f)
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("err = %v, want structured requires-root error", err)
	}
	if *called {
		t.Fatal("AX mode must never self-elevate")
	}
}

func TestSetupNetworkingRevertForwardsFlag(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("asserts the non-root elevation path")
	}
	called, gotArgs := withElevationStubs(t)
	stdinIsTerminal = func() bool { return true }
	readConfirmation = func(string) (bool, error) { return true, nil }

	f, _ := os.CreateTemp(t.TempDir(), "out")
	defer f.Close()
	if err := runHostSetupNetworking([]string{"--revert"}, f); err != nil {
		t.Fatalf("err = %v", err)
	}
	if !*called {
		t.Fatal("expected elevation")
	}
	if strings.Join(*gotArgs, " ") != "host setup-networking --revert" {
		t.Fatalf("child argv = %v, want [host setup-networking --revert]", *gotArgs)
	}
}
