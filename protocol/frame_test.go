package protocol

import (
	"bufio"
	"bytes"
	"io"
	"testing"
)

func TestFramingRoundTrip(t *testing.T) {
	payloads := [][]byte{
		{0x00},
		bytes.Repeat([]byte{0xAB}, 100),
		bytes.Repeat([]byte{0x42}, 70000), // forces multi-byte length prefix
	}
	var wire bytes.Buffer
	bw := bufio.NewWriter(&wire)
	for _, p := range payloads {
		if err := WriteFramed(bw, p); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	_ = bw.Flush()

	br := bufio.NewReader(&wire)
	fr := NewFrameReader(br, DefaultMaxPacketLen)
	for i, want := range payloads {
		got, err := fr.Next()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame %d: got %d bytes, want %d", i, len(got), len(want))
		}
	}
	if _, err := fr.Next(); err != io.EOF {
		t.Fatalf("want EOF at stream end, got %v", err)
	}
}

func TestFrameTooLarge(t *testing.T) {
	// Advertise a 100-byte packet but bound the reader at 8.
	var wire bytes.Buffer
	bw := bufio.NewWriter(&wire)
	if err := WriteFramed(bw, make([]byte, 100)); err != nil {
		t.Fatal(err)
	}
	fr := NewFrameReader(bufio.NewReader(&wire), 8)
	if _, err := fr.Next(); err != ErrPacketTooLarge {
		t.Fatalf("want ErrPacketTooLarge, got %v", err)
	}
}

func TestFrameReuse(t *testing.T) {
	var wire bytes.Buffer
	bw := bufio.NewWriter(&wire)
	_ = WriteFramed(bw, []byte{1, 2, 3})
	_ = WriteFramed(bw, []byte{4, 5, 6})
	fr := NewFrameReader(bufio.NewReader(&wire), DefaultMaxPacketLen)
	a, _ := fr.Next()
	ab := append([]byte(nil), a...) // copy before reuse
	b, _ := fr.Next()
	if &a[0] != &b[0] {
		t.Log("buffer was reallocated (acceptable)")
	}
	if !bytes.Equal(ab, []byte{1, 2, 3}) {
		t.Fatal("first payload was clobbered before copy")
	}
}
