package java

import (
	"math"

	"github.com/masgzy/gopherite/protocol"
)

// LpVec3 is the 26.2 low-precision velocity codec
// (net.minecraft.network.LpVec3). Each vector is quantised relative to its
// chessboard (max-abs) length: the scale shares its low 2 bits with the
// first wire byte and continues in a VarInt when it needs more range, the
// three components carry 15 bits each inside bits 3..47, and a zero vector
// is the single byte 0x00. Wire layout per vanilla read(): one byte, one
// byte, then a big-endian uint32 covering bits 16..47.

const (
	lpMask15       = int64(32767)
	lpMaxQuantum   = int64(32766)
	lpAbsMax       = 1.71798691e10        // vanilla ABS_MAX_VALUE
	lpMinMagnitude = 3.051944088384301e-5 // vanilla ABS_MIN_VALUE
)

func lpSanitize(v float64) float64 {
	if math.IsNaN(v) {
		return 0
	}
	return math.Max(-lpAbsMax, math.Min(lpAbsMax, v))
}

func lpPack(v float64) int64 {
	return int64(math.Round((v*0.5 + 0.5) * float64(lpMaxQuantum)))
}

func lpUnpack(v int64) float64 {
	if v&lpMask15 > lpMaxQuantum {
		v = lpMaxQuantum
	}
	return float64(v&lpMask15)*2.0/float64(lpMaxQuantum) - 1.0
}

// WriteLpVec3 encodes a velocity triple in the 26.2 low-precision form.
func WriteLpVec3(w *protocol.Writer, x, y, z float64) {
	x, y, z = lpSanitize(x), lpSanitize(y), lpSanitize(z)
	chess := math.Max(math.Abs(x), math.Max(math.Abs(y), math.Abs(z)))
	if chess < lpMinMagnitude {
		w.Byte(0)
		return
	}
	scale := int64(math.Ceil(chess))
	partial := scale&3 != scale
	markers := scale
	if partial {
		markers = scale&3 | 4
	}
	buf := uint64(markers) |
		uint64(lpPack(x/float64(scale)))<<3 |
		uint64(lpPack(y/float64(scale)))<<18 |
		uint64(lpPack(z/float64(scale)))<<33
	// Byte order mirrors vanilla: two single bytes then a big-endian int32.
	w.Byte(byte(buf))
	w.Byte(byte(buf >> 8))
	w.Byte(byte(buf >> 40))
	w.Byte(byte(buf >> 32))
	w.Byte(byte(buf >> 24))
	w.Byte(byte(buf >> 16))
	if partial {
		w.VarInt(int32(uint32(scale >> 2)))
	}
}

// ReadLpVec3 decodes a low-precision velocity triple; used to keep the
// stream in sync when parsing serverbound packets that embed one.
func ReadLpVec3(r *protocol.Reader) (float64, float64, float64, error) {
	lowest, err := r.Byte()
	if err != nil {
		return 0, 0, 0, err
	}
	if lowest == 0 {
		return 0, 0, 0, nil
	}
	middle, err := r.Byte()
	if err != nil {
		return 0, 0, 0, err
	}
	var hi uint32
	for shift := 24; shift >= 0; shift -= 8 {
		b, err := r.Byte()
		if err != nil {
			return 0, 0, 0, err
		}
		hi |= uint32(b) << uint(shift)
	}
	buf := uint64(hi)<<16 | uint64(middle)<<8 | uint64(lowest)
	scale := int64(lowest & 3)
	if lowest&4 != 0 {
		cont, err := r.VarInt()
		if err != nil {
			return 0, 0, 0, err
		}
		scale |= int64(uint32(cont)) << 2
	}
	x := lpUnpack(int64(buf>>3)) * float64(scale)
	y := lpUnpack(int64(buf>>18)) * float64(scale)
	z := lpUnpack(int64(buf>>33)) * float64(scale)
	return x, y, z, nil
}
