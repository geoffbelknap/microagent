package main

import (
	"strings"
	"testing"
)

// TestRunPreservesGuestCommandFlags proves run/dispatch do NOT hoist flags that
// belong to the guest command (everything after the IMAGE positional) into
// microagent's own flags. Guest flags like -e/-f/-p collide with microagent
// flag names and were being stolen out of the guest command.
func TestRunPreservesGuestCommandFlags(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		image string
		want  []string // tokens that must survive in the guest ExecCommand
	}{
		{"grep -e", []string{"alpine", "grep", "-e", "foo", "file"}, "alpine", []string{"grep", "-e", "foo", "file"}},
		{"tar -f", []string{"alpine", "tar", "-f", "x.tar"}, "alpine", []string{"tar", "-f", "x.tar"}},
		{"ls -p (publish collision)", []string{"alpine", "ls", "-p"}, "alpine", []string{"ls", "-p"}},
		{"sh -c with -v", []string{"alpine", "sh", "-c", "grep -v x f"}, "alpine", []string{"sh", "-c", "grep -v x f"}},
	}
	for _, tc := range cases {
		for _, cmd := range []string{"run", "dispatch"} {
			t.Run(cmd+"/"+tc.name, func(t *testing.T) {
				opts, err := parseWorkspaceOptions(cmd, tc.args)
				if err != nil {
					t.Fatalf("parseWorkspaceOptions(%q, %v): %v", cmd, tc.args, err)
				}
				if opts.ImageRef != tc.image {
					t.Fatalf("ImageRef = %q, want %q", opts.ImageRef, tc.image)
				}
				for _, tok := range tc.want {
					if !strings.Contains(opts.ExecCommand, tok) {
						t.Fatalf("ExecCommand = %q, want it to contain guest token %q", opts.ExecCommand, tok)
					}
				}
			})
		}
	}
}

// TestRunStillParsesFlagsBeforeImage proves the fix does not regress microagent
// flags that legitimately precede the IMAGE: they are still consumed, not left
// in the guest command.
func TestRunStillParsesFlagsBeforeImage(t *testing.T) {
	opts, err := parseWorkspaceOptions("run", []string{"--rm", "--env", "A=B", "alpine", "grep", "-e", "x"})
	if err != nil {
		t.Fatalf("parseWorkspaceOptions: %v", err)
	}
	if opts.ImageRef != "alpine" {
		t.Fatalf("ImageRef = %q, want alpine", opts.ImageRef)
	}
	if strings.Contains(opts.ExecCommand, "A=B") {
		t.Fatalf("ExecCommand = %q, --env before the image leaked into the guest command", opts.ExecCommand)
	}
	if !strings.Contains(opts.ExecCommand, "grep") || !strings.Contains(opts.ExecCommand, "-e") {
		t.Fatalf("ExecCommand = %q, want guest 'grep -e' preserved", opts.ExecCommand)
	}
}
