package rootfs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
)

const (
	ext4ModeFIFO      = 0o010000
	ext4ModeCharacter = 0o020000
	ext4ModeDirectory = 0o040000
	ext4ModeBlock     = 0o060000
	ext4ModeRegular   = 0o100000
	ext4ModeSymlink   = 0o120000
)

// applyExt4Metadata repairs metadata that cannot safely be represented in the
// unprivileged host staging tree. mke2fs first imports the bytes and directory
// structure; debugfs then writes the OCI inode metadata directly to the
// offline filesystem, before the image is committed or measured.
func applyExt4Metadata(ctx context.Context, debugfsPath, stageDir, imagePath string) error {
	metadata, err := readStageMetadata(stageDir)
	if err != nil {
		return err
	}
	if metadata == nil {
		return nil
	}
	if err := addDefaultStageMetadata(stageDir, metadata); err != nil {
		return err
	}

	workDir, err := os.MkdirTemp(filepath.Dir(imagePath), ".oci-metadata-*")
	if err != nil {
		return fmt.Errorf("create metadata work dir: %w", err)
	}
	defer os.RemoveAll(workDir)

	paths := make([]string, 0, len(metadata))
	for name := range metadata {
		hostPath := filepath.Join(stageDir, filepath.FromSlash(name))
		if name == "." {
			hostPath = stageDir
		}
		if _, err := os.Lstat(hostPath); err == nil {
			paths = append(paths, name)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect staged path %q: %w", name, err)
		}
	}
	sort.Strings(paths)

	var commands strings.Builder
	for _, name := range paths {
		record := metadata[name]
		if err := appendSpecialFileCommands(&commands, record); err != nil {
			return err
		}
	}
	for i, name := range paths {
		record := metadata[name]
		guestPath := "/" + name
		if name == "." {
			guestPath = "/"
		}
		quotedPath, err := quoteDebugFSMetadataArg(guestPath)
		if err != nil {
			return fmt.Errorf("OCI metadata path %q: %w", name, err)
		}
		fileType, err := ext4FileType(record, filepath.Join(stageDir, filepath.FromSlash(name)))
		if err != nil {
			return fmt.Errorf("OCI metadata path %q: %w", name, err)
		}
		fmt.Fprintf(&commands, "set_inode_field %s uid %d\n", quotedPath, record.UID)
		fmt.Fprintf(&commands, "set_inode_field %s gid %d\n", quotedPath, record.GID)
		fmt.Fprintf(&commands, "set_inode_field %s mode 0%o\n", quotedPath, fileType|record.Mode&0o7777)
		if record.Mtime != nil {
			fmt.Fprintf(&commands, "set_inode_field %s mtime @%d\n", quotedPath, *record.Mtime)
		}
		xattrNames := make([]string, 0, len(record.Xattrs))
		for attr := range record.Xattrs {
			xattrNames = append(xattrNames, attr)
		}
		sort.Strings(xattrNames)
		for j, attr := range xattrNames {
			valuePath := filepath.Join(workDir, fmt.Sprintf("xattr-%d-%d", i, j))
			if err := os.WriteFile(valuePath, record.Xattrs[attr], 0o600); err != nil {
				return fmt.Errorf("write xattr value: %w", err)
			}
			quotedValue, err := quoteDebugFSMetadataArg(valuePath)
			if err != nil {
				return err
			}
			quotedAttr, err := quoteDebugFSMetadataArg(attr)
			if err != nil {
				return fmt.Errorf("OCI xattr %q: %w", attr, err)
			}
			fmt.Fprintf(&commands, "ea_set -f %s %s %s\n", quotedValue, quotedPath, quotedAttr)
		}
	}
	quotedLedger, _ := quoteDebugFSMetadataArg("/" + stageMetadataName)
	fmt.Fprintf(&commands, "rm %s\n", quotedLedger)

	commandPath := filepath.Join(workDir, "commands.debugfs")
	if err := os.WriteFile(commandPath, []byte(commands.String()), 0o600); err != nil {
		return fmt.Errorf("write debugfs command file: %w", err)
	}
	cmd := exec.CommandContext(ctx, debugfsPath, "-w", "-f", commandPath, imagePath)
	output, runErr := cmd.CombinedOutput()
	text := strings.TrimSpace(string(output))
	if runErr != nil {
		return fmt.Errorf("run debugfs: %w: %s", runErr, text)
	}
	if diagnostic := debugFSMetadataDiagnostic(text); diagnostic != "" {
		return fmt.Errorf("debugfs: %s", diagnostic)
	}
	return nil
}

// addDefaultStageMetadata covers directories created implicitly while a tar
// entry or a microagent-owned file was installed. They have no OCI header of
// their own, but must still become root-owned in the guest rather than inherit
// the uid of the host process that happened to build the image.
func addDefaultStageMetadata(stageDir string, metadata map[string]stageMetadataRecord) error {
	return filepath.WalkDir(stageDir, func(hostPath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stageDir, hostPath)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if name == stageMetadataName {
			return nil
		}
		if _, ok := metadata[name]; ok {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		metadata[name] = stageMetadataRecord{
			Version: stageMetadataVersion,
			Path:    name,
			Mode:    int64(info.Mode().Perm()),
		}
		return nil
	})
}

func appendSpecialFileCommands(commands *strings.Builder, record stageMetadataRecord) error {
	if record.Type != "character" && record.Type != "block" && record.Type != "fifo" {
		return nil
	}
	guestPath := "/" + record.Path
	quotedPath, err := quoteDebugFSMetadataArg(guestPath)
	if err != nil {
		return fmt.Errorf("OCI special-file path %q: %w", record.Path, err)
	}
	parent := path.Dir(guestPath)
	base := path.Base(guestPath)
	quotedParent, err := quoteDebugFSMetadataArg(parent)
	if err != nil {
		return err
	}
	quotedBase, err := quoteDebugFSMetadataArg(base)
	if err != nil {
		return err
	}
	fmt.Fprintf(commands, "rm %s\ncd %s\n", quotedPath, quotedParent)
	switch record.Type {
	case "character":
		fmt.Fprintf(commands, "mknod %s c %d %d\n", quotedBase, record.DevMajor, record.DevMinor)
	case "block":
		fmt.Fprintf(commands, "mknod %s b %d %d\n", quotedBase, record.DevMajor, record.DevMinor)
	case "fifo":
		fmt.Fprintf(commands, "mknod %s p\n", quotedBase)
	}
	commands.WriteString("cd /\n")
	return nil
}

func ext4FileType(record stageMetadataRecord, hostPath string) (int64, error) {
	switch record.Type {
	case "directory":
		return ext4ModeDirectory, nil
	case "symlink":
		return ext4ModeSymlink, nil
	case "character":
		return ext4ModeCharacter, nil
	case "block":
		return ext4ModeBlock, nil
	case "fifo":
		return ext4ModeFIFO, nil
	case "regular", "hardlink":
		return ext4ModeRegular, nil
	case "":
		info, err := os.Lstat(hostPath)
		if err != nil {
			return 0, err
		}
		if info.IsDir() {
			return ext4ModeDirectory, nil
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ext4ModeSymlink, nil
		}
		return ext4ModeRegular, nil
	default:
		return 0, fmt.Errorf("unsupported inode type %q", record.Type)
	}
}

func quoteDebugFSMetadataArg(value string) (string, error) {
	if value == "" || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("debugfs argument is empty or contains NUL")
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("debugfs argument contains control character %s", strconv.QuoteRune(r))
		}
	}
	// debugfs uses a deliberately small argv parser: backslashes are literal,
	// while a doubled quote represents one quote inside a quoted argument.
	// Shell-style backslash escaping is incorrect here (and breaks systemd unit
	// names such as system-systemd\x2dcryptsetup.slice).
	value = strings.ReplaceAll(value, `"`, `""`)
	return `"` + value + `"`, nil
}

func debugFSMetadataDiagnostic(output string) string {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		lower := strings.ToLower(line)
		for _, marker := range []string{
			"file not found", "no such file", "usage:", "command not found",
			"filesystem not open", "bad magic", "while setting", "while looking up",
		} {
			if strings.Contains(lower, marker) {
				return line
			}
		}
	}
	return ""
}
