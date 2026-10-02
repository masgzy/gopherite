package server

import (
	"math"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M8.5 player visibility: every joined player streams every other player
// like a tracked entity — spawn by distance, pos_rot move deltas, head
// rotation and removal on disconnect. Players are NOT in s.entities (their
// movement is client-authoritative), so this pass mirrors the entity
// tracker with its own baseline per player.

// syncPlayerVisibility reconciles every observer's seen-player set and
// broadcasts move deltas. Runs on the ticker goroutine via tickEntities.
func (s *Server) syncPlayerVisibility() {
	s.mu.Lock()
	players := make([]*player, 0, len(s.players))
	for _, p := range s.players {
		players = append(players, p)
	}

	// Movement baselines are frozen once per tick so every observer sees
	// the same delta.
	type delta struct {
		p                *player
		moved            bool
		bigMove          bool
		dx, dy, dz       int16
		yaw, pitch, head byte
	}
	moves := make(map[*player]*delta, len(players))
	for _, t := range players {
		if !t.psHas {
			t.psX, t.psY, t.psZ = t.x, t.y, t.z
			t.psYaw, t.psPitch = t.yaw, t.pitch
			t.psHas = true
			continue
		}
		dx := t.x - t.psX
		dy := t.y - t.psY
		dz := t.z - t.psZ
		big := math.Abs(dx) > 7.9 || math.Abs(dy) > 7.9 || math.Abs(dz) > 7.9
		moved := dx != 0 || dy != 0 || dz != 0 || t.yaw != t.psYaw || t.pitch != t.psPitch
		if moved {
			moves[t] = &delta{
				p:       t,
				moved:   true,
				bigMove: big,
				dx:      shortDelta(dx), dy: shortDelta(dy), dz: shortDelta(dz),
				yaw: angleByte(t.yaw), pitch: angleByte(t.pitch), head: angleByte(t.yaw),
			}
			t.psX, t.psY, t.psZ = t.x, t.y, t.z
			t.psYaw, t.psPitch = t.yaw, t.pitch
		}
	}

	type spawnJob struct {
		observer *player
		body     []byte
	}
	var spawns []spawnJob
	type despawnJob struct {
		observer *player
		targetID int32
	}
	var despawns []despawnJob
	for _, o := range players {
		if o.seenPlayers == nil {
			o.seenPlayers = make(map[int32]bool)
		}
		for _, t := range players {
			if o == t {
				continue
			}
			dx := o.x - t.x
			dy := o.y - t.y
			dz := o.z - t.z
			d2 := dx*dx + dy*dy + dz*dz
			if d2 <= entityTrackRange2 && !o.seenPlayers[t.id] {
				o.seenPlayers[t.id] = true
				w := protocol.NewWriter()
				w.VarInt(v776.PacketPlayAddEntity)
				java.WriteAddEntity(w, t.id, t.conn.profileID, v776.EntityTypePlayer,
					t.x, t.y, t.z, 0, 0, 0, angleByte(t.pitch), angleByte(t.yaw), angleByte(t.yaw), 0)
				spawns = append(spawns, spawnJob{observer: o, body: w.Bytes()})
			} else if d2 > entityTrackRange2+entityDespawnSlop2 && o.seenPlayers[t.id] {
				delete(o.seenPlayers, t.id)
				despawns = append(despawns, despawnJob{observer: o, targetID: t.id})
			}
		}
	}
	// Build the move bodies once per moving target (all observers share).
	moveBodies := make(map[*player][][]byte, len(moves))
	for t, d := range moves {
		var bodies [][]byte
		if d.bigMove {
			// Delta shorts would overflow: full position sync instead.
			w := protocol.NewWriter()
			w.VarInt(v776.PacketPlayPosSync)
			java.WriteEntityPositionSync(w, t.id, t.x, t.y, t.z, 0, 0, 0, t.yaw, t.pitch, t.onGround)
			bodies = append(bodies, w.Bytes())
		} else {
			w := protocol.NewWriter()
			w.VarInt(v776.PacketPlayMoveEntityPosRot)
			java.WriteMoveEntityPosRot(w, t.id, d.dx, d.dy, d.dz, d.yaw, d.pitch, t.onGround)
			bodies = append(bodies, w.Bytes())
			// Players have no separate head pivot; skip rotate_head.
		}
		moveBodies[t] = bodies
	}
	s.mu.Unlock()

	for _, j := range spawns {
		_ = j.observer.conn.sendPacket(j.body)
	}
	for _, j := range despawns {
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlayRemoveEntities)
		java.WriteRemoveEntities(body, []int32{j.targetID})
		_ = j.observer.conn.sendPacket(body.Bytes())
	}
	for t, bodies := range moveBodies {
		s.mu.Lock()
		var watchers []*player
		for _, o := range players {
			if o != t && o.seenPlayers[t.id] {
				watchers = append(watchers, o)
			}
		}
		s.mu.Unlock()
		for _, body := range bodies {
			for _, o := range watchers {
				_ = o.conn.sendPacket(body)
			}
		}
	}
}

// broadcastRemovePlayer retires a disconnecting player from every tab and
// tracker. Called from removePlayer (conn teardown).
func (s *Server) broadcastRemovePlayer(p *player) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, o := range s.players {
		if o == p {
			continue
		}
		if o.seenPlayers[p.id] {
			delete(o.seenPlayers, p.id)
			body := protocol.NewWriter()
			body.VarInt(v776.PacketPlayRemoveEntities)
			java.WriteRemoveEntities(body, []int32{p.id})
			_ = o.conn.sendPacket(body.Bytes())
		}
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlayPlayerInfoRemove)
		java.WritePlayPlayerInfoRemove(body, []([16]byte){p.conn.profileID})
		_ = o.conn.sendPacket(body.Bytes())
	}
}

// broadcastEntityEventToTrackers sends an entity event (e.g. the death
// animation) to every player tracking the entity, excluding the owner.
// Caller holds s.mu.
func (s *Server) broadcastEntityEventToTrackers(entityID int32, event byte, exclude *player) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayEntityEvent)
	java.WriteEntityEvent(body, entityID, event)
	for _, o := range s.players {
		if o != exclude && o.seenPlayers[entityID] {
			_ = o.conn.sendPacket(body.Bytes())
		}
	}
}

// broadcastSwing shows the attacker's arm swing on every other client
// that tracks the attacker. Caller holds s.mu.
func (s *Server) broadcastSwing(attacker *player) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayAnimate)
	java.WriteAnimate(body, attacker.id, java.AnimateSwingMainHand)
	for _, o := range s.players {
		if o != attacker && o.seenPlayers[attacker.id] {
			_ = o.conn.sendPacket(body.Bytes())
		}
	}
}

// --- experience ------------------------------------------------------------

// xpToNextLevel is the vanilla level-up cost curve.
func xpToNextLevel(level int32) int32 {
	switch {
	case level >= 31:
		return 9*level - 158
	case level >= 16:
		return 5*level - 38
	default:
		return 2*level + 7
	}
}

// addXP grants experience points and pushes the XP bar. Caller holds s.mu.
func (p *player) addXP(v int32) {
	if v <= 0 {
		return
	}
	p.expTotal += v
	p.expProgress += float32(v) / float32(xpToNextLevel(p.expLevel))
	for p.expProgress >= 1 && p.expLevel < 24791 { // vanilla int-overflow guard
		p.expProgress -= 1
		p.expLevel++
		// Recompute progress against the new level's cost: the leftover
		// fraction rescales because one point is worth less now.
		p.expProgress = p.expProgress * float32(xpToNextLevel(p.expLevel-1)) / float32(xpToNextLevel(p.expLevel))
	}
	p.sendExperience()
}
