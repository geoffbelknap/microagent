package superviseunit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildSystemd(t *testing.T) {
	unit, err := Build(Options{Name: "research", ExecPath: "/usr/bin/microagent", StateDir: "/sd", Home: "/home/u", GOOS: "linux"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if unit.Label != "microagent-supervise-research.service" {
		t.Fatalf("label = %q", unit.Label)
	}
	if unit.Path != filepath.Join("/home/u", ".config", "systemd", "user", "microagent-supervise-research.service") {
		t.Fatalf("path = %q", unit.Path)
	}
	if !strings.Contains(unit.Content, "ExecStart=/usr/bin/microagent supervise research --state-dir /sd") {
		t.Fatalf("content missing ExecStart: %s", unit.Content)
	}
	if !strings.Contains(unit.Content, "WantedBy=default.target") {
		t.Fatalf("content missing install section: %s", unit.Content)
	}
	if strings.Join(unit.EnableArgs, " ") != "systemctl --user enable --now microagent-supervise-research.service" {
		t.Fatalf("enable args = %v", unit.EnableArgs)
	}
}

func TestBuildLaunchd(t *testing.T) {
	unit, err := Build(Options{Name: "svc", ExecPath: "/opt/microagent", StateDir: "", Home: "/Users/u", GOOS: "darwin"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if unit.Label != "com.microagent.supervise.svc" {
		t.Fatalf("label = %q", unit.Label)
	}
	if unit.Path != filepath.Join("/Users/u", "Library", "LaunchAgents", "com.microagent.supervise.svc.plist") {
		t.Fatalf("path = %q", unit.Path)
	}
	for _, want := range []string{"<string>/opt/microagent</string>", "<string>supervise</string>", "<string>svc</string>", "<key>RunAtLoad</key>"} {
		if !strings.Contains(unit.Content, want) {
			t.Fatalf("plist missing %q: %s", want, unit.Content)
		}
	}
	// No state dir → no --state-dir argument.
	if strings.Contains(unit.Content, "--state-dir") {
		t.Fatalf("plist should omit --state-dir when StateDir empty: %s", unit.Content)
	}
	if strings.Join(unit.DisableArgs, " ") != "launchctl unload -w "+unit.Path {
		t.Fatalf("disable args = %v", unit.DisableArgs)
	}
}

func TestBuildRejectsBadInput(t *testing.T) {
	if _, err := Build(Options{ExecPath: "/x", Home: "/h", GOOS: "linux"}); err == nil {
		t.Error("empty name should error")
	}
	if _, err := Build(Options{Name: "a", Home: "/h", GOOS: "linux"}); err == nil {
		t.Error("empty exec path should error")
	}
	if _, err := Build(Options{Name: "a", ExecPath: "/x", Home: "/h", GOOS: "plan9"}); err == nil {
		t.Error("unsupported GOOS should error")
	}
}

func TestInstallWritesUnitFile(t *testing.T) {
	home := t.TempDir()
	unit, err := Build(Options{Name: "demo", ExecPath: "/usr/bin/microagent", Home: home, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	// No EnableArgs side effect to assert on portably; clear them so Install
	// only does the file write (the part we can verify in any environment).
	unit.EnableArgs = nil
	if _, err := Install(unit); err != nil {
		t.Fatalf("Install: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "systemd", "user", "microagent-supervise-demo.service"))
	if err != nil {
		t.Fatalf("unit file not written: %v", err)
	}
	if !strings.Contains(string(data), "supervise demo") {
		t.Fatalf("unit content wrong: %s", data)
	}

	// Uninstall removes it (disable args cleared to avoid invoking systemctl).
	unit.DisableArgs = nil
	if err := Uninstall(unit); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	if _, err := os.Stat(unit.Path); !os.IsNotExist(err) {
		t.Fatalf("unit file should be removed: %v", err)
	}
}
