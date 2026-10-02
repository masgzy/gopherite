package server

import (
	"log"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// Entity tracking (M5): the same seen-set model as chunk streaming. Each
// player carries the entity ids it has been told about; the per-tick sync
// spawns/despawns by distance, broadcasts move deltas and metadata
// updates, and retires dead entities everywhere.

// entityTrackRange2 is the squared spawn distance; entities beyond it
// despawn with a small hysteresis margin so jitter can't flap.
const (
	entityTrackRange2  = 128 * 128
	entityDespawnSlop2 = 8 * 8
)

// tickEntities advances every entity and then reconciles trackers. Runs
// on the ticker goroutine only (guarded against shutdown by closing).
func (s *Server) tickEntities() {
	s.mu.Lock()
	ids := make([]entity, 0, len(s.entities))
	for _, e := range s.entities {
		ids = append(ids, e)
	}
	s.mu.Unlock()

	// M9: snapshot targets/daylight into hostile mobs before they tick
	// (the brains must not take Server.mu themselves).
	s.prepareMobTicks()

	for _, e := range ids {
		e.tick(s)
		if m, ok := e.(*mobEntity); ok {
			m.mu.Lock()
			dmg := m.pendingFallDmg
			m.pendingFallDmg = 0
			melee, shoot, explode, fireDmg, primed := m.pendingMelee, m.pendingShoot, m.pendingExplode, m.pendingFireDmg, m.pendingPrimedSound
			m.pendingMelee, m.pendingShoot, m.pendingExplode, m.pendingFireDmg, m.pendingPrimedSound = false, false, false, false, false
			m.mu.Unlock()
			if dmg > 0 {
				s.mobFallDamage(m, dmg)
			}
			if fireDmg || melee || shoot || explode || primed {
				// All hostile intents mutate server state: run them
				// under s.mu (their contracts; no mob lock held here).
				s.mu.Lock()
				if fireDmg {
					s.damageMobLocked(m, 1, v776.DamageTypeOnFire, -1, -1)
				}
				if melee {
					s.hostileMeleeLocked(m)
				}
				if shoot {
					s.skeletonShootLocked(m)
				}
				if primed {
					mx, my, mz := m.x, m.y, m.z
					s.broadcastSoundLocked("minecraft:entity.creeper.primed", v776.SoundSourceHostile,
						float32(mx), float32(my+0.5), float32(mz), 1.0, 0.5)
				}
				if explode {
					s.explodeCreeperLocked(m)
				}
				s.mu.Unlock()
			}
		}
	}

	// M9: arrow hits resolve after the tick loop on the ticker's lock
	// order (s.mu -> arrow.mu inside consumeArrowHits).
	s.consumeArrowHits()

	s.mu.Lock()
	var dead []int32
	for id, e := range s.entities {
		if !e.alive() {
			dead = append(dead, id)
			delete(s.entities, id)
		}
	}
	s.mu.Unlock()
	if len(dead) > 0 {
		s.broadcastRemoveEntities(dead)
	}

	s.syncEntities()
	s.syncPlayerVisibility()
}

// syncEntities reconciles every player's entity set: spawn, despawn,
// metadata refresh and move deltas. Payloads are encoded per entity and
// fanned out to all trackers.
func (s *Server) syncEntities() {
	s.mu.Lock()
	type work struct {
		e                   entity
		prevX, prevY, prevZ float64
		hadPrev             bool
		meta                bool
		moved               bool
		prevYaw, prevHead   float32
		hadRot              bool
	}
	works := make([]work, 0, len(s.entities))
	for _, e := range s.entities {
		if !e.alive() {
			continue
		}
		px, py, pz, ok := e.netPos()
		x, y, z := e.xPos()
		moved := ok && (px != x || py != y || pz != z)
		w := work{e: e, prevX: px, prevY: py, prevZ: pz, hadPrev: ok, meta: e.metadataDirty(), moved: moved}
		// Rotation baseline for mobs: capture before freezing the new one.
		if ro, isRot := e.(rotator); isRot {
			w.prevYaw, w.prevHead, w.hadRot = ro.netRot()
			cy, ch := ro.curRot()
			ro.setNetRot(cy, ch)
		}
		works = append(works, w)
		e.clearMetadataDirty()
		// Freeze the broadcast baseline AFTER capturing the previous one,
		// so the move delta below is computed against the right origin.
		e.setNetPos(x, y, z)
	}
	players := make([]*player, 0, len(s.players))
	for _, p := range s.players {
		players = append(players, p)
	}
	s.mu.Unlock()

	for _, w := range works {
		e := w.e
		ex, ey, ez := e.xPos()

		// Move deltas to existing trackers. Mobs carry their body yaw along
		// (pos_rot) plus a head-yaw update when it changed; items keep the
		// cheaper pos-only delta.
		if w.hadPrev && w.moved {
			if ro, isRot := e.(rotator); isRot {
				yaw, head := ro.curRot()
				body := protocol.NewWriter()
				body.VarInt(v776.PacketPlayMoveEntityPosRot)
				java.WriteMoveEntityPosRot(body, e.entityID(),
					shortDelta(ex-w.prevX), shortDelta(ey-w.prevY), shortDelta(ez-w.prevZ),
					angleByte(yaw), 0, e.onGroundFlag())
				for _, p := range players {
					if p.seenEnt[e.entityID()] {
						_ = p.conn.sendPacket(body.Bytes())
					}
				}
				if angleByte(head) != angleByte(w.prevHead) {
					headBody := protocol.NewWriter()
					headBody.VarInt(v776.PacketPlayRotateHead)
					java.WriteRotateHead(headBody, e.entityID(), angleByte(head))
					for _, p := range players {
						if p.seenEnt[e.entityID()] {
							_ = p.conn.sendPacket(headBody.Bytes())
						}
					}
				}
			} else {
				body := protocol.NewWriter()
				body.VarInt(v776.PacketPlayMoveEntityPos)
				java.WriteMoveEntityPos(body, e.entityID(),
					shortDelta(ex-w.prevX), shortDelta(ey-w.prevY), shortDelta(ez-w.prevZ), e.onGroundFlag())
				for _, p := range players {
					if p.seenEnt[e.entityID()] {
						_ = p.conn.sendPacket(body.Bytes())
					}
				}
			}
		} else if w.hadPrev && w.hadRot {
			// Stationary mobs still turn their heads towards the walk target.
			if ro, isRot := e.(rotator); isRot {
				_, head := ro.curRot()
				if angleByte(head) != angleByte(w.prevHead) {
					headBody := protocol.NewWriter()
					headBody.VarInt(v776.PacketPlayRotateHead)
					java.WriteRotateHead(headBody, e.entityID(), angleByte(head))
					for _, p := range players {
						if p.seenEnt[e.entityID()] {
							_ = p.conn.sendPacket(headBody.Bytes())
						}
					}
				}
			}
		}

		// Metadata refresh (stack size changed after a partial pickup).
		if w.meta {
			body := protocol.NewWriter()
			s.encodeMetadata(body, e)
			if body.Len() > 0 { // entities without metadata encode nothing
				for _, p := range players {
					if p.seenEnt[e.entityID()] {
						_ = p.conn.sendPacket(body.Bytes())
					}
				}
			}
		}

		// Spawn/despawn per player.
		spawnBody := protocol.NewWriter()
		s.encodeSpawn(spawnBody, e)
		metaBody := protocol.NewWriter()
		s.encodeMetadata(metaBody, e)
		// Mobs carry no metadata: encodeMetadata writes nothing for them,
		// and an EMPTY frame would abort the client's decoder outright.
		hasMeta := metaBody.Len() > 0
		for _, p := range players {
			dx := p.x - ex
			dy := p.y - ey
			dz := p.z - ez
			d2 := dx*dx + dy*dy + dz*dz
			if d2 <= entityTrackRange2 && !p.seenEnt[e.entityID()] {
				_ = p.conn.sendPacket(spawnBody.Bytes())
				if hasMeta {
					_ = p.conn.sendPacket(metaBody.Bytes())
				}
				p.seenEnt[e.entityID()] = true
			} else if d2 > entityTrackRange2+entityDespawnSlop2 && p.seenEnt[e.entityID()] {
				delete(p.seenEnt, e.entityID())
				body := protocol.NewWriter()
				body.VarInt(v776.PacketPlayRemoveEntities)
				java.WriteRemoveEntities(body, []int32{e.entityID()})
				_ = p.conn.sendPacket(body.Bytes())
			}
		}
	}
}

// shortDelta converts a world delta into move_entity_pos units (1/4096),
// clamped to the int16 range; larger jumps go through teleport_entity in
// a future pass (items never move that far in one tick).
func shortDelta(d float64) int16 {
	v := int32(d * 4096)
	if v > 32767 {
		v = 32767
	}
	if v < -32768 {
		v = -32768
	}
	return int16(v)
}

// broadcastRemoveEntities retires ids from every player that still sees
// them.
func (s *Server) broadcastRemoveEntities(ids []int32) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayRemoveEntities)
	java.WriteRemoveEntities(body, ids)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		var local []int32
		for _, id := range ids {
			if p.seenEnt[id] {
				delete(p.seenEnt, id)
				local = append(local, id)
			}
		}
		if len(local) == len(ids) {
			_ = p.conn.sendPacket(body.Bytes())
		} else if len(local) > 0 {
			sub := protocol.NewWriter()
			sub.VarInt(v776.PacketPlayRemoveEntities)
			java.WriteRemoveEntities(sub, local)
			_ = p.conn.sendPacket(sub.Bytes())
		}
	}
}

// sendTakeItem pushes the pickup animation to the collector.
func (c *conn) sendTakeItem(itemEntityID, collectorID, amount int32) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayTakeItemEntity)
	java.WriteTakeItemEntity(body, itemEntityID, collectorID, amount)
	if err := c.sendPacket(body.Bytes()); err != nil {
		log.Printf(ui.Warn("警告")+" 拾取动画发送失败: %v", err)
	}
}
