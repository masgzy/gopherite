package server

import (
	"sync"

	"github.com/masgzy/gopherite/protocol"
)

// world holds the M3 mutable chunk map. The initial terrain is the M2
// superflat plane (bedrock + 2 dirt + grass at world bottom); blocks can
// change at runtime through setBlock.
type world struct {
	seed   int64
	mu     sync.Mutex
	chunks map[[2]int32]*chunk
}

func newWorld(seed int64) *world {
	return &world{seed: seed, chunks: make(map[[2]int32]*chunk)}
}

// clockID returns the baked registry index of a world clock (overworld=0,
// the_end=1 in vanilla order) plus one for the holder reference form.
func (w *world) clockID(name string) int32 {
	if name == "the_end" {
		return 2
	}
	return 1
}

// vanilla world geometry.
const (
	minY          = -64
	maxY          = 320
	sectionsCount = (maxY - minY) / 16 // 24
	sectionBlocks = 4096
	sectionBiomes = 64
)

// superflat block state ids for 26.2, from the official blocks report.
const (
	stateAir   = 0
	stateStone = 1
	// grass_block default (snowy=false). State 8 is the SNOWY variant; the
	// M2 constant pointed at it, which real clients render as snow-covered.
	stateGrassBlock = 9
	stateDirt       = 10
	stateBedrock    = 85
)

// biome index for minecraft:plains inside the vanilla sorted biome list.
const biomePlains = 40

// chunk returns the chunk for a position, generating the superflat
// column on first access.
func (w *world) chunkAt(cx, cz int32) *chunk {
	key := [2]int32{cx, cz}
	w.mu.Lock()
	defer w.mu.Unlock()
	c, ok := w.chunks[key]
	if !ok {
		c = newSuperflatChunk(cx, cz)
		w.chunks[key] = c
	}
	return c
}

// chunkData serialises the Level Chunk payload for one chunk column.
func (w *world) chunkData(cx, cz int32) ([]byte, error) {
	return w.chunkAt(cx, cz).payload(), nil
}

// setBlock writes a block state and returns true when anything changed.
func (w *world) setBlock(x, y, z int, state int32) bool {
	c := w.chunkAt(int32(x>>4), int32(z>>4))
	return c.setBlock(x&15, y, z&15, state)
}

// getBlock reads a block state at world coordinates.
func (w *world) getBlock(x, y, z int) int32 {
	return w.chunkAt(int32(x>>4), int32(z>>4)).getBlock(x&15, y, z&15)
}

// writeSingleValueBiome encodes the biome container: plains everywhere.
func writeSingleValueBiome(w *protocol.Writer) {
	w.Byte(0)
	w.VarInt(biomePlains)
}

// writeLightData writes the sky/block light data. The superflat world has
// no light blocking: every sky light section is uniformly 15. Vanilla
// marks such sections in the sky Y mask and sends 2048 bytes of 0xFF each;
// block light sections carry no data (mask empty, value 0 default).
func writeLightData(body *protocol.Writer) {
	// Light sections span -1..sectionsCount inclusive: 26 sections for
	// vanilla world height.
	const lightSections = sectionsCount + 2
	writeBitSet := func(mask uint64) {
		body.VarInt(1)
		body.Int64(int64(mask))
	}
	var skyMask, blockMask uint64
	for i := 0; i < lightSections; i++ {
		skyMask |= 1 << i // all sky sections have (uniform) data
	}
	writeBitSet(skyMask)
	writeBitSet(blockMask)
	writeBitSet(0) // empty sky sections: none
	writeBitSet(0) // empty block sections: none

	// Sky updates: 2048 bytes of 0xFF per section (sky light 15).
	body.VarInt(lightSections)
	for i := 0; i < lightSections; i++ {
		body.VarInt(2048)
		for j := 0; j < 64; j++ {
			body.FixedBytes(ff32[:]) // 64 * 32 = 2048
		}
	}
	// Block updates: none.
	body.VarInt(0)
}

// ff32 is 32 bytes of 0xFF: eight 64-value runs of sky light 15.
var ff32 = func() [32]byte {
	var b [32]byte
	for i := range b {
		b[i] = 0xFF
	}
	return b
}()
