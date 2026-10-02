package server

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// TestNbtRoundTrip writes every tag type through the file encoder and
// verifies the decoded tree field by field.
func TestNbtRoundTrip(t *testing.T) {
	inner := java.NewNbtComp().
		Set("count", java.NbtInt(42)).
		Set("flag", java.NbtByte(1)).
		Set("ratio", java.NbtFloat(1.5)).
		Set("note", java.NbtString("héllo §a"))
	root := java.NewNbtComp().
		Set("DataVersion", java.NbtInt(v776.WorldVersion)).
		Set("long_val", java.NbtLong(1<<62)).
		Set("short_val", java.NbtShort(-2)).
		Set("double_val", java.NbtDouble(0.25)).
		Set("bytes", java.NbtBytes([]byte{1, 2, 3})).
		Set("ints", java.NbtInts([]int32{-1, 7})).
		Set("longs", java.NbtLongs([]int64{9, -9})).
		Set("empty", java.NbtEmptyList(java.TagCompound)).
		Set("inner", java.NbtAny{Type: java.TagCompound, Comp: inner})

	var buf protocol.Writer
	java.WriteNbtFile(&buf, root)

	// File form starts with TAG_Compound + empty root name.
	raw := buf.Bytes()
	if len(raw) < 3 || raw[0] != 0x0A || raw[1] != 0x00 || raw[2] != 0x00 {
		t.Fatalf("file root magic: %v", raw[:min(3, len(raw))])
	}

	got, err := java.ReadNbtFile(protocol.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if v := got.Get("DataVersion").Num; v != v776.WorldVersion {
		t.Fatalf("DataVersion %d", v)
	}
	if v := got.Get("long_val").Num; v != 1<<62 {
		t.Fatalf("long %d", v)
	}
	if v := got.Get("short_val").Num; v != -2 {
		t.Fatalf("short %d", v)
	}
	if v := got.Get("double_val").Float; v != 0.25 {
		t.Fatalf("double %v", v)
	}
	if !bytes.Equal(got.Get("bytes").Bytes, []byte{1, 2, 3}) {
		t.Fatal("byte array mismatch")
	}
	if got.Get("inner").Comp.Get("note").Str != "héllo §a" {
		t.Fatalf("nested string %q", got.Get("inner").Comp.Get("note").Str)
	}
	if n := len(got.Get("empty").List); n != 0 {
		t.Fatalf("empty list length %d", n)
	}
}

// TestSpanningPacking checks the Anvil bit layout: round-trips at every
// width, the 9-bit heightmap size (36 longs) and a hand-checked straddle.
func TestSpanningPacking(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	for _, bits := range []int{4, 5, 8, 9, 15} {
		vals := make([]uint32, 1000)
		for i := range vals {
			vals[i] = rng.Uint32() & ((1 << uint(bits)) - 1)
		}
		longs := packSpanning(vals, bits)
		if got := unpackSpanning(longs, bits, len(vals)); !equalU32(got, vals) {
			t.Fatalf("bits %d: spanning round-trip mismatch", bits)
		}
	}
	// 9 bits x 256 cells = 2304 bits = exactly 36 longs.
	if n := len(packSpanning(make([]uint32, 256), 9)); n != 36 {
		t.Fatalf("heightmap longs %d, want 36", n)
	}
	// Hand check: bits=5, values 1,2 → long0 = 1 | 2<<5 = 65.
	if l := packSpanning([]uint32{1, 2}, 5); l[0] != 65 {
		t.Fatalf("straddle check %d, want 65", l[0])
	}
}

func equalU32(a, b []uint32) bool {
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestChunkNbtRoundTrip serialises a mutated chunk to NBT, rebuilds it and
// verifies block contents and heightmaps survive the trip.
func TestChunkNbtRoundTrip(t *testing.T) {
	c := newSuperflatChunk(3, -5)
	if !c.setBlock(4, -59, 7, stateStone) {
		t.Fatal("setBlock no-op")
	}
	if !c.setBlock(0, -62, 0, 0) { // mine a hole into the dirt layer
		t.Fatal("setBlock no-op")
	}

	var buf protocol.Writer
	java.WriteNbtFile(&buf, nbtChunk(c, nil))
	root, err := java.ReadNbtFile(protocol.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}

	got, ents := chunkFromNBT(3, -5, root)
	if len(ents) != 0 {
		t.Fatalf("unexpected block entities: %d", len(ents))
	}
	if got.getBlock(4, -59, 7) != stateStone {
		t.Fatalf("placed block lost: %d", got.getBlock(4, -59, 7))
	}
	if got.getBlock(0, -62, 0) != stateAir {
		t.Fatalf("mined block restored: %d", got.getBlock(0, -62, 0))
	}
	if got.getBlock(0, -61, 0) != stateGrassBlock {
		t.Fatalf("surface layer wrong: %d", got.getBlock(0, -61, 0))
	}
	if got.getBlock(15, 100, 15) != stateAir {
		t.Fatal("sky not air")
	}
	// The mined column keeps its grass at -61: height = -61-minY+1 = 4.
	if h := got.heightAt(0, 0); h != -61-minY+1 {
		t.Fatalf("heightAt(0,0)=%d, want %d", h, -61-minY+1)
	}
	// Wire payload of the rebuilt chunk must decode with the same palette
	// content: parse it through the section reader.
	for si, s := range got.sections {
		orig := c.sections[si]
		for _, probe := range [][3]int{{4, -59, 7}, {0, -62, 0}, {0, -61, 0}} {
			if (probe[1]-minY)/16 != si {
				continue
			}
			if s.get(sectionIndex(probe[0], probe[1], probe[2])) != orig.get(sectionIndex(probe[0], probe[1], probe[2])) {
				t.Fatalf("section %d block mismatch at %v", si, probe)
			}
		}
	}
}

// TestWorldPersistRoundTrip is the M4 acceptance check: mutate a world,
// flush, then rebuild a fresh world from the same directory and confirm
// the edits and the generator default both come back correctly.
func TestWorldPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w1 := newWorld(0)
	w1.enableSaving(dir)
	if w1.setBlock(9, -59, 9, stateStone) != true {
		t.Fatal("place failed")
	}
	if w1.setBlock(-20, -62, 30, stateAir) != true { // mine cross-chunk
		t.Fatal("mine failed")
	}
	if err := w1.saveAll(nil); err != nil {
		t.Fatal(err)
	}

	// Region file layout sanity: chunk (9,·,9) lives in r.0.0.mca slot 0;
	// the mined chunk (-20,·,30) lands in r.-1.0.mca.
	raw, err := os.ReadFile(filepath.Join(dir, "region", "r.0.0.mca"))
	if err != nil {
		t.Fatalf("region file missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "region", "r.-1.0.mca")); err != nil {
		t.Fatalf("cross-chunk region missing: %v", err)
	}
	if len(raw) < regionTableLen*2 || len(raw)%sectorSize != 0 {
		t.Fatalf("region file size %d not sector aligned", len(raw))
	}
	off := uint32(raw[0])<<16 | uint32(raw[1])<<8 | uint32(raw[2])
	if off != 2 {
		t.Fatalf("first chunk sector %d, want 2 (right after header)", off)
	}
	size := int(binary.BigEndian.Uint32(raw[8192:8196]))
	if size < 1 || raw[8196] != 2 {
		t.Fatalf("payload header size=%d comp=%d", size, raw[8196])
	}

	// Fresh world from disk.
	w2 := newWorld(0)
	w2.enableSaving(dir)
	if got := w2.getBlock(9, -59, 9); got != stateStone {
		t.Fatalf("reload stone: %d", got)
	}
	if got := w2.getBlock(-20, -62, 30); got != stateAir {
		t.Fatalf("reload mined air: %d", got)
	}
	if got := w2.getBlock(0, -61, 0); got != stateGrassBlock {
		t.Fatalf("untouched surface: %d", got)
	}

	// Mutating a reloaded chunk and saving again keeps both edits.
	if w2.setBlock(9, -58, 9, stateDirt) != true {
		t.Fatal("post-reload place failed")
	}
	if err := w2.saveAll(nil); err != nil {
		t.Fatal(err)
	}
	w3 := newWorld(0)
	w3.enableSaving(dir)
	if got := w3.getBlock(9, -59, 9); got != stateStone {
		t.Fatalf("second reload stone: %d", got)
	}
	if got := w3.getBlock(9, -58, 9); got != stateDirt {
		t.Fatalf("second reload dirt: %d", got)
	}
}

// TestSaveDirLazyOnlyWritesWhenDirty proves the "only persist what must
// persist" rule: an untouched world writes no files at all.
func TestSaveDirLazy(t *testing.T) {
	dir := t.TempDir()
	w := newWorld(0)
	w.enableSaving(dir)
	if err := w.saveDirty(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "region")); !os.IsNotExist(err) {
		t.Fatal("region dir created without any dirty chunk")
	}
}

// TestStartTestServerNoPersistence guards the shared test fixture: the
// default test server must not write a world directory next to the repo.
func TestStartTestServerNoPersistence(t *testing.T) {
	s := startTestServer(t)
	if s.opts.LevelName != "" {
		t.Fatalf("test server level name %q", s.opts.LevelName)
	}
}
