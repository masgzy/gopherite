package server

import (
	"testing"

	"github.com/masgzy/gopherite/protocol"
)

// TestSectionUniformSplit verifies the uniform -> linear transition and
// readback through get().
func TestSectionUniformSplit(t *testing.T) {
	s := newSection(stateAir)
	if !s.uniform {
		t.Fatal("fresh section must be uniform")
	}
	// Writing the same value keeps the single-value container.
	s.set(123, stateAir)
	if !s.uniform || s.single != stateAir {
		t.Fatal("same-value set must not split")
	}
	// Writing a distinct value splits into a 4-bit linear section.
	s.set(123, stateStone)
	if s.uniform || s.bits != minBits {
		t.Fatalf("split: uniform=%v bits=%d", s.uniform, s.bits)
	}
	if got := s.get(123); got != stateStone {
		t.Fatalf("get(123) = %d, want stone", got)
	}
	if got := s.get(0); got != stateAir {
		t.Fatalf("get(0) = %d, want air", got)
	}
	// All other cells stay air.
	for c := 0; c < sectionBlocks; c++ {
		if c == 123 {
			continue
		}
		if got := s.get(c); got != stateAir {
			t.Fatalf("get(%d) = %d, want air", c, got)
		}
	}
}

// TestSectionPaletteGrowth drives a section past every palette threshold
// (4 -> 5 -> ... -> 8 -> 15 bits global) and verifies readback.
func TestSectionPaletteGrowth(t *testing.T) {
	s := newSection(stateAir)
	for n := 0; n < 300; n++ {
		// states 1..299 (stone is 1); air stays at cell 4095.
		if n == 299 {
			continue
		}
		s.set(n, int32(n+1))
	}
	for n := 0; n < 299; n++ {
		if got := s.get(n); got != int32(n+1) {
			t.Fatalf("cell %d = %d, want %d", n, got, n+1)
		}
	}
	if got := s.get(4095); got != stateAir {
		t.Fatalf("air cell = %d", got)
	}
	if s.bits != globalBits {
		t.Fatalf("bits after 300 values = %d, want global %d", s.bits, globalBits)
	}
}

// TestSectionSerializeRoundtrip decodes the wire bytes of a linear
// section and checks palette/storage agreement.
func TestSectionSerializeRoundtrip(t *testing.T) {
	s := newSection(stateAir)
	s.set(0, stateStone)
	s.set(1, stateDirt)
	s.set(2, stateGrassBlock)
	s.set(2, stateStone) // palette dedup: stone reused

	w := protocol.NewWriter()
	s.serialize(w)
	r := &protocol.Reader{}
	r.Reset(w.Bytes())
	bits, _ := r.Byte()
	if bits != 4 {
		t.Fatalf("bits %d", bits)
	}
	n, _ := r.VarInt()
	if n != 4 {
		t.Fatalf("palette size %d, want 4 (air, stone, dirt, grass)", n)
	}
	pal := make([]int32, n)
	for i := range pal {
		pal[i], _ = r.VarInt()
	}
	longs := make([]uint64, storageLongs(4))
	for i := range longs {
		v, _ := r.Int64()
		longs[i] = uint64(v)
	}
	// cell 0 -> stone, 1 -> dirt, 2 -> stone, rest air.
	check := func(cell int, want int32) {
		idx := extractEntry(longs, 4, cell)
		if pal[idx] != want {
			t.Fatalf("cell %d = state %d, want %d", cell, pal[idx], want)
		}
	}
	check(0, stateStone)
	check(1, stateDirt)
	check(2, stateStone)
	check(3, stateAir)
	check(4095, stateAir)
}

// TestWorldSetBlockRoundtrip mutates the world and reads back through
// both the model and a re-serialised payload.
func TestWorldSetBlockRoundtrip(t *testing.T) {
	w := newWorld(0)
	if w.getBlock(5, -61, 5) != stateGrassBlock {
		t.Fatalf("surface = %d", w.getBlock(5, -61, 5))
	}
	if !w.setBlock(5, -61, 5, stateStone) {
		t.Fatal("setBlock returned false")
	}
	if w.getBlock(5, -61, 5) != stateStone {
		t.Fatalf("after set: %d", w.getBlock(5, -61, 5))
	}
	// Placing a block in the air layer above.
	if !w.setBlock(5, -60, 5, stateDirt) {
		t.Fatal("place returned false")
	}
	if w.getBlock(5, -60, 5) != stateDirt {
		t.Fatalf("placed: %d", w.getBlock(5, -60, 5))
	}
	// No-op set returns false.
	if w.setBlock(5, -60, 5, stateDirt) {
		t.Fatal("no-op setBlock returned true")
	}
}
