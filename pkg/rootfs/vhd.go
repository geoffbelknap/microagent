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
}

// writeStageTar streams the stage tree to tw and returns an estimate of the
// data bytes the resulting filesystem will hold (file sizes rounded up to
// 4 KiB blocks). Hard-linked stage files are emitted as tar hard links so
// images that link heavily (busybox applets) do not explode into copies.
func writeStageTar(stageDir string, tw *tar.Writer) (int64, error) {
	modes, err := readStageModes(stageDir)
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
				header := &tar.Header{
					Name:     name,
					Typeflag: tar.TypeSymlink,
					Linkname: markerTarget,
					Mode:     0o777,
					ModTime:  info.ModTime(),
				}
				return tw.WriteHeader(header)
			}
			if id, linked := stageHardLinkID(hostPath, info); linked {
				if first, seen := hardLinks[id]; seen {
					return tw.WriteHeader(&tar.Header{
						Name:     name,
						Typeflag: tar.TypeLink,
						Linkname: first,
						Mode:     int64(info.Mode().Perm()),
						ModTime:  info.ModTime(),
					})
				}
				hardLinks[id] = name
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = name
		if mode, ok := modes[name]; ok {
			header.Mode = mode
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
	data := []byte(symlinkMarkerPrefix + target)
	return os.WriteFile(path, data, 0o644)
}

// writeSymlinkMarkerInRoot writes a symlink marker through the os.Root
// sandbox so the marker path gets the same traversal protection as every
// other extracted layer entry.
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

func readStageModes(stageDir string) (map[string]int64, error) {
	data, err := os.ReadFile(filepath.Join(stageDir, stageMetadataName))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read stage metadata: %w", err)
	}
	modes := map[string]int64{}
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
			modes[record.Path] = record.Mode
		}
	}
	return modes, nil
}
