package main

import (
	"fmt"
	"os"
)

func writeImageList(stdout *os.File, images []imageRecord) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, map[string]any{"images": images})
	}
	if len(images) == 0 {
		fmt.Fprintln(stdout, "No images.")
		return nil
	}
	// DIGEST keeps its legacy 72-wide field (sized for a full
	// "sha256:"+64-hex digest) so every other column's start position is
	// byte-identical to before; only the digest text itself shortens to 12
	// hex characters, matching every human list view. Full digests remain
	// in --json and `image inspect` (writeImageRecord).
	cols := []tableColumn{
		{Header: "IMAGE", Legacy: 48, Min: 16, Max: 60, Flex: true},
		{Header: "DIGEST", Legacy: 72, Min: 12, Max: 12},
		{Header: "PLATFORM", Legacy: 16, Min: 8, Max: 16},
		{Header: "SIZE", Legacy: 10, Min: 6, Max: 10},
		{Header: "LAST USED", Legacy: 0, Min: 10},
	}
	rows := make([][]tableCell, len(images))
	for i, image := range images {
		platform := image.Platform.OS + "/" + image.Platform.Architecture
		if image.Platform.Variant != "" {
			platform += "/" + image.Platform.Variant
		}
		rows[i] = []tableCell{
			cell(image.ImageRef),
			cell(shortDigest(image.Digest)),
			cell(platform),
			cell(fmt.Sprintf("%d", image.SizeBytes)),
			cell(image.LastUsedAt),
		}
	}
	renderTable(stdout, cols, rows)
	return nil
}

func writeImageRecord(stdout *os.File, record imageRecord) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, record)
	}
	fmt.Fprintf(stdout, "Image: %s\n", record.ImageRef)
	if record.ResolvedRef != "" {
		fmt.Fprintf(stdout, "Resolved: %s\n", record.ResolvedRef)
	}
	if record.Digest != "" {
		fmt.Fprintf(stdout, "Digest: %s\n", record.Digest)
	}
	platform := record.Platform.OS + "/" + record.Platform.Architecture
	if record.Platform.Variant != "" {
		platform += "/" + record.Platform.Variant
	}
	fmt.Fprintf(stdout, "Platform: %s\n", platform)
	if record.OutputPath != "" {
		fmt.Fprintf(stdout, "Rootfs: %s\n", record.OutputPath)
	}
	if record.SizeBytes != 0 {
		fmt.Fprintf(stdout, "Size: %d\n", record.SizeBytes)
	}
	return nil
}

func writeImagePruneResult(stdout *os.File, result imagePruneResult) error {
	if outputJSON(stdout) {
		return writeJSON(stdout, result)
	}
	fmt.Fprintf(stdout, "Removed: %d\n", len(result.Removed))
	fmt.Fprintf(stdout, "Deleted: %d\n", len(result.Deleted))
	fmt.Fprintf(stdout, "Kept: %d\n", len(result.Kept))
	if result.CacheEntriesRemoved > 0 {
		fmt.Fprintf(stdout, "Cache cleared: %d entries, %d bytes\n", result.CacheEntriesRemoved, result.CacheBytesFreed)
	}
	return nil
}
