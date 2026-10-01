package server

import (
	"github.com/masgzy/gopherite/protocol"
)

// Mutable chunk model for M3: the world stores real sections whose
// blocks can change; chunk payloads are serialised from the model with
// live heightmaps instead of the M2 constant writer.

// chunk holds one 16×384 column of sections.
type chunk struct {
	cx, cz   int32
	sections [sectionsCount]*section
	serial   []byte // cached packet payload (nil = dirty)
}

// newSuperflatChunk builds the M2 superflat column: bedrock, two dirt
// and a grass surface at the bottom of the world, air above.
func newSuperflatChunk(cx, cz int32) *chunk {
	c := &chunk{cx: cx, cz: cz}
	for si := range c.sections {
		c.sections[si] = newSection(stateAir)
	}
	// Floor section replicates the M2 palette layout exactly:
	// [bedrock, dirt, grass_block, air] at 4 bits per entry.
	floor := &section{
		bits:    minBits,
		palette: []int32{stateBedrock, stateDirt, stateGrassBlock, stateAir},
		data: buildData(minBits, func(cell int) int {
			switch y := cell >> 8; y {
			case 0:
				return 0
			case 1, 2:
				return 1
			case 3:
				return 2
			default:
				return 3
			}
		}),
	}
	c.sections[0] = floor
	return c
}

// getBlock returns the state id at world coordinates.
func (c *chunk) getBlock(x, y, z int) int32 {
	if y < minY || y >= maxY {
		return stateAir
	}
	s := c.sections[(y-minY)/16]
	return s.get(sectionIndex(x, y, z))
}

// setBlock writes a state and returns true when the chunk changed.
func (c *chunk) setBlock(x, y, z int, state int32) bool {
	if y < minY || y >= maxY {
		return false
	}
	si := (y - minY) / 16
	s := c.sections[si]
	if s.get(sectionIndex(x, y, z)) == state {
		return false
	}
	s.set(sectionIndex(x, y, z), state)
	c.serial = nil
	return true
}

// sectionIndex packs in-section coordinates: y<<8 | z<<4 | x.
func sectionIndex(x, y, z int) int {
	return ((y-minY)&15)<<8 | z&15<<4 | x&15
}

// heightAt returns the y of the highest non-air block of a column plus
// one (the heightmap convention), or minY when the column is empty.
func (c *chunk) heightAt(x, z int) int32 {
	for y := maxY - 1; y >= minY; y-- {
		if c.getBlock(x, y, z) != stateAir {
			return int32(y - minY + 1)
		}
	}
	return 0
}

// payload serialises the Level Chunk packet body (coords included) for
// this chunk, caching until the next mutation.
func (c *chunk) payload() []byte {
	if c.serial != nil {
		return c.serial
	}
	body := protocol.NewWriter()
	body.Int32(c.cx)
	body.Int32(c.cz)
	c.writeData(body)
	writeLightData(body)
	c.serial = body.Bytes()
	return c.serial
}

// writeData serialises heightmaps, the section buffer and block
// entities (none).
func (c *chunk) writeData(body *protocol.Writer) {
	// Heightmaps: three client-facing types; superflat has no leaves or
	// fluids so WORLD_SURFACE, MOTION_BLOCKING and NO_LEAVES agree.
	const heightmapBits = 9
	longs := make([]uint64, 37)
	for z := 0; z < 16; z++ {
		for x := 0; x < 16; x++ {
			h := uint64(c.heightAt(x, z))
			cell := z*16 + x
			bitIndex := cell * heightmapBits
			l, bit := bitIndex/64, uint(bitIndex%64)
			longs[l] |= h << bit
			if bit+heightmapBits > 64 {
				longs[l+1] |= h >> (64 - bit)
			}
		}
	}
	body.VarInt(3)
	for _, typ := range []int32{1, 4, 5} {
		body.VarInt(typ)
		body.VarInt(int32(len(longs)))
		for _, l := range longs {
			body.Int64(int64(l))
		}
	}

	var sec protocol.Writer
	for _, s := range c.sections {
		sec.Int16(s.nonAirCount())
		sec.Int16(0) // fluid count: superflat has no fluids
		s.serialize(&sec)
		writeSingleValueBiome(&sec)
	}
	payload := sec.Bytes()
	body.VarInt(int32(len(payload)))
	body.FixedBytes(payload)
	body.VarInt(0) // block entities
}
