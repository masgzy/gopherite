package server

import (
	"github.com/masgzy/gopherite/protocol"
)

// Palette-compressed block-state container for one 16³ section.
//
// Wire rules (1.16+ PalettedContainer, unchanged in 26.2):
//   - bits=0: single-value container, one VarInt state, no storage.
//   - linear: bits 4..8 for blocks (4 is the minimum); palette is a
//     VarInt-counted list of global ids, entries packed as palette
//     INDICES. Sections above 256 distinct states upgrade to global.
//   - global: entries are global state ids, bits = the full id space
//     (15 bits for 26.2's 32366 states).
//
// Packing never straddles long boundaries: with b bits per entry,
// floor(64/b) entries fill each long and the remaining bits stay zero.
// (The contiguous SimpleBitStorage layout is only used by heightmaps.)
type section struct {
	uniform bool
	single  int32    // state id when uniform
	palette []int32  // global ids, only in linear mode
	bits    uint8    // bits per entry (0 when uniform)
	data    []uint64 // packed palette indices or global ids
}

// minimum and maximum linear bits for block-state sections.
const (
	minBits     = 4
	maxBits     = 8
	globalBits  = 15 // ceil(log2(blockStateCount)) for 26.2
	entriesPerS = sectionBlocks
)

func newSection(state int32) *section {
	return &section{uniform: true, single: state}
}

// get returns the global state id at cell index i (0..4095,
// index = y<<8 | z<<4 | x).
func (s *section) get(i int) int32 {
	if s.uniform {
		return s.single
	}
	if s.palette == nil {
		return int32(extractEntry(s.data, s.bits, i))
	}
	return s.palette[extractEntry(s.data, s.bits, i)]
}

// set updates one cell, growing or re-packing the palette as needed.
func (s *section) set(i int, state int32) {
	if s.uniform {
		if s.single == state {
			return
		}
		// Split into a linear section: two distinct values -> 4 bits.
		s.uniform = false
		s.palette = []int32{s.single, state}
		s.bits = minBits
		s.data = buildData(minBits, func(cell int) int {
			if cell == i {
				return 1
			}
			return 0
		})
		return
	}
	if s.palette == nil {
		// global mode: direct ids
		s.setPacked(i, uint64(state))
		return
	}
	for idx, v := range s.palette {
		if v == state {
			s.setPacked(i, uint64(idx))
			return
		}
	}
	if len(s.palette) < 1<<s.bits {
		s.palette = append(s.palette, state)
		s.setPacked(i, uint64(len(s.palette)-1))
		return
	}
	// Palette exhausted: widen (or go global above 8 bits).
	s.grow(state)
	s.set(i, state)
}

// grow widens the palette by one bit, switching to global ids beyond the
// linear cap.
func (s *section) grow(extra int32) {
	if s.bits < maxBits {
		nb := s.bits + 1
		indices := make([]int, entriesPerS)
		for c := 0; c < entriesPerS; c++ {
			indices[c] = int(extractEntry(s.data, s.bits, c))
		}
		s.bits = nb
		s.data = buildData(nb, func(c int) int { return indices[c] })
		return
	}
	// linear full at 8 bits -> flatten to global ids
	ids := make([]int, entriesPerS)
	for c := 0; c < entriesPerS; c++ {
		ids[c] = int(s.palette[extractEntry(s.data, s.bits, c)])
	}
	_ = extra
	s.palette = nil
	s.bits = globalBits
	s.data = buildData(globalBits, func(c int) int { return ids[c] })
}

// setPacked writes one entry in the packed storage.
func (s *section) setPacked(i int, v uint64) {
	perLong := int64(64 / s.bits)
	cell := i / int(perLong)
	bit := uint(i%int(perLong)) * uint(s.bits)
	mask := ((uint64(1) << s.bits) - 1) << bit
	s.data[cell] = s.data[cell]&^mask | v<<bit
}

// extractEntry reads one entry from packed storage (non-straddling).
func extractEntry(data []uint64, bits uint8, i int) uint64 {
	perLong := int64(64 / bits)
	cell := i / int(perLong)
	bit := uint(i%int(perLong)) * uint(bits)
	return data[cell] >> bit & (uint64(1)<<bits - 1)
}

// storageLongs returns the packed storage size for a bits value.
func storageLongs(bits uint8) int {
	perLong := int64(64 / bits)
	return (entriesPerS + int(perLong) - 1) / int(perLong)
}

// buildData packs a value-per-cell mapping into non-straddling storage.
func buildData(bits uint8, value func(cell int) int) []uint64 {
	out := make([]uint64, storageLongs(bits))
	perLong := int64(64 / bits)
	for c := 0; c < entriesPerS; c++ {
		cell := c / int(perLong)
		bit := uint(c%int(perLong)) * uint(bits)
		out[cell] |= uint64(value(c)) << bit
	}
	return out
}

// nonAirCount counts the cells holding a non-air state (the section
// block counter on the wire).
func (s *section) nonAirCount() int16 {
	n := 0
	for i := 0; i < sectionBlocks; i++ {
		if s.get(i) != stateAir {
			n++
		}
	}
	return int16(n)
}

// serialize writes the container in the 26.2 chunk wire format.
func (s *section) serialize(w *protocol.Writer) {
	switch {
	case s.uniform:
		w.Byte(0)
		w.VarInt(s.single)
	case s.palette != nil:
		w.Byte(s.bits)
		w.VarInt(int32(len(s.palette)))
		for _, v := range s.palette {
			w.VarInt(v)
		}
		for _, l := range s.data {
			w.Int64(int64(l))
		}
	default:
		w.Byte(s.bits)
		for _, l := range s.data {
			w.Int64(int64(l))
		}
	}
}
