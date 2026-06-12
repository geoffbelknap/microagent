//go:build windows

package rootfs

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// stageHardLinkID returns a stable identity for a regular stage file that
// has more than one name (an NTFS hard link), so the stage tar can emit
// later names as tar hard links instead of duplicating content. Returns
// ok=false for singly-linked files and whenever the identity cannot be
// read (the caller then falls back to copying the content, which is always
// correct).
func stageHardLinkID(hostPath string, info os.FileInfo) (string, bool) {
	if !info.Mode().IsRegular() {
		return "", false
	}
	pathPtr, err := windows.UTF16PtrFromString(hostPath)
	if err != nil {
		return "", false
	}
	handle, err := windows.CreateFile(pathPtr, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return "", false
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	var data windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &data); err != nil {
		return "", false
	}
	if data.NumberOfLinks <= 1 {
		return "", false
	}
	return fmt.Sprintf("%d-%d-%d", data.VolumeSerialNumber, data.FileIndexHigh, data.FileIndexLow), true
}
