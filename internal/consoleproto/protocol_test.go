package consoleproto

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

func TestCopyInputConsumesResizeFrame(t *testing.T) {
	frame, err := EncodeResize(43, 137)
	if err != nil {
		t.Fatal(err)
	}
	input := append([]byte("before"), frame...)
	input = append(input, []byte("after")...)
	var output bytes.Buffer
	var got Resize
	written, err := CopyInput(&output, bytes.NewReader(input), func(resize Resize) error {
		got = resize
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != (Resize{Rows: 43, Cols: 137}) {
		t.Fatalf("resize = %+v", got)
	}
	if output.String() != "beforeafter" || written != int64(len("beforeafter")) {
		t.Fatalf("output=%q written=%d", output.String(), written)
	}
}

func TestCopyInputPreservesInvalidAndPartialFrames(t *testing.T) {
	for _, input := range [][]byte{
		{frameMarker, 'n', 'o', 't', '-', 'a', '-', 'f', 'r', 'a', 'm'},
		{frameMarker, 'M', 'A'},
	} {
		var output bytes.Buffer
		if _, err := CopyInput(&output, bytes.NewReader(input), nil); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(output.Bytes(), input) {
			t.Fatalf("output=%v want=%v", output.Bytes(), input)
		}
	}
}

func TestCopyInputReturnsResizeError(t *testing.T) {
	frame, err := EncodeResize(24, 80)
	if err != nil {
		t.Fatal(err)
	}
	want := errors.New("resize failed")
	_, err = CopyInput(io.Discard, bytes.NewReader(frame), func(Resize) error { return want })
	if !errors.Is(err, want) {
		t.Fatalf("error = %v", err)
	}
}

func TestEncodeResizeRejectsInvalidDimensions(t *testing.T) {
	for _, size := range [][2]int{{0, 80}, {24, 0}, {65536, 80}, {24, 65536}} {
		if _, err := EncodeResize(size[0], size[1]); err == nil {
			t.Fatalf("EncodeResize(%d, %d) succeeded", size[0], size[1])
		}
	}
}
