package rootfs

import (
	"archive/tar"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const symlinkMarkerPrefix = "microagent-symlink\x00"
const stageMetadataName = ".microagent-rootfs-metadata.jsonl"
const stageMetadataVersion = 1

type stageMetadataRecord struct {
	Version  int               `json:"version"`
	Path     string            `json:"path,omitempty"`
	Type     string            `json:"type,omitempty"`
	Mode     int64             `json:"mode,omitempty"`
	UID      int               `json:"uid,omitempty"`
	GID      int               `json:"gid,omitempty"`
	Mtime    *int64            `json:"mtime,omitempty"`
	Xattrs   map[string][]byte `json:"xattrs,omitempty"`
	DevMajor int64             `json:"dev_major,omitempty"`
	DevMinor int64             `json:"dev_minor,omitempty"`
}

// stageModeRecord and readStageEntries retain the focused ownership view used
// by the setuid policy and its tests while the durable ledger carries the
// complete OCI metadata record.
type stageModeRecord struct {
	Path string
	Mode int64
	Uid  int
	Gid  int
}

func writeStageTar(stageDir string, tw *tar.Writer) (int64, error) {
	metadata, err := readStageMetadata(stageDir)
	if err != nil {
		return 0, err
	}
	var contentBytes int64
	hardLinks := map[string]string{}
	walkErr := filepath.WalkDir(stageDir, func(hostPath string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if hostPath == stageDir {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(stageDir, hostPath)
		if err != nil {
			return err
		}
		name := filepath.ToSlash(rel)
		if name == "." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("unsafe stage path %q", rel)
		}
		if name == stageMetadataName {
			return nil
		}
		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(hostPath)
			if err != nil {
				return err
			}
		}
		if info.Mode().IsRegular() {
			markerTarget, ok, err := readSymlinkMarker(hostPath)
			if err != nil {
				return err
			}
			if ok {
				return tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: markerTarget, Mode: 0o777, ModTime: info.ModTime()})
			}
			if id, linked := stageHardLinkID(hostPath, info); linked {
				if first, seen := hardLinks[id]; seen {
					return tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeLink, Linkname: first, Mode: int64(info.Mode().Perm()), ModTime: info.ModTime()})
				}
				hardLinks[id] = name
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = name
		if record, ok := metadata[name]; ok {
			header.Mode = record.Mode
			header.Uid = record.UID
			header.Gid = record.GID
			if record.Mtime != nil {
				header.ModTime = time.Unix(*record.Mtime, 0)
			}
			if len(record.Xattrs) != 0 {
				if header.PAXRecords == nil {
					header.PAXRecords = make(map[string]string, len(record.Xattrs))
				}
				for key, value := range record.Xattrs {
					header.PAXRecords["SCHILY.xattr."+key] = string(value)
				}
			}
		}
		if entry.IsDir() && !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		contentBytes += (header.Size + 4095) &^ 4095
		in, err := os.Open(hostPath)
		if err != nil {
			return err
		}
		if _, err := io.Copy(tw, in); err != nil {
			_ = in.Close()
			return err
		}
		return in.Close()
	})
	return contentBytes, walkErr
}

func writeSymlinkMarker(path, target string) error {
	return os.WriteFile(path, []byte(symlinkMarkerPrefix+target), 0o644)
}

func writeSymlinkMarkerInRoot(root *os.Root, name, target string) error {
	f, err := root.OpenFile(name, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte(symlinkMarkerPrefix + target)); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func readSymlinkMarker(path string) (string, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false, err
	}
	text := string(data)
	if !strings.HasPrefix(text, symlinkMarkerPrefix) {
		return "", false, nil
	}
	return strings.TrimPrefix(text, symlinkMarkerPrefix), true, nil
}

func readStageEntries(stageDir string) (map[string]stageModeRecord, error) {
	metadata, err := readStageMetadata(stageDir)
	if err != nil {
		return nil, err
	}
	entries := make(map[string]stageModeRecord, len(metadata))
	for name, record := range metadata {
		entries[name] = stageModeRecord{
			Path: record.Path,
			Mode: record.Mode,
			Uid:  record.UID,
			Gid:  record.GID,
		}
	}
	return entries, nil
}

func ensureStageMetadata(stageDir string) error {
	metadataPath := filepath.Join(stageDir, stageMetadataName)
	if _, err := os.Stat(metadataPath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat stage metadata: %w", err)
	}
	f, err := os.OpenFile(metadataPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create stage metadata: %w", err)
	}
	if err := initializeStageMetadata(f); err != nil {
		_ = f.Close()
		return fmt.Errorf("initialize stage metadata: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close stage metadata: %w", err)
	}
	return nil
}

// initializeStageMetadata gives the guest root a deterministic, traversable
// default. OCI layers are allowed to omit an explicit root-directory entry;
// in that case the host staging directory's mode is only an implementation
// detail and may have been narrowed by the builder's umask. A later explicit
// OCI root entry is appended to the ledger and wins when it is read.
func initializeStageMetadata(w io.Writer) error {
	encoder := json.NewEncoder(w)
	for _, record := range []stageMetadataRecord{
		{Version: stageMetadataVersion},
		defaultStageRootMetadata(),
	} {
		if err := encoder.Encode(record); err != nil {
			return err
		}
	}
	return nil
}

func defaultStageRootMetadata() stageMetadataRecord {
	return stageMetadataRecord{
		Version: stageMetadataVersion,
		Path:    ".",
		Type:    "directory",
		Mode:    0o755,
		UID:     0,
		GID:     0,
	}
}

func readStageMetadata(stageDir string) (map[string]stageMetadataRecord, error) {
	data, err := os.ReadFile(filepath.Join(stageDir, stageMetadataName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read stage metadata: %w", err)
	}
	metadata := map[string]stageMetadataRecord{}
	seenVersion := false
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var record stageMetadataRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("parse stage metadata: %w", err)
		}
		if record.Version != stageMetadataVersion {
			return nil, fmt.Errorf("stage metadata version %d is unsupported", record.Version)
		}
		seenVersion = true
		if record.Path != "" {
			metadata[record.Path] = record
		}
	}
	if !seenVersion {
		return nil, fmt.Errorf("stage metadata has no version marker")
	}
	return metadata, nil
}
