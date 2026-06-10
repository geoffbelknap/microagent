//go:build windows

package rootfs

import (
	"archive/tar"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Microsoft/hcsshim/ext4/tar2ext4"
)

func buildVHDImage(ctx context.Context, stageDir, tmpImage, outputPath string, sizeBytes int64) error {
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o755); err != nil {
		return fmt.Errorf("create output dir: %w", err)
	}
	out, err := os.Create(tmpImage)
	if err != nil {
		return fmt.Errorf("create vhd image: %w", err)
	}
	defer func() { _ = out.Close() }()

	reader, writer := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		tw := tar.NewWriter(writer)
		writeErr := writeStageTar(stageDir, tw)
		closeErr := tw.Close()
		if writeErr == nil {
			writeErr = closeErr
		}
		if writeErr != nil {
			_ = writer.CloseWithError(writeErr)
			errCh <- writeErr
			return
		}
		errCh <- writer.Close()
	}()

	convertErr := tar2ext4.Convert(reader, out, tar2ext4.ConvertBackslash, tar2ext4.AppendVhdFooter, tar2ext4.MaximumDiskSize(sizeBytes))
	readCloseErr := reader.Close()
	writeErr := <-errCh
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	if convertErr != nil {
		return fmt.Errorf("build vhd image: %w", convertErr)
	}
	if readCloseErr != nil {
		return fmt.Errorf("close stage tar pipe: %w", readCloseErr)
	}
	if writeErr != nil {
		return fmt.Errorf("write stage tar: %w", writeErr)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close vhd image: %w", err)
	}
	if err := os.Rename(tmpImage, outputPath); err != nil {
		return fmt.Errorf("commit vhd image: %w", err)
	}
	return nil
}
