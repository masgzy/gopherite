package protocol

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
	"unicode/utf8"
)

// Reader decodes Minecraft protocol fields from a byte slice holding exactly
// one packet payload. Readers do not own the underlying slice: Bytes returns
// memory that is only valid until the buffer is recycled, while String always
// copies.
type Reader struct {
	data []byte
	pos  int
}

// NewReader returns a Reader over b.
func NewReader(b []byte) *Reader { return &Reader{data: b} }

// Remaining returns the number of unread bytes.
func (r *Reader) Remaining() int { return len(r.data) - r.pos }

// Reset re-arms the reader over a new payload.
func (r *Reader) Reset(b []byte) { r.data, r.pos = b, 0 }

func (r *Reader) take(n int) ([]byte, error) {
	if n < 0 || n > r.Remaining() {
		return nil, io.ErrUnexpectedEOF
	}
	out := r.data[r.pos : r.pos+n]
	r.pos += n
	return out, nil
}

// VarInt decodes a VarInt.
func (r *Reader) VarInt() (int32, error) {
	var u uint32
	for i := 0; i < MaxVarIntLen; i++ {
		if r.pos >= len(r.data) {
			return 0, io.ErrUnexpectedEOF
		}
		b := r.data[r.pos]
		r.pos++
		u |= uint32(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int32(u), nil
		}
	}
	return 0, ErrVarIntTooBig
}

// VarLong decodes a VarLong.
func (r *Reader) VarLong() (int64, error) {
	var u uint64
	for i := 0; i < MaxVarLongLen; i++ {
		if r.pos >= len(r.data) {
			return 0, io.ErrUnexpectedEOF
		}
		b := r.data[r.pos]
		r.pos++
		u |= uint64(b&0x7F) << (7 * i)
		if b&0x80 == 0 {
			return int64(u), nil
		}
	}
	return 0, ErrVarLongTooBig
}

// Int32 decodes a big-endian signed int.
func (r *Reader) Int32() (int32, error) {
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.BigEndian.Uint32(b)), nil
}

// Float decodes a big-endian float32.
func (r *Reader) Float() (float32, error) {
	b, err := r.take(4)
	if err != nil {
		return 0, err
	}
	return math.Float32frombits(binary.BigEndian.Uint32(b)), nil
}

// Double decodes a big-endian float64.
func (r *Reader) Double() (float64, error) {
	b, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
}

// Byte decodes a single unsigned byte.
func (r *Reader) Byte() (byte, error) {
	b, err := r.take(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

// UUID decodes a UUID stored as four big-endian int32 words.
func (r *Reader) UUID() ([16]byte, error) {
	var out [16]byte
	b, err := r.take(16)
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	return out, nil
}

// Uint16 decodes a big-endian unsigned short.
func (r *Reader) Uint16() (uint16, error) {
	b, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b), nil
}

// Int64 decodes a big-endian signed long.
func (r *Reader) Int64() (int64, error) {
	b, err := r.take(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.BigEndian.Uint64(b)), nil
}

// Bool decodes a boolean.
func (r *Reader) Bool() (bool, error) {
	b, err := r.take(1)
	if err != nil {
		return false, err
	}
	return b[0] != 0, nil
}

// Bytes decodes a length-prefixed byte array and returns a transient view
// into the underlying packet buffer. Callers must copy if they retain it.
func (r *Reader) Bytes() ([]byte, error) {
	n, err := r.VarInt()
	if err != nil {
		return nil, err
	}
	return r.take(int(n))
}

// String decodes a length-prefixed UTF-8 string with a maximum of maxChars
// Unicode code points (matching the vanilla String(N) bound).
func (r *Reader) String(maxChars int) (string, error) {
	n, err := r.VarInt()
	if err != nil {
		return "", err
	}
	if n < 0 || int(n) > r.Remaining() {
		return "", io.ErrUnexpectedEOF
	}
	b := r.data[r.pos : r.pos+int(n)]
	r.pos += int(n)
	if utf8.RuneCountInString(string(b)) > maxChars {
		return "", ErrStringTooLong
	}
	return string(b), nil // copy: packet buffers are pooled and reused
}

// FixedBytes decodes exactly n raw bytes.
func (r *Reader) FixedBytes(n int) ([]byte, error) { return r.take(n) }

// Raw returns the entire remaining payload as a transient view.
func (r *Reader) Raw() []byte {
	out := r.data[r.pos:]
	r.pos = len(r.data)
	return out
}

// Writer incrementally encodes a packet payload into a growable buffer.
// Zero-value usage is illegal; obtain one from NewWriter or the pool.
type Writer struct {
	buf []byte
}

// NewWriter returns an empty Writer with a small initial capacity.
func NewWriter() *Writer { return &Writer{buf: make([]byte, 0, 64)} }

// Reset empties the writer while retaining its capacity for reuse.
func (w *Writer) Reset() { w.buf = w.buf[:0] }

// Bytes returns the encoded payload.
func (w *Writer) Bytes() []byte { return w.buf }

// Len returns the current payload length.
func (w *Writer) Len() int { return len(w.buf) }

// VarInt encodes a VarInt.
func (w *Writer) VarInt(v int32) *Writer { w.buf = AppendVarInt(w.buf, v); return w }

// VarLong encodes a VarLong.
func (w *Writer) VarLong(v int64) *Writer { w.buf = AppendVarLong(w.buf, v); return w }

// Int32 encodes a big-endian signed int.
func (w *Writer) Int32(v int32) *Writer {
	w.buf = binary.BigEndian.AppendUint32(w.buf, uint32(v))
	return w
}

// Float encodes a big-endian float32.
func (w *Writer) Float(v float32) *Writer {
	w.buf = binary.BigEndian.AppendUint32(w.buf, math.Float32bits(v))
	return w
}

// Double encodes a big-endian float64.
func (w *Writer) Double(v float64) *Writer {
	w.buf = binary.BigEndian.AppendUint64(w.buf, math.Float64bits(v))
	return w
}

// Byte appends a single raw byte.
func (w *Writer) Byte(v byte) *Writer { w.buf = append(w.buf, v); return w }

// FixedBytes appends raw bytes without a length prefix.
func (w *Writer) FixedBytes(b []byte) *Writer { w.buf = append(w.buf, b...); return w }

// UUID encodes a UUID as sixteen raw bytes.
func (w *Writer) UUID(id [16]byte) *Writer { w.buf = append(w.buf, id[:]...); return w }

// OptionalString encodes a string with a presence flag, matching the
// vanilla "optional" wrapper (bool + value).
func (w *Writer) OptionalString(s string, present bool) *Writer {
	if !present {
		return w.Bool(false)
	}
	return w.Bool(true).String(s)
}

// Uint16 encodes a big-endian unsigned short.
func (w *Writer) Uint16(v uint16) *Writer {
	w.buf = binary.BigEndian.AppendUint16(w.buf, v)
	return w
}

// Int64 encodes a big-endian signed long.
func (w *Writer) Int64(v int64) *Writer {
	w.buf = binary.BigEndian.AppendUint64(w.buf, uint64(v))
	return w
}

// Bool encodes a boolean.
func (w *Writer) Bool(v bool) *Writer {
	if v {
		w.buf = append(w.buf, 1)
	} else {
		w.buf = append(w.buf, 0)
	}
	return w
}

// String encodes a length-prefixed UTF-8 string.
func (w *Writer) String(s string) *Writer {
	w.buf = AppendVarInt(w.buf, int32(len(s)))
	w.buf = append(w.buf, s...)
	return w
}

// Raw appends pre-encoded bytes verbatim.
func (w *Writer) Raw(b []byte) *Writer { w.buf = append(w.buf, b...); return w }

// ErrStringTooLong is returned when a decoded string exceeds its bound.
var ErrStringTooLong = errors.New("protocol: string exceeds maximum length")

// Int16 encodes a big-endian signed short.
func (w *Writer) Int16(v int16) *Writer {
	w.buf = binary.BigEndian.AppendUint16(w.buf, uint16(v))
	return w
}

// Int16 decodes a big-endian signed short.
func (r *Reader) Int16() (int16, error) {
	b, err := r.take(2)
	if err != nil {
		return 0, err
	}
	return int16(binary.BigEndian.Uint16(b)), nil
}
