package rootfs

import (
	"archive/tar"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func writeStageTar(stageDir string, tw *tar.Writer) error {
	return filepath.WalkDir(stageDir, func(hostPath string, entry os.DirEntry, err error) error {
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
		var link string
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(hostPath)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		header.Name = name
		if entry.IsDir() && !strings.HasSuffix(header.Name, "/") {
			header.Name += "/"
		}
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
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
}
