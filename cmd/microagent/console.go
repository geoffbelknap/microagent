package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

func dataAfterOffset(data []byte, currentOffset, detectAfter int64) []byte {
	startOffset := currentOffset - int64(len(data))
	if detectAfter <= startOffset {
		return data
	}
	if detectAfter >= currentOffset {
		return nil
	}
	return data[detectAfter-startOffset:]
}

func waitForConsoleReady(ctx context.Context, path string, timeout time.Duration) error {
	const maxConsoleReadyBytes int64 = 64 * 1024
	var deadline time.Time
	if timeout > 0 {
		deadline = time.Now().Add(timeout)
	}
	for {
		data, err := readFileTail(path, maxConsoleReadyBytes)
		if err == nil && consoleLooksReady(string(data)) {
			return nil
		} else if err != nil && !os.IsNotExist(err) {
			return err
		}
		if !deadline.IsZero() && time.Now().After(deadline) {
			return fmt.Errorf("console did not become ready before timeout: %s", path)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func readFileTail(path string, maxBytes int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if info.Size() > maxBytes {
		offset = info.Size() - maxBytes
	}
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(io.LimitReader(file, maxBytes))
}

func consoleLooksReady(output string) bool {
	return strings.Contains(output, "# ") ||
		strings.Contains(output, "$ ") ||
		strings.Contains(strings.ToLower(output), "login:")
}

func copyConsoleInput(dst io.Writer, src io.Reader) (int64, error) {
	var total int64
	buffer := make([]byte, 4096)
	state := consoleInputState{}
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			filtered, detach := filterConsoleInput(chunk, &state)
			written, writeErr := writeConsoleInputChunk(dst, filtered)
			total += written
			if writeErr != nil {
				return total, writeErr
			}
			if detach {
				return total, nil
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}

func copyShellInput(dst io.Writer, src io.Reader) (int64, error) {
	var total int64
	buffer := make([]byte, 4096)
	state := consoleInputState{}
	for {
		n, readErr := src.Read(buffer)
		if n > 0 {
			chunk := buffer[:n]
			filtered, detach := filterConsoleInput(chunk, &state)
			written, writeErr := dst.Write(filtered)
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if detach {
				return total, nil
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				return total, nil
			}
			return total, readErr
		}
	}
}

type consoleInputState struct {
	sawDetachPrefix bool
}

func filterConsoleInput(chunk []byte, state *consoleInputState) ([]byte, bool) {
	if len(chunk) == 0 {
		return chunk, false
	}
	var out []byte
	for i := 0; i < len(chunk); i++ {
		b := chunk[i]
		if state.sawDetachPrefix {
			state.sawDetachPrefix = false
			if b == consoleDetachSuffix {
				return out, true
			}
			out = append(out, consoleDetachPrefix)
		}
		if b == consoleDetachByte {
			return out, true
		}
		if b == consoleDetachPrefix {
			state.sawDetachPrefix = true
			continue
		}
		out = append(out, b)
	}
	return out, false
}

func writeConsoleInputChunk(dst io.Writer, chunk []byte) (int64, error) {
	if len(chunk) == 0 {
		return 0, nil
	}
	chunk = stripBracketedPasteMarkers(chunk)
	normalized := bytes.ReplaceAll(chunk, []byte("\n"), []byte("\r"))
	written, err := dst.Write(normalized)
	if err != nil {
		return int64(written), err
	}
	if written != len(normalized) {
		return int64(written), io.ErrShortWrite
	}
	return int64(written), nil
}

func stripBracketedPasteMarkers(chunk []byte) []byte {
	chunk = bytes.ReplaceAll(chunk, []byte("\x1b[200~"), nil)
	chunk = bytes.ReplaceAll(chunk, []byte("\x1b[201~"), nil)
	return chunk
}
