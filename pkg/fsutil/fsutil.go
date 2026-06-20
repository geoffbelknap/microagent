// Package fsutil holds small filesystem helpers shared across microagent
// binaries.
package fsutil

import "os"

// WriteFile writes data to path with the given mode, then forces that mode via
// an explicit Chmod so the result is not narrowed by the process umask.
func WriteFile(path string, data []byte, mode os.FileMode) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Chmod(mode); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}
