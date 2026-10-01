package server

import (
	"testing"

	"github.com/masgzy/gopherite/protocol"
)

// readSimpleBitStorage mirrors vanilla SimpleBitStorage.get(): entries are
// packed contiguously at bit offset index*bits and straddle long boundaries.
func readSimpleBitStorage(longs []uint64, index, bits int) uint64 {
	bitIndex := index * bits
	cell, start := bitIndex/64, bitIndex%64
	v := longs[cell] >> start
	if start+bits > 64 {
		v |= longs[cell+1] << (64 - start)
	}
	return v & (1<<bits - 1)
}

func TestPackUniformMatchesSimpleBitStorage(t *testing.T) {
	// 9 bits, 256 entries -> ceil(256/7) = 37 longs, exactly the vanilla
	// heightmap geometry.
	longs := make([]uint64, 37)
	packUniform(longs, 256, 9, 4)
	for i := 0; i < 256; i++ {
		if got := readSimpleBitStorage(longs, i, 9); got != 4 {
			t.Fatalf("entry %d = %d, want 4", i, got)
		}
	}
	// Entry 7 and 8 sit across the long0/long1 boundary; a naive aligned
	// packer returns 8/0 here. Reassert explicitly.
	if e7 := readSimpleBitStorage(longs, 7, 9); e7 != 4 {
		t.Fatalf("straddled entry 7 = %d, want 4", e7)
	}
	// 4-bit block storage: 4096 entries, 256 longs, no straddling.
	bl := make([]uint64, 256)
	packUniform(bl, 4096, 4, 0xA)
	for i := 0; i < 4096; i++ {
		if got := readSimpleBitStorage(bl, i, 4); got != 0xA {
			t.Fatalf("block entry %d = %d, want 10", i, got)
		}
	}
}

// decodeChunkPayload parses the cached Level Chunk with Light payload the
// server produces and asserts the invariants a vanilla client relies on.
func TestChunkDataInvariants(t *testing.T) {
	w := newWorld(0)
	data, err := w.chunkData(3, -7)
	if err != nil {
		t.Fatalf("chunkData: %v", err)
	}
	r := protocol.NewReader(data)
	cx, _ := r.Int32()
	cz, _ := r.Int32()
	if cx != 3 || cz != -7 {
		t.Fatalf("chunk coords %d,%d", cx, cz)
	}

	// Heightmaps: exactly the three client-facing types.
	n, _ := r.VarInt()
	if n != 3 {
		t.Fatalf("heightmap count %d, want 3", n)
	}
	wantTypes := map[int32]bool{1: false, 4: false, 5: false}
	for i := int32(0); i < n; i++ {
		typ, _ := r.VarInt()
		if _, seen := wantTypes[typ]; !seen {
			t.Fatalf("unexpected heightmap type %d", typ)
		}
		wantTypes[typ] = true
		lc, _ := r.VarInt()
		if lc != 37 {
			t.Fatalf("heightmap long count %d, want 37", lc)
		}
		for j := int32(0); j < lc; j++ {
			if _, err := r.Int64(); err != nil {
				t.Fatal(err)
			}
		}
	}
	for typ, seen := range wantTypes {
		if !seen {
			t.Fatalf("missing heightmap type %d", typ)
		}
	}

	// Section buffer.
	size, _ := r.VarInt()
	buf, err := r.FixedBytes(int(size))
	if err != nil {
		t.Fatalf("section buffer truncated: %v", err)
	}
	if int32(len(buf)) != size {
		t.Fatalf("section buffer length %d, want %d", len(buf), size)
	}
	sr := protocol.NewReader(buf)
	for i := 0; i < sectionsCount; i++ {
		nonAir, _ := sr.Int16()
		fluids, _ := sr.Int16()
		if fluids != 0 {
			t.Fatalf("section %d fluid count %d", i, fluids)
		}
		bits, _ := sr.Byte()
		if i == 0 {
			if nonAir != 4*16*16 {
				t.Fatalf("floor non-air count %d, want 1024", nonAir)
			}
			if bits != 4 {
				t.Fatalf("floor bits %d, want 4", bits)
			}
			pal, _ := sr.VarInt()
			if pal != 4 {
				t.Fatalf("floor palette size %d, want 4", pal)
			}
			ids := map[int32]bool{}
			for k := int32(0); k < pal; k++ {
				id, _ := sr.VarInt()
				ids[id] = true
			}
			for _, want := range []int32{stateBedrock, stateDirt, stateGrassBlock, stateAir} {
				if !ids[want] {
					t.Fatalf("floor palette missing state %d", want)
				}
			}
			// Packed values are palette indices: bedrock/dirt/dirt/grass
			// for the first four layers, air above.
			wantIdx := func(y int32) uint64 {
				switch y {
				case 0:
					return 0
				case 1, 2:
					return 1
				case 3:
					return 2
				default:
					return 3
				}
			}
			for l := 0; l < 256; l++ {
				v, err := sr.Int64()
				if err != nil {
					t.Fatal(err)
				}
				for s := int32(0); s < 16; s++ {
					i := int32(l)*16 + s
					got := (uint64(v) >> uint(s*4)) & 0xF
					if got != wantIdx(i>>8) {
						t.Fatalf("floor block %d (y=%d): want idx %d, got %d", i, i>>8, wantIdx(i>>8), got)
					}
				}
			}
		} else {
			if nonAir != 0 {
				t.Fatalf("section %d non-air count %d, want 0", i, nonAir)
			}
			if bits != 0 {
				t.Fatalf("section %d bits %d, want 0 (single value)", i, bits)
			}
			if id, _ := sr.VarInt(); id != stateAir {
				t.Fatalf("section %d single value %d, want air", i, id)
			}
		}
		// Biome container: single-value plains.
		bbits, _ := sr.Byte()
		if bbits != 0 {
			t.Fatalf("section %d biome bits %d", i, bbits)
		}
		if bid, _ := sr.VarInt(); bid != biomePlains {
			t.Fatalf("section %d biome %d, want plains(%d)", i, bid, biomePlains)
		}
	}
	if sr.Remaining() != 0 {
		t.Fatalf("section buffer has %d trailing bytes", sr.Remaining())
	}

	// Block entities: none.
	be, _ := r.VarInt()
	if be != 0 {
		t.Fatalf("block entities %d, want 0", be)
	}

	// Light data: bitsets + sky arrays only.
	readBitSet := func() uint64 {
		l, _ := r.VarInt()
		if l != 1 {
			t.Fatalf("bitset long count %d", l)
		}
		v, _ := r.Int64()
		return uint64(v)
	}
	sky := readBitSet()
	_ = readBitSet()      // block mask: empty
	_ = readBitSet()      // empty sky mask
	_ = readBitSet()      // empty block mask
	if sky != 0x3FFFFFF { // sections -1..24 all present
		t.Fatalf("sky mask %#x, want all 26 sections", sky)
	}
	cnt, _ := r.VarInt()
	if cnt != sectionsCount+2 {
		t.Fatalf("sky update count %d, want %d", cnt, sectionsCount+2)
	}
	for i := int32(0); i < cnt; i++ {
		l, _ := r.VarInt()
		if l != 2048 {
			t.Fatalf("sky array %d size %d", i, l)
		}
		for j := int32(0); j < 2048; j++ {
			b, err := r.Byte()
			if err != nil {
				t.Fatal(err)
			}
			if b != 0xFF {
				t.Fatalf("sky array %d byte %d = %#x", i, j, b)
			}
		}
	}
	if n, _ := r.VarInt(); n != 0 {
		t.Fatalf("block update count %d, want 0", n)
	}
	if r.Remaining() != 0 {
		t.Fatalf("payload has %d trailing bytes", r.Remaining())
	}
}

func TestChunkCacheReuse(t *testing.T) {
	w := newWorld(0)
	a, _ := w.chunkData(1, 1)
	b, _ := w.chunkData(1, 1)
	if &a[0] != &b[0] {
		t.Fatalf("cache returned a fresh copy instead of the stored payload")
	}
	c, _ := w.chunkData(2, 1)
	if &a[0] == &c[0] {
		t.Fatalf("distinct chunks share a cache entry")
	}
}
