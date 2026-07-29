package main

import (
	"bytes"
	"io"
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
