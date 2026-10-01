package protocol

import (
	"bytes"
	"compress/zlib"
	"io"
)

// CompressionLayer wraps the frame codec with the vanilla zlib framing used
// after a Set Compression packet: each frame becomes
// VarInt(uncompressedLen) + payload, where the payload is zlib-compressed
// when uncompressedLen meets or exceeds the threshold and raw otherwise
// (with uncompressedLen written as zero). A threshold of -1 disables
// compression.
type CompressionLayer struct {
	threshold int32
	zbuf      bytes.Buffer
}

// NewCompressionLayer returns a layer with the negotiated threshold.
func NewCompressionLayer(threshold int32) *CompressionLayer {
	return &CompressionLayer{threshold: threshold}
}

// Threshold returns the configured compression threshold.
func (c *CompressionLayer) Threshold() int32 { return c.threshold }

// CompressInner produces the compression-layer frame body: the
// uncompressed-length VarInt followed by either a zlib stream or the raw
// body. The outer length VarInt is added later by WriteFramed, after the
// encryption layer has run: vanilla stacks compression *inside* encryption
// (compress, then encrypt, then length-prefix), and the whole compressed
// frame including the size prefix travels encrypted.
func (c *CompressionLayer) CompressInner(body []byte) []byte {
	var payload []byte
	if len(body) >= int(c.threshold) {
		c.zbuf.Reset()
		zw := zlib.NewWriter(&c.zbuf)
		_, _ = zw.Write(body)
		_ = zw.Close()
		payload = c.zbuf.Bytes()
	} else {
		payload = body
	}
	sizePrefix := int32(0)
	if len(body) >= int(c.threshold) {
		sizePrefix = int32(len(body))
	}
	head := make([]byte, 0, SizeVarInt(sizePrefix)+len(payload))
	head = AppendVarInt(head, sizePrefix)
	head = append(head, payload...)
	return head
}

// DecompressFrame inverts CompressFrame for one frame payload. The returned
// slice is freshly allocated when compression was applied (zlib streams
// cannot be read in place) and aliases input when it was sent uncompressed.
func (c *CompressionLayer) DecompressFrame(payload []byte, maxSize int32) ([]byte, error) {
	r := NewReader(payload)
	size, err := r.VarInt()
	if err != nil {
		return nil, err
	}
	if size < 0 || (size != 0 && size > maxSize) {
		return nil, ErrPacketTooLarge
	}
	raw := r.Raw()
	if size == 0 {
		if int32(len(raw)) > maxSize {
			return nil, ErrPacketTooLarge
		}
		return raw, nil
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	lim := &io.LimitedReader{R: zr, N: int64(maxSize) + 1}
	out, err := io.ReadAll(lim)
	if err != nil {
		return nil, err
	}
	if int32(len(out)) > maxSize {
		return nil, ErrPacketTooLarge
	}
	if int32(len(out)) != size {
		return nil, io.ErrUnexpectedEOF
	}
	return out, nil
}
