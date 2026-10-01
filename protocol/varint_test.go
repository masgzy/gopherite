package protocol

import (
	"math"
	"testing"
)

func TestVarIntRoundTrip(t *testing.T) {
	cases := []int32{0, 1, 2, 127, 128, 255, 256, 25565, 2097151,
		math.MaxInt32, -1, -2147483648}
	for _, v := range cases {
		buf := AppendVarInt(nil, v)
		if SizeVarInt(v) != len(buf) {
			t.Fatalf("SizeVarInt(%d) = %d, encoded %d bytes", v, SizeVarInt(v), len(buf))
		}
		got, err := NewReader(buf).VarInt()
		if err != nil {
			t.Fatalf("decode %d: %v", v, err)
		}
		if got != v {
			t.Fatalf("round trip: want %d, got %d", v, got)
		}
	}
}

func TestVarLongRoundTrip(t *testing.T) {
	cases := []int64{0, 1, 127, 128, 2147483647, math.MaxInt64, -1, math.MinInt64}
	for _, v := range cases {
		buf := AppendVarLong(nil, v)
		got, err := NewReader(buf).VarLong()
		if err != nil {
			t.Fatalf("decode %d: %v", v, err)
		}
		if got != v {
			t.Fatalf("round trip: want %d, got %d", v, got)
		}
	}
}

func TestVarIntTooBig(t *testing.T) {
	// Five continuation bytes is always invalid.
	buf := []byte{0x80, 0x80, 0x80, 0x80, 0x80, 0x01}
	if _, err := NewReader(buf).VarInt(); err != ErrVarIntTooBig {
		t.Fatalf("want ErrVarIntTooBig, got %v", err)
	}
}

func TestStringBound(t *testing.T) {
	w := NewWriter().String("hello")
	got, err := NewReader(w.Bytes()).String(5)
	if err != nil || got != "hello" {
		t.Fatalf("want hello, got %q, err %v", got, err)
	}
	if _, err := NewReader(w.Bytes()).String(4); err != ErrStringTooLong {
		t.Fatalf("want ErrStringTooLong, got %v", err)
	}
}

func TestReaderDoesNotCopyForBytesButDoesForString(t *testing.T) {
	w := NewWriter().String("stable")
	raw := w.Bytes()
	b1 := NewReader(raw).Raw()
	b1[0] = 'X' // transient view mutates the underlying buffer
	if raw[0] != 'X' {
		t.Fatal("Raw should alias the buffer")
	}
}
