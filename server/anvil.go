package server

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// Anvil region persistence (M4). Files follow the vanilla .mca layout so
// the world directory stays readable by vanilla clients and tools:
//
//      <header>  1024 x 4-byte location entries (3-byte sector offset in
//                4 KiB units, 1-byte sector count) followed by 1024 x 4-byte
//                unix timestamps
//      <sectors> 4-byte payload length, 1-byte compression type (2 = zlib),
//                zlib-compressed chunk NBT
//
// Only chunks the generator cannot reproduce (touched by players) are
// written, keeping the on-disk footprint to what must persist.

const (
	regionChunks   = 32
	sectorSize     = 4096
	regionTableLen = regionChunks * regionChunks * 4 // location or timestamp table
)

// ---- bit packing ----

// packSpanning packs values into longs the way Anvil does: entries flow
// across long boundaries with no per-long padding (vanilla SimpleBitStorage).
func packSpanning(values []uint32, bits int) []int64 {
	if bits <= 0 {
		return nil
	}
	longs := (len(values)*bits + 63) / 64
	out := make([]int64, longs)
	bit := 0
	for _, v := range values {
		l, off := bit/64, uint(bit%64)
		out[l] |= int64(v) << off
		if off+uint(bits) > 64 {
			out[l+1] |= int64(v) >> (64 - off)
		}
		bit += bits
	}
	return out
}

// unpackSpanning is the inverse of packSpanning.
func unpackSpanning(longs []int64, bits, count int) []uint32 {
	out := make([]uint32, count)
	bit := 0
	mask := uint32(1)<<uint(bits) - 1
	for i := 0; i < count; i++ {
		l, off := bit/64, uint(bit%64)
		v := uint32(uint64(longs[l])>>off) & mask
		if off+uint(bits) > 64 && l+1 < len(longs) {
			v |= uint32(uint64(longs[l+1]) & (1<<(off+uint(bits)-64) - 1) << (64 - off))
		}
		out[i] = v
		bit += bits
	}
	return out
}

// paletteBits picks the Anvil storage width for a palette size: 4 bits
// minimum, growing to 16 for the global palette fallback.
func paletteBits(n int) int {
	bits := 4
	for 1<<bits < n && bits < 16 {
		bits++
	}
	return bits
}

// ---- state id <-> palette entry ----

// stateIndexKey canonicalises a palette entry: name plus properties in
// sorted k=v order.
func stateIndexKey(name string, props map[string]string) string {
	if len(props) == 0 {
		return name
	}
	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var sb strings.Builder
	sb.WriteString(name)
	for _, k := range keys {
		sb.WriteByte('|')
		sb.WriteString(k)
		sb.WriteByte('=')
		sb.WriteString(props[k])
	}
	return sb.String()
}

var stateIndexOnce = func() map[string]int32 {
	m := make(map[string]int32, blockStateCount)
	for id := 0; id < blockStateCount; id++ {
		m[stateIndexKey(blockNameOf(id), blockPropsOf(id))] = int32(id)
	}
	return m
}()

// stateIDOf resolves a palette entry back to the global state id; unknown
// entries fall back to air (0) with the name preserved nowhere — the world
// model cannot represent them.
func stateIDOf(name string, props map[string]string) int32 {
	if id, ok := stateIndexOnce[stateIndexKey(name, props)]; ok {
		return id
	}
	return stateAir
}

// nbtPaletteEntry builds one block_states palette element.
func nbtPaletteEntry(id int32) java.NbtAny {
	entry := java.NewNbtComp().Set("Name", java.NbtString(blockNameOf(int(id))))
	if props := blockPropsOf(int(id)); len(props) > 0 {
		p := java.NewNbtComp()
		for k, v := range props {
			p.Set(k, java.NbtString(v))
		}
		entry.Set("Properties", java.NbtAny{Type: java.TagCompound, Comp: p})
	}
	return java.NbtAny{Type: java.TagCompound, Comp: entry}
}

// ---- chunk -> NBT ----

// nbtHeightmap packs one heightmap type (9 bits per cell, 256 cells).
func nbtHeightmap(c *chunk) []int64 {
	vals := make([]uint32, sectionBlocks)
	for z := 0; z < 16; z++ {
		for x := 0; x < 16; x++ {
			vals[z*16+x] = uint32(c.heightAt(x, z))
		}
	}
	return packSpanning(vals, 9)
}

// nbtChunk serialises a chunk column to its Anvil compound form.
func nbtChunk(c *chunk) *java.NbtComp {
	root := java.NewNbtComp().
		Set("DataVersion", java.NbtInt(v776.WorldVersion)).
		Set("xPos", java.NbtInt(int64(c.cx))).
		Set("zPos", java.NbtInt(int64(c.cz))).
		Set("yPos", java.NbtInt(minY/16)).
		Set("Status", java.NbtString("minecraft:full")).
		Set("LastUpdate", java.NbtLong(0)).
		Set("InhabitedTime", java.NbtLong(0))

	sections := make([]java.NbtAny, 0, sectionsCount)
	for si, s := range c.sections {
		sc := java.NewNbtComp().Set("Y", java.NbtByte(int64(si-minY/16)))
		if nonAir := s.nonAirCount(); nonAir > 0 {
			states := java.NewNbtComp()
			if s.uniform {
				pal := []java.NbtAny{nbtPaletteEntry(s.single)}
				states.Set("palette", java.NbtListOf(java.TagCompound, pal))
			} else {
				pal := make([]java.NbtAny, len(s.palette))
				for i, id := range s.palette {
					pal[i] = nbtPaletteEntry(id)
				}
				states.Set("palette", java.NbtListOf(java.TagCompound, pal))
				// Disk data packs palette INDICES; section.get translates
				// to global ids, so invert the palette here first.
				index := make(map[int32]uint32, len(s.palette))
				for i, id := range s.palette {
					index[id] = uint32(i)
				}
				vals := make([]uint32, sectionBlocks)
				for i := range vals {
					vals[i] = index[s.get(i)]
				}
				states.Set("data", java.NbtLongs(packSpanning(vals, paletteBits(len(s.palette)))))
			}
			sc.Set("block_states", java.NbtAny{Type: java.TagCompound, Comp: states})
			biomes := java.NewNbtComp().
				Set("palette", java.NbtListOf(java.TagString, []java.NbtAny{java.NbtString("minecraft:plains")}))
			sc.Set("biomes", java.NbtAny{Type: java.TagCompound, Comp: biomes})
		}
		sections = append(sections, java.NbtAny{Type: java.TagCompound, Comp: sc})
	}
	root.Set("sections", java.NbtListOf(java.TagCompound, sections))

	root.Set("Heightmaps", java.NbtAny{Type: java.TagCompound, Comp: java.NewNbtComp().
		Set("MOTION_BLOCKING", java.NbtLongs(nbtHeightmap(c))).
		Set("WORLD_SURFACE", java.NbtLongs(nbtHeightmap(c)))})

	// No light is stored: isLightOn=0 tells vanilla engines to relight on
	// load, which matches our uniformly-lit sky model.
	root.Set("isLightOn", java.NbtByte(0))
	root.Set("BlockEntities", java.NbtEmptyList(java.TagCompound))
	root.Set("structures", java.NbtAny{Type: java.TagCompound, Comp: java.NewNbtComp().
		Set("References", java.NbtAny{Type: java.TagCompound, Comp: java.NewNbtComp()}).
		Set("starts", java.NbtAny{Type: java.TagCompound, Comp: java.NewNbtComp()})})
	return root
}

// ---- NBT -> chunk ----

// chunkFromNBT rebuilds a chunk column from its Anvil compound. Sections
// missing block_states load as air; unknown palette entries degrade to air.
func chunkFromNBT(cx, cz int32, root *java.NbtComp) *chunk {
	c := &chunk{cx: cx, cz: cz}
	for si := range c.sections {
		c.sections[si] = newSection(stateAir)
	}
	for _, sv := range root.Get("sections").List {
		sc := sv.Comp
		// Y is the absolute section index (minY/16 = -4 for the overworld);
		// convert back to our 0-based array position.
		si := int(sc.Get("Y").Num) + minY/16
		if si < 0 || si >= sectionsCount {
			continue
		}
		bs := sc.Get("block_states").Comp
		if bs == nil || !bs.Has("palette") {
			continue
		}
		pal := bs.Get("palette").List
		ids := make([]int32, len(pal))
		for i, pv := range pal {
			name := pv.Comp.Get("Name").Str
			var props map[string]string
			if pp := pv.Comp.Get("Properties").Comp; pp != nil {
				props = make(map[string]string, len(pp.Fields))
				for k, v := range pp.Fields {
					props[k] = v.Str
				}
			}
			ids[i] = stateIDOf(name, props)
		}

		switch {
		case len(ids) == 0:
			continue
		case len(ids) == 1:
			c.sections[si] = &section{uniform: true, single: ids[0]}
		default:
			vals := unpackSpanning(bs.Get("data").Longs, paletteBits(len(ids)), sectionBlocks)
			c.sections[si] = newSpanSection(ids, vals)
		}
	}
	return c
}

// newSpanSection rebuilds a linear section from disk values: the spanning
// payload is repacked into the wire layout so the existing get() decodes
// palette indices unchanged.
func newSpanSection(palette []int32, vals []uint32) *section {
	bits := paletteBits(len(palette))
	s := &section{bits: uint8(bits), palette: palette}
	s.data = repackNoStraddle(vals, bits)
	return s
}

// repackNoStraddle converts spanning-packed values into the wire layout:
// floor(64/bits) entries per long, tail bits zero.
func repackNoStraddle(vals []uint32, bits int) []uint64 {
	per := 64 / bits
	longs := (len(vals) + per - 1) / per
	out := make([]uint64, longs)
	for i, v := range vals {
		l, off := i/per, uint((i%per)*bits)
		out[l] |= uint64(v) << off
	}
	return out
}

// ---- region file I/O ----

// regionPath maps chunk coords to <dir>/region/r.<rx>.<rz>.mca.
func regionPath(dir string, rx, rz int32) string {
	return filepath.Join(dir, "region", fmt.Sprintf("r.%d.%d.mca", rx, rz))
}

// zlibEncode compresses the chunk NBT payload for storage.
func zlibEncode(b []byte) []byte {
	var buf bytes.Buffer
	zw := zlib.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

// zlibDecode reverses zlibEncode.
func zlibDecode(b []byte) ([]byte, error) {
	zr, err := zlib.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	return io.ReadAll(zr)
}

// writeRegion stores the given compressed payloads (keyed by local chunk
// index 0..1023) as a complete .mca file, replacing any previous content.
// The write is atomic: temp file + rename.
func writeRegion(path string, payloads map[int][]byte, now int64) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	// Sector allocation in local-index order keeps the layout deterministic.
	idxs := make([]int, 0, len(payloads))
	for i := range payloads {
		idxs = append(idxs, i)
	}
	sort.Ints(idxs)

	loc := make([]byte, regionTableLen)
	stamp := make([]byte, regionTableLen)
	binary.BigEndian.PutUint32(stamp[0:4], uint32(now))

	body := bytes.NewBuffer(nil)
	nextSector := uint32(2) // header occupies sectors 0 and 1
	for _, i := range idxs {
		p := payloads[i]
		sectors := uint32((len(p) + sectorSize - 1) / sectorSize)
		if sectors == 0 {
			sectors = 1
		}
		if nextSector+sectors > uint32(1<<24) {
			return fmt.Errorf("anvil: region %s overflow", path)
		}
		loc[i*4] = byte(nextSector >> 16)
		loc[i*4+1] = byte(nextSector >> 8)
		loc[i*4+2] = byte(nextSector)
		loc[i*4+3] = byte(sectors)
		start := nextSector - 2
		pad := int64(start*sectorSize) - int64(body.Len())
		if pad > 0 {
			_, _ = body.Write(make([]byte, pad))
		}
		var hdr [4]byte
		binary.BigEndian.PutUint32(hdr[:], uint32(len(p)+1))
		_, _ = body.Write(hdr[:])
		_ = body.WriteByte(2) // zlib
		_, _ = body.Write(p)
		nextSector += sectors
	}

	// Sector-align the tail.
	if rem := body.Len() % sectorSize; rem != 0 {
		_, _ = body.Write(make([]byte, sectorSize-rem))
	}

	out := bytes.NewBuffer(nil)
	out.Write(loc)
	out.Write(stamp)
	out.Write(body.Bytes())

	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, out.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// readRegion parses a .mca file and returns each chunk's compressed
// payload keyed by local index. Absent files yield an empty map.
func readRegion(path string) (map[int][]byte, error) {
	out := make(map[int][]byte)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return out, nil
		}
		return nil, err
	}
	if len(raw) < regionTableLen*2 {
		return out, nil // truncated header: treat as empty region
	}
	for i := 0; i < regionChunks*regionChunks; i++ {
		off := uint32(raw[i*4])<<16 | uint32(raw[i*4+1])<<8 | uint32(raw[i*4+2])
		cnt := uint32(raw[i*4+3])
		if off == 0 || cnt == 0 {
			continue
		}
		start := int(off) * sectorSize
		if start+4 > len(raw) {
			continue
		}
		size := int(binary.BigEndian.Uint32(raw[start : start+4]))
		if size < 1 || start+4+size > len(raw) {
			continue
		}
		if raw[start+4] != 2 { // only zlib payloads are expected
			continue
		}
		data, err := zlibDecode(raw[start+5 : start+4+size])
		if err != nil {
			continue // damaged chunk: skip, never fail the whole load
		}
		out[i] = data
	}
	return out, nil
}

// localChunkIndex is the .mca slot of a chunk inside its region.
func localChunkIndex(cx, cz int32) int {
	return int(uint32(cx)&31) + int(uint32(cz)&31)*32
}

// regionOf maps chunk coords to their region file coords.
func regionOf(cx, cz int32) (int32, int32) {
	return cx >> 5, cz >> 5
}

// parseRegionName reads "r.<rx>.<rz>.mca" components.
func parseRegionName(name string) (rx, rz int32, ok bool) {
	if !strings.HasPrefix(name, "r.") || !strings.HasSuffix(name, ".mca") {
		return 0, 0, false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, "r."), ".mca"), ".")
	if len(parts) != 2 {
		return 0, 0, false
	}
	x, err1 := strconv.ParseInt(parts[0], 10, 32)
	z, err2 := strconv.ParseInt(parts[1], 10, 32)
	if err1 != nil || err2 != nil {
		return 0, 0, false
	}
	return int32(x), int32(z), true
}
