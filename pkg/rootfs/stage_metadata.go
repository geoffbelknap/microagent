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
)

const symlinkMarkerPrefix = "microagent-symlink\x00"
const stageMetadataName = ".microagent-rootfs-metadata.jsonl"

type stageModeRecord struct {
	Path string `json:"path"`
	Mode int64  `json:"mode"`
	Uid  int    `json:"uid"`
	Gid  int    `json:"gid"`
}

func writeStageTar(stageDir string, tw *tar.Writer) (int64, error) {
	entries, err := readStageEntries(stageDir)
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
		if record, ok := entries[name]; ok {
			header.Mode = record.Mode
			header.Uid = record.Uid
			header.Gid = record.Gid
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
	data, err := os.ReadFile(filepath.Join(stageDir, stageMetadataName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read stage metadata: %w", err)
	}
	entries := map[string]stageModeRecord{}
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var record stageModeRecord
		if err := json.Unmarshal(line, &record); err != nil {
			return nil, fmt.Errorf("parse stage metadata: %w", err)
		}
		if record.Path != "" {
			entries[record.Path] = record
		}
	}
	return entries, nil
}
