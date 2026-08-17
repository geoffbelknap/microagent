// Package consoleproto defines the private control messages carried alongside
// an interactive console's byte stream. The guest advertises support before it
// starts the shell; hosts that do not understand the advertisement pass the
// DCS sequence to the user's terminal, which ignores it.
package consoleproto

import (
	"encoding/binary"
	"fmt"
	"io"
)

const (
	// CapabilityV1 is written by guestinit at the start of every shell session.
	// It is a terminal DCS sequence so older clients do not render visible text.
	CapabilityV1 = "\x1bP=microagent-console;1\x1b\\"

	frameMarker  = byte(0xff)
	frameVersion = byte(1)
	frameResize  = byte(1)
	frameLength  = 11
)

// Resize describes the terminal cell dimensions for an interactive shell.
type Resize struct {
	Rows uint16
	Cols uint16
}

// EncodeResize returns one v1 resize control frame.
func EncodeResize(rows, cols int) ([]byte, error) {
	if rows <= 0 || rows > 65535 || cols <= 0 || cols > 65535 {
		return nil, fmt.Errorf("console dimensions must be between 1 and 65535 cells, got %dx%d", rows, cols)
	}
	frame := []byte{frameMarker, 'M', 'A', 'C', frameVersion, frameResize, 0, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(frame[6:8], uint16(rows))
	binary.BigEndian.PutUint16(frame[8:10], uint16(cols))
	frame[10] = frameChecksum(frame[:10])
	return frame, nil
}

// CopyInput copies user input to dst while consuming negotiated console
// control frames. Invalid marker-prefixed input is forwarded unchanged.
func CopyInput(dst io.Writer, src io.Reader, onResize func(Resize) error) (int64, error) {
	var written int64
	one := make([]byte, 1)
	for {
		n, readErr := src.Read(one)
		if n == 0 {
			if readErr == io.EOF {
				return written, nil
			}
			if readErr != nil {
				return written, readErr
			}
			continue
		}
		if one[0] != frameMarker {
			nw, writeErr := dst.Write(one)
			written += int64(nw)
			if writeErr != nil {
				return written, writeErr
			}
			if nw != 1 {
				return written, io.ErrShortWrite
			}
			if readErr != nil && readErr != io.EOF {
				return written, readErr
			}
			continue
		}

		frame := make([]byte, frameLength)
		frame[0] = frameMarker
		nrest, frameErr := io.ReadFull(src, frame[1:])
		frame = frame[:1+nrest]
		if frameErr == nil && validResizeFrame(frame) {
			resize := Resize{
				Rows: binary.BigEndian.Uint16(frame[6:8]),
				Cols: binary.BigEndian.Uint16(frame[8:10]),
			}
			if onResize != nil {
				if err := onResize(resize); err != nil {
					return written, err
				}
			}
			continue
		}

		nw, writeErr := dst.Write(frame)
		written += int64(nw)
		if writeErr != nil {
			return written, writeErr
		}
		if nw != len(frame) {
			return written, io.ErrShortWrite
		}
		if frameErr != nil {
			if frameErr == io.EOF || frameErr == io.ErrUnexpectedEOF {
				return written, nil
			}
			return written, frameErr
		}
	}
}

func validResizeFrame(frame []byte) bool {
	return len(frame) == frameLength &&
		frame[0] == frameMarker &&
		frame[1] == 'M' && frame[2] == 'A' && frame[3] == 'C' &&
		frame[4] == frameVersion && frame[5] == frameResize &&
		frame[10] == frameChecksum(frame[:10]) &&
		binary.BigEndian.Uint16(frame[6:8]) != 0 &&
		binary.BigEndian.Uint16(frame[8:10]) != 0
}

func frameChecksum(frame []byte) byte {
	var checksum byte
	for _, value := range frame {
		checksum ^= value
	}
	return checksum
}
