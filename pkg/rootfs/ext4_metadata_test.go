package rootfs

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestApplyExt4MetadataPreservesOCIInodeMetadata(t *testing.T) {
	mke2fsPath, err := exec.LookPath("mke2fs")
	if err != nil {
		t.Skip("mke2fs is not installed")
	}
	debugfsPath, err := exec.LookPath("debugfs")
	if err != nil {
		t.Skip("debugfs is not installed")
	}

	stageDir := t.TempDir()
	if err := ensureStageMetadata(stageDir); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(stageDir, "owned"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "owned", "tool"), []byte("payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	trickyNames := []string{`systemd\x2dunit`, `quoted"name`}
	for _, name := range trickyNames {
		if err := os.WriteFile(filepath.Join(stageDir, "owned", name), []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(stageDir, "dev"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stageDir, "dev", "nullish"), nil, 0o600); err != nil {
		t.Fatal(err)
	}

	root, err := os.OpenRoot(stageDir)
	if err != nil {
		t.Fatal(err)
	}
	mtime := int64(1_234_567_890)
	if err := recordStageMetadata(root, stageMetadataRecord{
		Path:  "owned/tool",
		Type:  "regular",
		Mode:  0o4750,
		UID:   123,
		GID:   456,
		Mtime: &mtime,
		Xattrs: map[string][]byte{
			"user.microagent":     []byte("yes"),
			"security.capability": {0x01, 0x00, 0x00, 0x02, 0x00, 0x04, 0x00, 0x00},
		},
	}); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	for _, name := range trickyNames {
		if err := recordStageMetadata(root, stageMetadataRecord{
			Path: "owned/" + name, Type: "regular", Mode: 0o640,
			UID: 234, GID: 567,
		}); err != nil {
			_ = root.Close()
			t.Fatal(err)
		}
	}
	if err := recordStageMetadata(root, stageMetadataRecord{
		Path: "dev/nullish", Type: "character", Mode: 0o666,
		UID: 12, GID: 34, DevMajor: 1, DevMinor: 3,
	}); err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}

	imagePath := filepath.Join(t.TempDir(), "rootfs.ext4")
	if err := allocateFile(imagePath, 32*1024*1024); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(mke2fsPath, "-q", "-t", "ext4", "-d", stageDir, imagePath).CombinedOutput(); err != nil {
		t.Fatalf("mke2fs: %v: %s", err, output)
	}
	if err := applyExt4Metadata(t.Context(), debugfsPath, stageDir, imagePath); err != nil {
		t.Fatal(err)
	}

	toolStat := debugFSRead(t, debugfsPath, imagePath, `stat "/owned/tool"`)
	for _, want := range []string{"Mode:  04750", "User:   123", "Group:   456", "mtime: 0x499602d2"} {
		if !strings.Contains(toolStat, want) {
			t.Errorf("tool stat missing %q:\n%s", want, toolStat)
		}
	}
	for _, name := range trickyNames {
		quoted, err := quoteDebugFSMetadataArg("/owned/" + name)
		if err != nil {
			t.Fatal(err)
		}
		stat := debugFSRead(t, debugfsPath, imagePath, "stat "+quoted)
		for _, want := range []string{"Mode:  0640", "User:   234", "Group:   567"} {
			if !strings.Contains(stat, want) {
				t.Errorf("%q stat missing %q:\n%s", name, want, stat)
			}
		}
	}
	xattrs := debugFSRead(t, debugfsPath, imagePath, `ea_list "/owned/tool"`)
	if !strings.Contains(xattrs, "user.microagent") || !strings.Contains(xattrs, "yes") || !strings.Contains(xattrs, "security.capability") {
		t.Errorf("xattr was not preserved:\n%s", xattrs)
	}
	specialStat := debugFSRead(t, debugfsPath, imagePath, `stat "/dev/nullish"`)
	for _, want := range []string{"Type: character", "User:    12", "Group:    34", "Device major/minor number: 01:03"} {
		if !strings.Contains(specialStat, want) {
			t.Errorf("special-file stat missing %q:\n%s", want, specialStat)
		}
	}
	ledgerStat := debugFSRead(t, debugfsPath, imagePath, `stat "/`+stageMetadataName+`"`)
	if !strings.Contains(strings.ToLower(ledgerStat), "file not found") {
		t.Errorf("internal metadata ledger remains in image:\n%s", ledgerStat)
	}
}

func debugFSRead(t *testing.T, debugfsPath, imagePath, command string) string {
	t.Helper()
	output, err := exec.Command(debugfsPath, "-R", command, imagePath).CombinedOutput()
	if err != nil {
		t.Fatalf("debugfs %s: %v: %s", command, err, output)
	}
	return string(output)
}
