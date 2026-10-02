package server

import (
	"log"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
)

// World persistence glue (M4): dirty tracking, load-on-start, region
// saves on a vanilla-style autosave cadence plus a full flush on shutdown.

// enableSaving points the world at its save directory and replays any
// previously persisted chunks. Missing directories are tolerated: a fresh
// world simply generates from the superflat plane.
func (w *world) enableSaving(dir string) {
	w.saveDir = dir
	w.loadAll()
}

// drainPendingBEs hands the loaded block entities to the Server registry
// (called once after the startup replay).
func (w *world) drainPendingBEs() map[[3]int]*blockEntity {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := w.pendingBEs
	w.pendingBEs = make(map[[3]int]*blockEntity)
	return out
}

// loadAll reads every region file under <saveDir>/region and rebuilds the
// stored chunks. A damaged chunk skips without failing the rest: a world
// that half-loads still plays.
func (w *world) loadAll() {
	regionDir := filepath.Join(w.saveDir, "region")
	entries, err := os.ReadDir(regionDir)
	if err != nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	var loaded int
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		rx, rz, ok := parseRegionName(e.Name())
		if !ok {
			continue
		}
		payloads, err := readRegion(filepath.Join(regionDir, e.Name()))
		if err != nil {
			log.Printf(ui.Warn("警告")+" 读取区域文件 %s 失败: %v", e.Name(), err)
			continue
		}
		for idx, payload := range payloads {
			root, err := java.ReadNbtFile(protocol.NewReader(payload))
			if err != nil {
				log.Printf(ui.Warn("警告")+" 区块 NBT 解析失败 (%s #%d): %v", e.Name(), idx, err)
				continue
			}
			cx := rx*32 + int32(idx%32)
			cz := rz*32 + int32(idx/32)
			c, ents := chunkFromNBT(cx, cz, root)
			w.chunks[[2]int32{cx, cz}] = c
			w.persisted[[2]int32{cx, cz}] = true
			for _, b := range ents {
				w.pendingBEs[b.posKey()] = b
			}
			loaded++
		}
	}
	if loaded > 0 {
		log.Printf(ui.Success("OK ")+"从 %s 恢复 %s 个区块", ui.Path(w.saveDir), ui.Number(strconv.Itoa(loaded)))
	}
}

// markDirty records a chunk whose content changed since the last save.
// Safe to call without holding w.mu (setBlock path). Persisted chunks
// re-dirty on mutation: what is on disk must never drift from memory.
func (w *world) markDirty(cx, cz int32) {
	key := [2]int32{cx, cz}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.saveDir == "" {
		return
	}
	w.dirty[key] = true
}

// collectDirty drains the dirty set, returning the keys to persist.
func (w *world) collectDirty() [][2]int32 {
	keys := make([][2]int32, 0, len(w.dirty))
	for k := range w.dirty {
		keys = append(keys, k)
	}
	w.dirty = make(map[[2]int32]bool)
	return keys
}

// saveDirty persists every chunk modified since the last save. bes maps
// chunk keys to their container block entity snapshots (from the Server).
// Regions are rewritten whole: each .mca file regenerates from its
// in-memory chunk set so sector tables never go stale. On failure the
// dirty set is restored so the next cadence tick retries.
func (w *world) saveDirty(bes map[[2]int32][]*blockEntity) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	keys := w.collectDirty()
	if len(keys) == 0 {
		return nil
	}
	if err := w.saveKeysLocked(keys, bes); err != nil {
		for _, k := range keys {
			w.dirty[k] = true
		}
		return err
	}
	return nil
}

// saveAll flushes every chunk that is on disk or diverged from the
// generator (shutdown path).
func (w *world) saveAll(bes map[[2]int32][]*blockEntity) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	all := make(map[[2]int32]bool, len(w.persisted)+len(w.dirty))
	for k := range w.persisted {
		all[k] = true
	}
	for k := range w.dirty {
		all[k] = true
	}
	keys := make([][2]int32, 0, len(all))
	for k := range all {
		keys = append(keys, k)
	}
	return w.saveKeysLocked(keys, bes)
}

// saveKeysLocked serialises the given chunks into their region files.
// Callers hold w.mu. bes carries the per-chunk block entity snapshots.
// Dirty flags clear and the persisted set grows as chunks land on disk.
func (w *world) saveKeysLocked(keys [][2]int32, bes map[[2]int32][]*blockEntity) error {
	now := time.Now().Unix()
	// Group by region: one full .mca rewrite per touched region.
	regions := make(map[[2]int32]map[int][]byte)
	for _, key := range keys {
		c, ok := w.chunks[key]
		if !ok {
			continue
		}
		w.persisted[key] = true
		rx, rz := regionOf(key[0], key[1])
		m := regions[[2]int32{rx, rz}]
		if m == nil {
			m = make(map[int][]byte)
			regions[[2]int32{rx, rz}] = m
		}
		var buf protocol.Writer
		java.WriteNbtFile(&buf, nbtChunk(c, bes[key]))
		m[localChunkIndex(key[0], key[1])] = zlibEncode(buf.Bytes())
		delete(w.dirty, key)
	}
	for rg, payloads := range regions {
		if err := writeRegion(regionPath(w.saveDir, rg[0], rg[1]), payloads, now); err != nil {
			return err
		}
	}
	return nil
}

// blockEntitySnapshot deep-copies the container block entities grouped by
// chunk so the save goroutine never races live tick state. Caller holds
// Server.mu.
func (s *Server) blockEntitySnapshot() map[[2]int32][]*blockEntity {
	out := make(map[[2]int32][]*blockEntity)
	for _, b := range s.blockEnts {
		copyB := *b
		copyB.slots = append([]invSlot(nil), b.slots...)
		key := [2]int32{int32(b.x >> 4), int32(b.z >> 4)}
		out[key] = append(out[key], &copyB)
	}
	return out
}

// saveDirtyWorld snapshots block entities and flushes dirty chunks
// (autosave cadence).
func (s *Server) saveDirtyWorld() error {
	if s.opts.LevelName == "" {
		return nil
	}
	s.mu.Lock()
	bes := s.blockEntitySnapshot()
	s.mu.Unlock()
	return s.world.saveDirty(bes)
}

// saveAllWorld is the shutdown flush: every persisted or diverged chunk
// plus the final block entity state.
func (s *Server) saveAllWorld() error {
	s.mu.Lock()
	bes := s.blockEntitySnapshot()
	s.mu.Unlock()
	return s.world.saveAll(bes)
}

// autosaveLoop flushes dirty chunks every 5 seconds until shutdown,
// mirroring vanilla's continuous per-chunk save cadence.
func (s *Server) autosaveLoop() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if s.closing.Load() {
					return
				}
				if err := s.saveDirtyWorld(); err != nil {
					log.Printf(ui.Warn("警告")+"自动保存失败: %v", err)
				}
			case <-s.stop:
				return
			}
		}
	}()
}
