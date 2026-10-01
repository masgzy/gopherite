package protocol

import (
	"bytes"
	"io"
	"testing"
)

func TestCompressionInnerRoundTrip(t *testing.T) {
	c := NewCompressionLayer(256)

	// Below threshold: stored raw with a zero size prefix.
	small := []byte("hello vanilla client")
	inner := c.CompressInner(small)
	back, err := c.DecompressFrame(inner, DefaultMaxPacketLen)
	if err != nil {
		t.Fatalf("decompress small: %v", err)
	}
	if !bytes.Equal(back, small) {
		t.Fatalf("small roundtrip mismatch: %q", back)
	}

	// Above threshold: zlib-compressed payload with real size prefix.
	big := make([]byte, 4096)
	for i := range big {
		big[i] = byte(i % 251)
	}
	inner = c.CompressInner(big)
	r := NewReader(inner)
	size, err := r.VarInt()
	if err != nil {
		t.Fatalf("size prefix: %v", err)
	}
	if size != int32(len(big)) {
		t.Fatalf("size prefix %d, want %d", size, len(big))
	}
	payload, err := c.DecompressFrame(inner, DefaultMaxPacketLen)
	if err != nil {
		t.Fatalf("decompress big: %v", err)
	}
	if !bytes.Equal(payload, big) {
		t.Fatalf("big roundtrip mismatch (len %d vs %d)", len(payload), len(big))
	}
	// The compressed inner frame must actually shrink a repetitive body.
	if len(inner) >= len(big) {
		t.Fatalf("payload not compressed: %d bytes for %d input", len(inner), len(big))
	}
}

// TestCompressionEncryptedLayering pins the vanilla layering: compress ->
// encrypt -> length prefix, with the whole inner frame encrypted. A server
// that encrypts before compressing (or leaves the size prefix in plaintext)
// breaks every vanilla client.
func TestCompressionEncryptedLayering(t *testing.T) {
	key := make([]byte, 16)
	iv := make([]byte, 16)
	for i := range key {
		key[i] = byte(0xA0 + i)
		iv[i] = byte(0x50 + i)
	}
	enc, _ := NewCFB8Encrypter(key, iv)
	dec, _ := NewCFB8Decrypter(key, iv)
	c := NewCompressionLayer(256)

	body := bytes.Repeat([]byte{0x42}, 1000) // compressible, above threshold

	// Server side: exactly what conn.sendPacket does.
	payload := c.CompressInner(body)
	payload = enc.Crypt(nil, payload)

	// Wire: plaintext length VarInt + encrypted inner frame.
	framed := AppendVarInt(nil, int32(len(payload)))
	framed = append(framed, payload...)

	// Client side: mirror the conn read loop.
	r := NewReader(framed)
	flen, err := r.VarInt()
	if err != nil {
		t.Fatal(err)
	}
	wire := r.Raw()
	if len(wire) != int(flen) {
		t.Fatalf("frame length %d, remaining %d", flen, len(wire))
	}
	plain := dec.Crypt(nil, wire)
	got, err := c.DecompressFrame(plain, DefaultMaxPacketLen)
	if err != nil {
		t.Fatalf("client-side decompress: %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Fatalf("layered roundtrip mismatch")
	}
}

func TestDecompressRejectsOversize(t *testing.T) {
	c := NewCompressionLayer(256)
	big := make([]byte, 2048)
	inner := c.CompressInner(big) // declares 2048 uncompressed
	if _, err := c.DecompressFrame(inner, 1024); err != ErrPacketTooLarge {
		t.Fatalf("want ErrPacketTooLarge, got %v", err)
	}
}

func TestDecompressRejectsLengthMismatch(t *testing.T) {
	c := NewCompressionLayer(256)
	body := bytes.Repeat([]byte{0xAB}, 512)
	inner := c.CompressInner(body)
	// Corrupt the declared uncompressed length: claim 1023 instead.
	varintLen := SizeVarInt(int32(len(body)))
	corrupt := AppendVarInt(nil, 1023)
	corrupt = append(corrupt, inner[varintLen:]...)
	if _, err := c.DecompressFrame(corrupt, DefaultMaxPacketLen); err == nil {
		t.Fatalf("corrupted size prefix accepted")
	}
}

func TestDecompressRejectsGarbageZlib(t *testing.T) {
	c := NewCompressionLayer(256)
	// Declare a compressed size prefix but ship non-zlib bytes.
	inner := AppendVarInt(nil, 100)
	inner = append(inner, bytes.Repeat([]byte{0x00}, 100)...)
	if _, err := c.DecompressFrame(inner, DefaultMaxPacketLen); err == nil {
		t.Fatalf("garbage zlib accepted")
	}
}

// TestCompressionStreamReuse guards the shared zlib buffer against
// interleaving bugs: consecutive frames must stay independent.
func TestCompressionStreamReuse(t *testing.T) {
	c := NewCompressionLayer(64)
	first := c.CompressInner(bytes.Repeat([]byte{1}, 128))
	second := c.CompressInner(bytes.Repeat([]byte{2}, 128))
	f1, err := c.DecompressFrame(first, DefaultMaxPacketLen)
	if err != nil {
		t.Fatal(err)
	}
	f2, err := c.DecompressFrame(second, DefaultMaxPacketLen)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(f1, bytes.Repeat([]byte{1}, 128)) {
		t.Fatalf("first frame corrupted by second encode")
	}
	if !bytes.Equal(f2, bytes.Repeat([]byte{2}, 128)) {
		t.Fatalf("second frame corrupted")
	}
	_ = io.Discard
}
