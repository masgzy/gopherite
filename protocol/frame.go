package protocol

import (
	"bufio"
	"io"
)

// FrameReader reads length-prefixed packets from a buffered stream.
// The payload buffer is owned by the FrameReader and reused between Next
// calls: callers must finish decoding (or copy) before calling Next again.
type FrameReader struct {
	br       *bufio.Reader
	maxLen   int32
	scratch  []byte
	capacity int
}

// NewFrameReader returns a FrameReader with the given maximum packet length.
func NewFrameReader(br *bufio.Reader, maxLen int32) *FrameReader {
	if maxLen <= 0 {
		maxLen = DefaultMaxPacketLen
	}
	return &FrameReader{br: br, maxLen: maxLen}
}

// Next blocks until a complete packet payload is available and returns it.
// The returned slice is only valid until the next call to Next.
func (f *FrameReader) Next() ([]byte, error) {
	length, err := readVarInt(f.br)
	if err != nil {
		return nil, err
	}
	if length < 0 || length > f.maxLen {
		return nil, ErrPacketTooLarge
	}
	if int32(cap(f.scratch)) < length {
		f.scratch = make([]byte, length)
		f.capacity = len(f.scratch)
	} else {
		f.scratch = f.scratch[:length]
	}
	if _, err := io.ReadFull(f.br, f.scratch); err != nil {
		return nil, err
	}
	return f.scratch, nil
}

// WriteFramed writes a length-prefixed packet to bw and flushes it.
// The length prefix is encoded on the stack: no allocation per packet.
func WriteFramed(bw *bufio.Writer, body []byte) error {
	var prefix [MaxVarIntLen]byte
	n := SizeVarInt(int32(len(body)))
	putVarInt(prefix[:n], int32(len(body)))
	if _, err := bw.Write(prefix[:n]); err != nil {
		return err
	}
	if _, err := bw.Write(body); err != nil {
		return err
	}
	return bw.Flush()
}

// readVarInt decodes a VarInt from an io.ByteReader (e.g. bufio.Reader).
func readVarInt(r io.ByteReader) (int32, error) {
	var u uint32
	for i := 0; i < MaxVarIntLen; i++ {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		u |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int32(u), nil
		}
	}
	return 0, ErrVarIntTooBig
}

// putVarInt writes the VarInt encoding of v into dst (which must have size
// at least SizeVarInt(v)) and returns the encoded length.
func putVarInt(dst []byte, v int32) int {
	u := uint32(v)
	i := 0
	for u&^0x7F != 0 {
		dst[i] = byte(u) | 0x80
		u >>= 7
		i++
	}
	dst[i] = byte(u)
	return i + 1
}
