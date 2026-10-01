package server

import (
	"bytes"
	"compress/zlib"
	"sort"
	"sync"

	"github.com/masgzy/gopherite/protocol"
)

// world models the M2 superflat overworld: an infinite plane of
// bedrock + 2 dirt + grass at the bottom of the vanilla world height.
type world struct {
	seed   int64
	mu     sync.Mutex
	caches map[[2]int32][]byte // compressed chunk payloads keyed by chunk pos
}

func newWorld(seed int64) *world {
	return &world{seed: seed, caches: make(map[[2]int32][]byte)}
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
	stateAir        = 0
	stateStone      = 1
	stateGrassBlock = 8
	stateDirt       = 10
	stateBedrock    = 85
)

// biome index for minecraft:plains inside the vanilla sorted biome list.
const biomePlains = 40

// blockAt returns the state id of the superflat floor at a y level, or
// air above the surface.
func blockAt(y int) int32 {
	switch y {
	case -64:
		return stateBedrock
	case -63, -62:
		return stateDirt
	case -61:
		return stateGrassBlock
	default:
		return stateAir
	}
}

// surfaceHeight is the first air y above the superflat surface.
const surfaceHeight = -60

// chunkData serialises the Level Chunk with Light packet payload for one
// chunk column, caching the compressed result.
func (w *world) chunkData(cx, cz int32) ([]byte, error) {
	key := [2]int32{cx, cz}
	w.mu.Lock()
	cached, ok := w.caches[key]
	w.mu.Unlock()
	if ok {
		return cached, nil
	}

	// The packet id is written by the caller; the payload starts at
	// the chunk coordinates.
	body := protocol.NewWriter()
	body.Int32(cx)
	body.Int32(cz)
	writeChunkData(body, cx, cz)
	writeLightData(body)

	// The play chunk packet is wrapped in zlib exactly like the old
	// "compressed chunk" packets? No: the payload travels inside the normal
	// connection compression. The vanilla packet is plain; keep it plain.
	out := body.Bytes()

	w.mu.Lock()
	w.caches[key] = out
	w.mu.Unlock()
	return out, nil
}

// writeChunkData writes the heightmaps + section buffer + block entities.
func writeChunkData(body *protocol.Writer, cx, cz int32) {
	// Heightmaps: the four client-facing types. All values equal the
	// surface: the first non-air y is -61, stored as y - minY + 1 = 4.
	// Heightmap values are packed with 9-bit entries.
	const heightmapBits = 9
	const hValue = surfaceHeight - minY // 4: first air y minus world bottom
	var longs [37]uint64                // 256 entries * 9 bits: 7 per long -> 37 longs
	packUniform(longs[:], 256, heightmapBits, hValue)

	body.VarInt(3)                         // three client-facing heightmap types (sendToClient: Usage.CLIENT only)
	for _, typ := range []int32{1, 4, 5} { // WORLD_SURFACE, MOTION_BLOCKING, MOTION_BLOCKING_NO_LEAVES
		body.VarInt(typ)
		body.VarInt(int32(len(longs)))
		for _, l := range longs {
			body.Int64(int64(l))
		}
	}

	// Section buffer: 24 sections, each 2-byte counters + block container
	// + biome container. Sections are uniform except the floor section.
	var sec protocol.Writer
	for i := 0; i < sectionsCount; i++ {
		base := minY + i*16
		if base == minY {
			// floor section: bedrock/dirt/dirt/grass -> 4 non-air layers
			sec.Int16(4 * 16 * 16)
			sec.Int16(0) // fluid count
			// Palette includes air: the floor section also holds the
			// 12 air layers above the surface block.
			writeLinearContainer(&sec, []int32{stateBedrock, stateDirt, stateGrassBlock, stateAir}, base)
		} else {
			sec.Int16(0)
			sec.Int16(0)
			writeSingleValueContainer(&sec, stateAir)
		}
		writeSingleValueBiome(&sec)
	}
	_ = cx
	_ = cz

	payload := sec.Bytes()
	body.VarInt(int32(len(payload)))
	body.FixedBytes(payload)

	// Block entities: none in superflat.
	body.VarInt(0)
}

// writeLinearContainer encodes a 4-bit linear-palette container holding the
// three-layer superflat floor. Palette: bedrock, dirt, grass_block.
// Entries are stored per block index (y<<8 | z<<4 | x) with 4 bits each;
// the floor uses the same state for a whole layer.
func writeLinearContainer(w *protocol.Writer, states []int32, baseY int) {
	w.Byte(4) // bits per entry -> linear palette
	// palette: VarInt count + VarInt global ids
	w.VarInt(int32(len(states)))
	for _, s := range states {
		w.VarInt(s)
	}
	// storage: 4096 entries * 4 bits = 256 longs, 16 entries per long.
	// With a linear palette the packed values are palette INDICES, not
	// global block state ids: layer 0 -> 0 (bedrock), layers 1-2 -> 1
	// (dirt), layer 3 -> 2 (grass_block), layers 4-15 -> 3 (air).
	var longs [256]uint64
	for i := 0; i < sectionBlocks; i++ {
		y := i >> 8
		idx := uint64(3) // air above the surface
		switch y {
		case 0:
			idx = 0
		case 1, 2:
			idx = 1
		case 3:
			idx = 2
		}
		cell := i / 16
		bit := (i % 16) * 4
		longs[cell] |= idx << bit
	}
	for _, l := range longs {
		w.Int64(int64(l))
	}
	_ = baseY
}

// writeSingleValueContainer encodes a bits=0 container with one state.
func writeSingleValueContainer(w *protocol.Writer, state int32) {
	w.Byte(0)
	w.VarInt(state)
	// zero-length long storage: nothing follows
}

// writeSingleValueBiome encodes the biome container: plains everywhere.
func writeSingleValueBiome(w *protocol.Writer) {
	w.Byte(0)
	w.VarInt(biomePlains)
}

// packUniform fills a bit-packed heightmap where every entry is value.
// The vanilla SimpleBitStorage format packs entries contiguously at bit
// offset i*bits, straddling long boundaries when 64 is not a multiple of
// bits; the long count is ceil(entries / (64/bits)).
func packUniform(dst []uint64, entries int, bits int, value int32) {
	mask := uint64(1)<<uint(bits) - 1
	v := uint64(value) & mask
	for i := 0; i < entries; i++ {
		bitIndex := i * bits
		cell, start := bitIndex/64, uint(bitIndex%64)
		dst[cell] |= v << start
		if start+uint(bits) > 64 {
			dst[cell+1] |= v >> (64 - start)
		}
	}
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

// compressZlib compresses payload for the cached chunk transport if the
// connection layer does not handle it. Kept for M3 compression tuning.
func compressZlib(payload []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(payload)
	_ = zw.Close()
	return buf.Bytes()
}

// sortStrings is a tiny helper used by the registry synchroniser.
func sortStrings(s []string) []string {
	out := append([]string(nil), s...)
	sort.Strings(out)
	return out
}
