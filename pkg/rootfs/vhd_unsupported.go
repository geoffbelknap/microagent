//go:build !windows

package rootfs

import (
	"context"
	"fmt"
)

func buildVHDImage(ctx context.Context, stageDir, tmpImage, outputPath string, sizeBytes int64, reserveFreeSpace bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return fmt.Errorf("vhd rootfs output is only supported on windows")
}
