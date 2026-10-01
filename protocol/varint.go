// Package protocol implements the Minecraft Java Edition network protocol
// primitives: variable-length integers, packet buffers and packet framing.
//
// All hot-path helpers are allocation-free: they operate on caller-owned
// byte slices so the packet pipeline can run on pooled buffers.
package protocol

import "errors"

const (
	// MaxVarIntLen is the maximum encoded size of a VarInt in bytes.
	MaxVarIntLen = 5

	// MaxVarLongLen is the maximum encoded size of a VarLong in bytes.
	MaxVarLongLen = 10

	// DefaultMaxPacketLen is the maximum packet payload length accepted
	// by the server (matches the vanilla server default of 2 MiB).
	DefaultMaxPacketLen = 1 << 21
)

var (
	// ErrVarIntTooBig indicates a VarInt exceeded MaxVarIntLen bytes.
	ErrVarIntTooBig = errors.New("protocol: varint exceeds 5 bytes")

	// ErrVarLongTooBig indicates a VarLong exceeded MaxVarLongLen bytes.
	ErrVarLongTooBig = errors.New("protocol: varlong exceeds 10 bytes")

	// ErrPacketTooLarge indicates a framed packet exceeded the negotiated
	// maximum payload length.
	ErrPacketTooLarge = errors.New("protocol: packet too large")

	// ErrNegativeLength indicates a length-prefixed field was negative.
	ErrNegativeLength = errors.New("protocol: negative length prefix")
)

// AppendVarInt appends the VarInt encoding of v to dst and returns the
// extended slice. This never allocates when dst has spare capacity.
func AppendVarInt(dst []byte, v int32) []byte {
	u := uint32(v)
	for u&^0x7F != 0 {
		dst = append(dst, byte(u)|0x80)
		u >>= 7
	}
	return append(dst, byte(u))
}

// AppendVarLong appends the VarLong encoding of v to dst and returns the
// extended slice. This never allocates when dst has spare capacity.
func AppendVarLong(dst []byte, v int64) []byte {
	u := uint64(v)
	for u&^0x7F != 0 {
		dst = append(dst, byte(u)|0x80)
		u >>= 7
	}
	return append(dst, byte(u))
}

// SizeVarInt returns the number of bytes required to encode v as a VarInt.
func SizeVarInt(v int32) int {
	u := uint32(v)
	n := 1
	for u&^0x7F != 0 {
		u >>= 7
		n++
	}
	return n
}
