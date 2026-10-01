package java

import (
	"fmt"

	"github.com/masgzy/gopherite/protocol"
)

// Full named-binary-tag tree for the Anvil save format (M4). The wire
// side keeps its hand-rolled single-purpose encoders (component.go,
// registry NBT); this file is the general reader/writer for disk.

// NBT type ids.
const (
	TagEnd       byte = 0
	TagByte      byte = 1
	TagShort     byte = 2
	TagInt       byte = 3
	TagLong      byte = 4
	TagFloat     byte = 5
	TagDouble    byte = 6
	TagByteArray byte = 7
	TagString    byte = 8
	TagList      byte = 9
	TagCompound  byte = 10
	TagIntArray  byte = 11
	TagLongArray byte = 12
)

// NBT tree nodes. Scalars stay unboxed inside NbtAny for compact code;
// Byte/Short/Int/Long all widen to int64, Float/Double to float64.
type (
	NbtAny struct {
		Type  byte
		Num   int64   // byte/short/int/long payloads
		Float float64 // float/double payloads
		Str   string
		Bytes []byte   // byte array
		Ints  []int32  // int array
		Longs []int64  // long array
		List  []NbtAny // list elements (uniform type in Type)
		Comp  *NbtComp
	}

	NbtComp struct {
		Fields map[string]NbtAny
		Order  []string // write order (vanilla parity where it matters)
	}
)

// ---- construction helpers ----

func NbtByte(v int64) NbtAny     { return NbtAny{Type: TagByte, Num: v} }
func NbtShort(v int64) NbtAny    { return NbtAny{Type: TagShort, Num: v} }
func NbtInt(v int64) NbtAny      { return NbtAny{Type: TagInt, Num: v} }
func NbtLong(v int64) NbtAny     { return NbtAny{Type: TagLong, Num: v} }
func NbtFloat(v float64) NbtAny  { return NbtAny{Type: TagFloat, Float: v} }
func NbtDouble(v float64) NbtAny { return NbtAny{Type: TagDouble, Float: v} }
func NbtString(v string) NbtAny  { return NbtAny{Type: TagString, Str: v} }
func NbtBytes(v []byte) NbtAny   { return NbtAny{Type: TagByteArray, Bytes: v} }
func NbtInts(v []int32) NbtAny   { return NbtAny{Type: TagIntArray, Ints: v} }
func NbtLongs(v []int64) NbtAny  { return NbtAny{Type: TagLongArray, Longs: v} }
func NbtListOf(t byte, v []NbtAny) NbtAny {
	return NbtAny{Type: TagList, List: v, Num: int64(t)}
}
func NbtEmptyList(t byte) NbtAny { return NbtAny{Type: TagList, Num: int64(t)} }

func NewNbtComp() *NbtComp {
	return &NbtComp{Fields: make(map[string]NbtAny)}
}

// Set stores a field, remembering the insertion order.
func (c *NbtComp) Set(name string, v NbtAny) *NbtComp {
	if _, ok := c.Fields[name]; !ok {
		c.Order = append(c.Order, name)
	}
	c.Fields[name] = v
	return c
}

// Get fetches a field (zero NbtAny when absent).
func (c *NbtComp) Get(name string) NbtAny { return c.Fields[name] }

// Has reports whether the field exists.
func (c *NbtComp) Has(name string) bool { _, ok := c.Fields[name]; return ok }

// ---- writer ----

// WriteNbtFile serialises a root compound in the on-disk form: named root
// (empty name, matching modern vanilla region files).
func WriteNbtFile(w *protocol.Writer, root *NbtComp) {
	w.Byte(TagCompound)
	nbtWriteName(w, "")
	nbtWritePayload(w, NbtAny{Type: TagCompound, Comp: root})
}

// nbtWriteName writes a TAG_String field prefix: u16 length + bytes.
func nbtWriteName(w *protocol.Writer, name string) {
	b := []byte(name)
	w.Uint16(uint16(len(b)))
	w.FixedBytes(b)
}

func nbtWritePayload(w *protocol.Writer, v NbtAny) {
	switch v.Type {
	case TagByte:
		w.Byte(byte(v.Num))
	case TagShort:
		w.Int16(int16(v.Num))
	case TagInt:
		w.Int32(int32(v.Num))
	case TagLong:
		w.Int64(v.Num)
	case TagFloat:
		w.Float(float32(v.Float))
	case TagDouble:
		w.Double(v.Float)
	case TagByteArray:
		w.Int32(int32(len(v.Bytes)))
		w.FixedBytes(v.Bytes)
	case TagString:
		nbtWriteName(w, v.Str)
	case TagList:
		et := byte(v.Num)
		w.Byte(et)
		w.Int32(int32(len(v.List)))
		for _, e := range v.List {
			e.Type = et
			nbtWritePayload(w, e)
		}
	case TagCompound:
		c := v.Comp
		for _, name := range c.Order {
			f := c.Fields[name]
			w.Byte(f.Type)
			nbtWriteName(w, name)
			nbtWritePayload(w, f)
		}
		w.Byte(TagEnd)
	case TagIntArray:
		w.Int32(int32(len(v.Ints)))
		for _, n := range v.Ints {
			w.Int32(n)
		}
	case TagLongArray:
		w.Int32(int32(len(v.Longs)))
		for _, n := range v.Longs {
			w.Int64(n)
		}
	}
}

// ---- reader ----

// ReadNbtFile parses the on-disk form and returns the root compound plus
// the number of bytes consumed (advancing r past the payload).
func ReadNbtFile(r *protocol.Reader) (*NbtComp, error) {
	t, err := r.Byte()
	if err != nil {
		return nil, err
	}
	if t != TagCompound {
		return nil, fmt.Errorf("nbt: file root 0x%x, want compound", t)
	}
	name, err := nbtReadName(r)
	if err != nil {
		return nil, err
	}
	v, err := nbtReadPayload(r, TagCompound)
	if err != nil {
		return nil, err
	}
	_ = name
	return v.Comp, nil
}

func nbtReadName(r *protocol.Reader) (string, error) {
	n, err := r.Uint16()
	if err != nil {
		return "", err
	}
	b, err := r.FixedBytes(int(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func nbtReadPayload(r *protocol.Reader, t byte) (NbtAny, error) {
	switch t {
	case TagByte:
		b, err := r.Byte()
		return NbtAny{Type: t, Num: int64(int8(b))}, err
	case TagShort:
		n, err := r.Int16()
		return NbtAny{Type: t, Num: int64(n)}, err
	case TagInt:
		n, err := r.Int32()
		return NbtAny{Type: t, Num: int64(n)}, err
	case TagLong:
		n, err := r.Int64()
		return NbtAny{Type: t, Num: n}, err
	case TagFloat:
		f, err := r.Float()
		return NbtAny{Type: t, Float: float64(f)}, err
	case TagDouble:
		f, err := r.Double()
		return NbtAny{Type: t, Float: f}, err
	case TagByteArray:
		n, err := r.Int32()
		if err != nil {
			return NbtAny{}, err
		}
		if n < 0 || n > 1<<26 {
			return NbtAny{}, fmt.Errorf("nbt: byte array length %d", n)
		}
		b, err := r.FixedBytes(int(n))
		return NbtAny{Type: t, Bytes: b}, err
	case TagString:
		s, err := nbtReadName(r)
		return NbtAny{Type: t, Str: s}, err
	case TagList:
		et, err := r.Byte()
		if err != nil {
			return NbtAny{}, err
		}
		n, err := r.Int32()
		if err != nil {
			return NbtAny{}, err
		}
		if n < 0 || n > 1<<22 {
			return NbtAny{}, fmt.Errorf("nbt: list length %d", n)
		}
		out := NbtAny{Type: t, Num: int64(et), List: make([]NbtAny, 0, n)}
		for i := int32(0); i < n; i++ {
			e, err := nbtReadPayload(r, et)
			if err != nil {
				return NbtAny{}, err
			}
			e.Type = et
			out.List = append(out.List, e)
		}
		return out, nil
	case TagCompound:
		c := NewNbtComp()
		for {
			ft, err := r.Byte()
			if err != nil {
				return NbtAny{}, err
			}
			if ft == TagEnd {
				return NbtAny{Type: t, Comp: c}, nil
			}
			name, err := nbtReadName(r)
			if err != nil {
				return NbtAny{}, err
			}
			fv, err := nbtReadPayload(r, ft)
			if err != nil {
				return NbtAny{}, err
			}
			c.Set(name, fv)
		}
	case TagIntArray:
		n, err := r.Int32()
		if err != nil {
			return NbtAny{}, err
		}
		if n < 0 || n > 1<<24 {
			return NbtAny{}, fmt.Errorf("nbt: int array length %d", n)
		}
		out := make([]int32, n)
		for i := range out {
			v, err := r.Int32()
			if err != nil {
				return NbtAny{}, err
			}
			out[i] = v
		}
		return NbtAny{Type: t, Ints: out}, nil
	case TagLongArray:
		n, err := r.Int32()
		if err != nil {
			return NbtAny{}, err
		}
		if n < 0 || n > 1<<23 {
			return NbtAny{}, fmt.Errorf("nbt: long array length %d", n)
		}
		out := make([]int64, n)
		for i := range out {
			v, err := r.Int64()
			if err != nil {
				return NbtAny{}, err
			}
			out[i] = v
		}
		return NbtAny{Type: t, Longs: out}, nil
	}
	return NbtAny{}, fmt.Errorf("nbt: unknown tag 0x%x", t)
}
