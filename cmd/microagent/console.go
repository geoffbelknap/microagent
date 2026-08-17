package main

import (
	"bytes"
	"io"
	"strconv"
	"strings"
	"time"
)

const consoleInputSequenceTimeout = 25 * time.Millisecond

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
	return copyFilteredConsoleInput(dst, src, writeConsoleInputChunk)
}

func copyShellInput(dst io.Writer, src io.Reader) (int64, error) {
	return copyFilteredConsoleInput(dst, src, func(dst io.Writer, chunk []byte) (int64, error) {
		written, err := dst.Write(chunk)
		if err == nil && written != len(chunk) {
			err = io.ErrShortWrite
		}
		return int64(written), err
	})
}

type consoleInputRead struct {
	data []byte
	err  error
}

func copyFilteredConsoleInput(dst io.Writer, src io.Reader, writeChunk func(io.Writer, []byte) (int64, error)) (int64, error) {
	var total int64
	state := consoleInputState{}
	reads := make(chan consoleInputRead)
	done := make(chan struct{})
	defer close(done)
	go func() {
		buffer := make([]byte, 4096)
		for {
			n, err := src.Read(buffer)
			if n == 0 && err == nil {
				continue
			}
			result := consoleInputRead{err: err}
			if n > 0 {
				result.data = append([]byte(nil), buffer[:n]...)
			}
			select {
			case reads <- result:
			case <-done:
				return
			}
			if err != nil {
				return
			}
		}
	}()

	var sequenceTimer *time.Timer
	var sequenceTimeout <-chan time.Time
	stopSequenceTimer := func() {
		if sequenceTimer != nil && !sequenceTimer.Stop() {
			select {
			case <-sequenceTimer.C:
			default:
			}
		}
		sequenceTimeout = nil
	}
	defer stopSequenceTimer()
	armSequenceTimer := func() {
		stopSequenceTimer()
		if sequenceTimer == nil {
			sequenceTimer = time.NewTimer(consoleInputSequenceTimeout)
		} else {
			sequenceTimer.Reset(consoleInputSequenceTimeout)
		}
		sequenceTimeout = sequenceTimer.C
	}
	write := func(chunk []byte) error {
		written, err := writeChunk(dst, chunk)
		total += written
		return err
	}

	for {
		select {
		case result := <-reads:
			filtered, detach := filterConsoleInput(result.data, &state)
			if err := write(filtered); err != nil {
				return total, err
			}
			if detach {
				return total, nil
			}
			if result.err != nil {
				if err := write(state.flush()); err != nil {
					return total, err
				}
				if result.err == io.EOF {
					return total, nil
				}
				return total, result.err
			}
			if len(state.kittyPrefix) > 0 {
				armSequenceTimer()
			} else {
				stopSequenceTimer()
			}
		case <-sequenceTimeout:
			sequenceTimeout = nil
			if err := write(state.flush()); err != nil {
				return total, err
			}
		}
	}
}

type consoleInputState struct {
	detachPrefix []byte
	kittyPrefix  []byte
}

func filterConsoleInput(chunk []byte, state *consoleInputState) ([]byte, bool) {
	if len(state.kittyPrefix) > 0 {
		combined := make([]byte, 0, len(state.kittyPrefix)+len(chunk))
		combined = append(combined, state.kittyPrefix...)
		combined = append(combined, chunk...)
		chunk = combined
		state.kittyPrefix = nil
	}
	if len(chunk) == 0 {
		return nil, false
	}
	var out []byte
	for i := 0; i < len(chunk); {
		token, consumed, incomplete := nextConsoleInputToken(chunk[i:])
		if incomplete {
			state.kittyPrefix = append(state.kittyPrefix[:0], chunk[i:]...)
			break
		}
		i += consumed
		if len(state.detachPrefix) > 0 {
			if token.control && token.key == 'q' {
				state.detachPrefix = nil
				return out, true
			}
			if token.release && token.key == 'p' {
				state.detachPrefix = append(state.detachPrefix, token.raw...)
				continue
			}
			out = append(out, state.detachPrefix...)
			state.detachPrefix = nil
		}
		if token.control && token.key == ']' {
			return out, true
		}
		if token.control && token.key == 'p' {
			state.detachPrefix = append(state.detachPrefix[:0], token.raw...)
			continue
		}
		out = append(out, token.raw...)
	}
	return out, false
}

func (state *consoleInputState) flush() []byte {
	buffered := make([]byte, 0, len(state.detachPrefix)+len(state.kittyPrefix))
	buffered = append(buffered, state.detachPrefix...)
	buffered = append(buffered, state.kittyPrefix...)
	state.detachPrefix = nil
	state.kittyPrefix = nil
	return buffered
}

type consoleInputToken struct {
	raw     []byte
	key     byte
	control bool
	release bool
}

func nextConsoleInputToken(input []byte) (consoleInputToken, int, bool) {
	switch input[0] {
	case consoleDetachByte:
		return consoleInputToken{raw: input[:1], key: ']', control: true}, 1, false
	case consoleDetachPrefix:
		return consoleInputToken{raw: input[:1], key: 'p', control: true}, 1, false
	case consoleDetachSuffix:
		return consoleInputToken{raw: input[:1], key: 'q', control: true}, 1, false
	}
	if key, control, release, consumed, ok := parseKittyKey(input); ok {
		return consoleInputToken{raw: input[:consumed], key: key, control: control, release: release}, consumed, false
	}
	if incompleteKittyKey(input) {
		return consoleInputToken{}, 0, true
	}
	return consoleInputToken{raw: input[:1]}, 1, false
}

func incompleteKittyKey(input []byte) bool {
	if input[0] != '\x1b' {
		return false
	}
	if len(input) == 1 {
		return true
	}
	if input[1] != '[' {
		return false
	}
	if len(input) == 2 {
		return true
	}
	limit := len(input)
	if limit > 64 {
		limit = 64
	}
	for _, character := range input[2:limit] {
		if character == 'u' {
			return false
		}
		if (character < '0' || character > '9') && character != ';' && character != ':' {
			return false
		}
	}
	return len(input) < 64
}

// parseKittyKey recognizes a complete CSI-u key event. Pi enables this
// protocol, which represents Ctrl-] as "CSI 93;5u" instead of the legacy
// 0x1d byte. Modifier values are one plus a bitmask; bit 4 is Ctrl. Release
// events are preserved instead of acting as detach shortcuts.
func parseKittyKey(input []byte) (key byte, control, release bool, consumed int, ok bool) {
	if len(input) < 6 || input[0] != '\x1b' || input[1] != '[' {
		return 0, false, false, 0, false
	}
	limit := len(input)
	if limit > 64 {
		limit = 64
	}
	end := bytes.IndexByte(input[2:limit], 'u')
	if end < 0 {
		return 0, false, false, 0, false
	}
	end += 2
	body := string(input[2:end])
	for _, character := range body {
		if (character < '0' || character > '9') && character != ';' && character != ':' {
			return 0, false, false, 0, false
		}
	}
	fields := strings.Split(body, ";")
	if len(fields) < 2 {
		return 0, false, false, 0, false
	}
	keyFields := strings.Split(fields[0], ":")
	keyCode, err := strconv.Atoi(keyFields[0])
	if err != nil || keyCode < 0 || keyCode > 255 {
		return 0, false, false, 0, false
	}
	modifierFields := strings.Split(fields[1], ":")
	modifiers, err := strconv.Atoi(modifierFields[0])
	if err != nil || modifiers < 1 {
		return 0, false, false, 0, false
	}
	if len(modifierFields) > 1 {
		eventType, err := strconv.Atoi(modifierFields[1])
		if err != nil || eventType < 1 || eventType > 3 {
			return 0, false, false, 0, false
		}
		if eventType == 3 {
			return byte(keyCode), false, true, end + 1, true
		}
	}
	return byte(keyCode), (modifiers-1)&4 != 0, false, end + 1, true
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
