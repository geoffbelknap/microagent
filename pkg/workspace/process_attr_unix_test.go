//go:build !windows

package workspace

import (
	"os/exec"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDetachedSysProcAttrStartsNewSession(t *testing.T) {
	attr := detachedSysProcAttr()
	if !attr.Setsid {
		t.Fatal("Setsid = false, want detached supervisor to start a new session")
	}
	if attr.Setpgid {
		t.Fatal("Setpgid = true, want Setsid to establish the process group")
	}

	cmd := exec.Command("sleep", "30")
	cmd.SysProcAttr = attr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start detached child: %v", err)
	}
	pid := cmd.Process.Pid
	defer func() {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	}()
	sid, err := unix.Getsid(pid)
	if err != nil {
		t.Fatalf("get detached child session: %v", err)
	}
	if sid != pid {
		t.Fatalf("child session = %d, want process id %d", sid, pid)
	}
}
