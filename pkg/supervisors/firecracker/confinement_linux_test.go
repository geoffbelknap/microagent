package firecracker

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/geoffbelknap/microagent/pkg/vmkit"
)

func TestNormalizeConfinementKnob(t *testing.T) {
	cases := map[string]string{
		"":         confinementAuto,
		"   ":      confinementAuto,
		"AUTO":     confinementAuto,
		"auto":     confinementAuto,
		"off":      confinementOffKnob,
		" Off ":    confinementOffKnob,
		"Jailer":   confinementJailerKnob,
		"rootless": confinementRootlessKnob,
		"nonsense": confinementAuto,
	}
	for in, want := range cases {
		if got := normalizeConfinementKnob(in); got != want {
			t.Errorf("normalizeConfinementKnob(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSelectConfinementMode(t *testing.T) {
	cases := []struct {
		name          string
		knob          string
		euid          int
		userNSEnabled bool
		want          confinementMode
		wantErr       bool
	}{
		{"off is always off", confinementOffKnob, 0, true, confinementOff, false},
		{"jailer as root", confinementJailerKnob, 0, false, confinementJailer, false},
		{"jailer non-root fails closed", confinementJailerKnob, 1000, true, confinementOff, true},
		{"rootless with userns", confinementRootlessKnob, 1000, true, confinementRootless, false},
		{"rootless without userns fails closed", confinementRootlessKnob, 1000, false, confinementOff, true},
		{"auto+root -> jailer", confinementAuto, 0, false, confinementJailer, false},
		{"auto+userns -> rootless", confinementAuto, 1000, true, confinementRootless, false},
		{"auto, neither -> off", confinementAuto, 1000, false, confinementOff, false},
		{"unknown fails closed", "bogus", 0, true, confinementOff, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := selectConfinementMode(tc.knob, tc.euid, tc.userNSEnabled)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("mode = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestConfinedJailLayout(t *testing.T) {
	opts := Options{Name: "ws1", StateDir: "/state"}
	cfg := &vmkit.Config{
		KernelPath: "/images/vmlinux",
		RootfsPath: "/state/ws1/rootfs.ext4",
		Disks:      []vmkit.Disk{{Name: "data", Path: "/vol/data.img"}},
	}
	l := confinedJailLayout(opts, cfg, "/opt/firecracker")

	if l.Root != "/state/ws1/jail" {
		t.Fatalf("Root = %q, want /state/ws1/jail", l.Root)
	}
	checks := []struct {
		name string
		got  jailArtifact
		want jailArtifact
	}{
		{"kernel", l.Kernel, jailArtifact{Source: "/images/vmlinux", Host: "/state/ws1/jail/kernel", Guest: "/kernel"}},
		{"rootfs", l.Rootfs, jailArtifact{ID: "rootfs", Source: "/state/ws1/rootfs.ext4", Host: "/state/ws1/jail/rootfs.ext4", Guest: "/rootfs.ext4"}},
		{"config", l.ConfigFile, jailArtifact{Host: "/state/ws1/jail/firecracker.json", Guest: "/firecracker.json"}},
		{"api", l.APISocket, jailArtifact{Host: "/state/ws1/jail/run/firecracker-api.sock", Guest: "/run/firecracker-api.sock"}},
		{"vsock", l.VsockUDS, jailArtifact{Host: "/state/ws1/jail/run/vsock.sock", Guest: "/run/vsock.sock"}},
		{"firecracker", l.Firecracker, jailArtifact{Source: "/opt/firecracker", Host: "/state/ws1/jail/firecracker", Guest: "/firecracker"}},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %+v, want %+v", c.name, c.got, c.want)
		}
	}
	if len(l.Disks) != 1 {
		t.Fatalf("Disks len = %d, want 1", len(l.Disks))
	}
	wantDisk := jailArtifact{ID: "data", Source: "/vol/data.img", Host: "/state/ws1/jail/disks/data", Guest: "/disks/data"}
	if l.Disks[0] != wantDisk {
		t.Errorf("Disks[0] = %+v, want %+v", l.Disks[0], wantDisk)
	}
}

func TestConfinedLaunchArgsTranslateWorkspacePaths(t *testing.T) {
	opts := Options{Name: "ws1", StateDir: "/state"}

	bootArgs, err := confinedLaunchArgs(opts, []string{
		"--api-sock", "/state/ws1/firecracker-api.sock",
		"--config-file", "/state/ws1/firecracker.json",
	})
	if err != nil {
		t.Fatalf("confinedLaunchArgs boot: %v", err)
	}
	wantBoot := []string{"--api-sock", "/run/firecracker-api.sock", "--config-file", "/run/firecracker.json"}
	if !reflect.DeepEqual(bootArgs, wantBoot) {
		t.Fatalf("boot args = %#v, want %#v", bootArgs, wantBoot)
	}

	loadArgs, err := confinedLaunchArgs(opts, []string{"--api-sock", "/state/ws1/firecracker-api.sock"})
	if err != nil {
		t.Fatalf("confinedLaunchArgs load: %v", err)
	}
	wantLoad := []string{"--api-sock", "/run/firecracker-api.sock"}
	if !reflect.DeepEqual(loadArgs, wantLoad) {
		t.Fatalf("load args = %#v, want %#v", loadArgs, wantLoad)
	}
}

func TestStageJailArtifactsHardlinks(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kernelSrc := filepath.Join(srcDir, "vmlinux")
	rootfsSrc := filepath.Join(srcDir, "rootfs.ext4")
	fcSrc := filepath.Join(srcDir, "firecracker")
	for _, p := range []string{kernelSrc, rootfsSrc, fcSrc} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// State dir shares the tempdir filesystem with the sources, so staging
	// hard-links (no EXDEV bind fallback).
	opts := Options{Name: "ws1", StateDir: filepath.Join(tmp, "state")}
	cfg := &vmkit.Config{KernelPath: kernelSrc, RootfsPath: rootfsSrc}
	l := confinedJailLayout(opts, cfg, fcSrc)

	if err := stageJailArtifacts(l); err != nil {
		t.Fatalf("stageJailArtifacts: %v", err)
	}
	assertSameFile(t, kernelSrc, l.Kernel.Host)
	assertSameFile(t, rootfsSrc, l.Rootfs.Host)
	assertSameFile(t, fcSrc, l.Firecracker.Host)
	if fi, err := os.Stat(filepath.Dir(l.VsockUDS.Host)); err != nil || !fi.IsDir() {
		t.Errorf("jail run dir not created: %v", err)
	}
}

func TestStageJailArtifactsDereferencesFirecrackerSymlink(t *testing.T) {
	tmp := t.TempDir()
	srcDir := filepath.Join(tmp, "src")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatal(err)
	}
	kernelSrc := filepath.Join(srcDir, "vmlinux")
	rootfsSrc := filepath.Join(srcDir, "rootfs.ext4")
	fcReal := filepath.Join(srcDir, "firecracker-real")
	fcLink := filepath.Join(srcDir, "firecracker")
	for _, p := range []string{kernelSrc, rootfsSrc} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(fcReal, []byte("firecracker"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(fcReal, fcLink); err != nil {
		t.Fatal(err)
	}

	opts := Options{Name: "ws1", StateDir: filepath.Join(tmp, "state")}
	cfg := &vmkit.Config{KernelPath: kernelSrc, RootfsPath: rootfsSrc}
	l := confinedJailLayout(opts, cfg, fcLink)

	if err := stageJailArtifacts(l); err != nil {
		t.Fatalf("stageJailArtifacts: %v", err)
	}
	info, err := os.Lstat(l.Firecracker.Host)
	if err != nil {
		t.Fatalf("lstat staged firecracker: %v", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("staged firecracker is a symlink; confined exec would resolve it outside the jail")
	}
	assertSameFile(t, fcReal, l.Firecracker.Host)
	if info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("staged firecracker mode = %v, want executable", info.Mode().Perm())
	}
}

func assertSameFile(t *testing.T, a, b string) {
	t.Helper()
	fa, err := os.Stat(a)
	if err != nil {
		t.Fatalf("stat %s: %v", a, err)
	}
	fb, err := os.Stat(b)
	if err != nil {
		t.Fatalf("stat %s: %v", b, err)
	}
	if !os.SameFile(fa, fb) {
		t.Errorf("%s and %s are not the same file (hard link expected)", a, b)
	}
}

func TestResolveConfinementModeOff(t *testing.T) {
	// "off" resolves to off regardless of host facts (euid / userns), so this is
	// deterministic on any runner.
	mode, err := resolveConfinementMode(Options{Confinement: "off"})
	if err != nil {
		t.Fatalf("resolveConfinementMode: %v", err)
	}
	if mode != confinementOff {
		t.Errorf("mode = %v, want off", mode)
	}
}

func TestHostResponseConfinementInactiveWhenOff(t *testing.T) {
	// Confinement is on by default; with the knob explicitly "off", doctor must
	// not claim it active.
	t.Setenv(confinementEnv, "off")
	resp, _ := hostResponse(Options{FirecrackerPath: "/bin/true"})
	if resp.Host == nil {
		t.Fatal("host support missing")
	}
	if resp.Host.ConfinementActive {
		t.Error("ConfinementActive = true, want false when confinement is off")
	}
	if resp.Host.ConfinementMode != "" {
		t.Errorf("ConfinementMode = %q, want empty when off", resp.Host.ConfinementMode)
	}
}
