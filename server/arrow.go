package server

import (
	"math"
	"sync"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M9/M11 arrow projectile. Skeletons fire them at players; M11 adds
// player-shot arrows that can hit mobs (and other players) and be
// collected again. The server runs the authoritative flight (vanilla
// gravity 0.05, inertia 0.99) while the client free-simulates from the
// initial velocity in add_entity, so move-delta corrections stream
// exactly like they do for items. Hit detection runs on the tick loop;
// the actual damage applies in the ticker's consume pass to respect the
// s.mu -> e.mu lock order.

// Vanilla AbstractArrow constants.
const (
	arrowGravity     = 0.05
	arrowInertia     = 0.99
	arrowBaseDamage  = 2.0
	arrowLifespan    = 1200 // stuck arrows despawn after 60 s
	arrowPotionDecay = 600  // Arrow.EXPOSED_POTION_DECAY_TIME：药水箭插墙褪色
)

// arrowEntity is one flying or stuck arrow.
type arrowEntity struct {
	mu         sync.Mutex
	id         int32
	uuid       [16]byte
	x, y, z    float64
	vx, vy, vz float64
	yaw, pitch float32
	shooter    int32

	// M11: fromPlayer marks player-shot arrows (they may hit mobs and
	// other players); pickup marks collectible stuck arrows (survival
	// shots only, like vanilla).
	fromPlayer bool
	pickup     bool

	stuck bool
	age   int32

	// M14 药水箭与暴击：potion 是药水注册 ID+1（0 = 普通箭）；crit 对应
	// AbstractArrow 的 FLAG_CRIT（满蓄力射击）；groundTime 统计插墙
	// tick，药水箭满 600t 褪色回普通箭（Arrow.tick 移植）。
	potion     int32
	crit       bool
	groundTime int32

	px, py, pz float64 // position at the start of this tick (segment hits)

	nx, ny, nz   float64
	hasNet       bool
	nyaw, npitch float32
	hasRot       bool
	dead         bool
	pendingHit   int32 // player entity id this arrow hit, consumed by the ticker
	pendingMob   int32 // mob entity id this arrow hit (M11 player arrows)
	aMeta        bool  // 元数据脏标记（crit/color 变化时置位）
}

func (e *arrowEntity) entityID() int32 { return e.id }
func (e *arrowEntity) typeID() int32   { return v776.EntityTypeArrow }
func (e *arrowEntity) xPos() (float64, float64, float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.x, e.y, e.z
}
func (e *arrowEntity) onGroundFlag() bool { return false }
func (e *arrowEntity) alive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.dead
}
func (e *arrowEntity) metadataDirty() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.aMeta
}
func (e *arrowEntity) clearMetadataDirty() {
	e.mu.Lock()
	e.aMeta = false
	e.mu.Unlock()
}
func (e *arrowEntity) netPos() (float64, float64, float64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nx, e.ny, e.nz, e.hasNet
}
func (e *arrowEntity) setNetPos(x, y, z float64) {
	e.mu.Lock()
	e.nx, e.ny, e.nz, e.hasNet = x, y, z, true
	e.mu.Unlock()
}
func (e *arrowEntity) curRot() (float32, float32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.yaw, e.pitch
}
func (e *arrowEntity) netRot() (float32, float32, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nyaw, e.npitch, e.hasRot
}
func (e *arrowEntity) setNetRot(yaw, pitch float32) {
	e.mu.Lock()
	e.nyaw, e.npitch, e.hasRot = yaw, pitch, true
	e.mu.Unlock()
}

// updateRotation derives the render orientation from the velocity
// (vanilla lerps it, but per-tick recomputation is close enough).
// Caller holds e.mu.
func (e *arrowEntity) updateRotation() {
	h := math.Sqrt(e.vx*e.vx + e.vz*e.vz)
	e.yaw = float32(math.Atan2(e.vx, e.vz) * 180 / math.Pi)
	e.pitch = float32(math.Atan2(e.vy, h) * 180 / math.Pi)
}

// tick advances one server tick: ballistic flight, block impact and the
// player-hit segment test. Runs on the ticker goroutine.
func (e *arrowEntity) tick(s *Server) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.age++
	if e.age >= arrowLifespan {
		e.dead = true
		return
	}
	if e.stuck {
		// M14：药水箭插墙 600t 后褪色（EXPOSED_POTION_DECAY_TIME）；
		// vanilla 会广播实体事件 0（药水消散粒子），此处只保留数据态。
		if e.potion != 0 {
			e.groundTime++
			if e.groundTime >= arrowPotionDecay {
				e.potion = 0
				e.aMeta = true
			}
		}
		return
	}

	e.px, e.py, e.pz = e.x, e.y, e.z
	e.vx *= arrowInertia
	e.vy = e.vy*arrowInertia - arrowGravity
	e.vz *= arrowInertia
	e.x += e.vx
	e.y += e.vy
	e.z += e.vz
	e.updateRotation()

	// Block impact: the 0.25 probe box touching anything solid sticks the
	// arrow where it stands (vanilla stores the exact entry face; the
	// visual difference is negligible at this speed).
	if s.entityBoxCollides(e.x, e.y, e.z) {
		e.x -= e.vx
		e.y -= e.vy
		e.z -= e.vz
		e.vx, e.vy, e.vz = 0, 0, 0
		e.stuck = true
		return
	}

	// Entity hits: solve the closest approach of this tick's flight
	// segment to every candidate body. Skeleton arrows hit players;
	// player arrows hit mobs and other players (PvP). Damage scales
	// with impact speed like vanilla.
	s.mu.Lock()
	var bestPlayer *player
	var bestMob *mobEntity
	bestD2 := 1e9
	for _, p := range s.players {
		if p.dead || p.gameMode != 0 {
			continue
		}
		if e.fromPlayer && p.id == e.shooter {
			continue // never hit the shooter
		}
		d2 := segmentBoxDist2(e.px, e.py, e.pz, e.x, e.y, e.z, p.x, p.y, p.z)
		if d2 < bestD2 {
			bestD2 = d2
			bestPlayer = p
		}
	}
	if e.fromPlayer {
		for _, o := range s.entities {
			m, ok := o.(*mobEntity)
			if !ok {
				continue
			}
			m.mu.Lock()
			dying := m.deathTicks > 0 || m.removed || m.health <= 0
			mx, my, mz := m.x, m.y, m.z
			hw, h := float64(m.def.width)/2, float64(m.def.height)
			m.mu.Unlock()
			if dying {
				continue
			}
			d2 := segmentBoxDist2Dims(e.px, e.py, e.pz, e.x, e.y, e.z, mx, my, mz, hw, h)
			if d2 < bestD2 {
				bestD2 = d2
				bestPlayer = nil
				bestMob = m
			}
		}
	}
	s.mu.Unlock()
	if bestD2 < 0.45*0.45 {
		if bestPlayer != nil {
			e.pendingHit = bestPlayer.id
			if !e.fromPlayer {
				e.stuck = true // M9 behavior: skeleton arrows stick on hit
			}
		} else if bestMob != nil {
			e.pendingMob = bestMob.id
		}
		if bestPlayer == nil && bestMob == nil {
			// Vanilla removes the arrow on any entity hit; M11 drops it
			// here so the consume pass despawns it cleanly.
			e.dead = true
		}
	}
}

// segmentBoxDist2 returns the squared distance between the segment
// (x1,y1,z1)-(x2,y2,z2) and the player body box (0.6 wide, 1.8 tall,
// feet at bx/by/bz).
func segmentBoxDist2(x1, y1, z1, x2, y2, z2, bx, by, bz float64) float64 {
	return segmentBoxDist2Dims(x1, y1, z1, x2, y2, z2, bx, by, bz, 0.3, 1.8)
}

// segmentBoxDist2Dims is the sized variant used for mobs: halfW is the
// body half-width, h the full height.
func segmentBoxDist2Dims(x1, y1, z1, x2, y2, z2, bx, by, bz, halfW, h float64) float64 {
	dx, dy, dz := x2-x1, y2-y1, z2-z1
	length2 := dx*dx + dy*dy + dz*dz
	t := closestSegmentParam(x1, y1, z1, dx, dy, dz, length2, bx, by, bz, h)
	cx, cy, cz := x1+dx*t, y1+dy*t, z1+dz*t
	// Expand the box by the arrow radius, then clamp the point in.
	px := clampF(cx, bx-halfW, bx+halfW)
	py := clampF(cy, by, by+h)
	pz := clampF(cz, bz-halfW, bz+halfW)
	ex, ey, ez := cx-px, cy-py, cz-pz
	return ex*ex + ey*ey + ez*ez
}

// closestSegmentParam finds the segment parameter t in [0,1] minimizing
// the distance to the box center line (bodies are tall; testing against
// the vertical center axis handles body hits without a full SAT solve).
func closestSegmentParam(x1, y1, z1, dx, dy, dz, length2, bx, by, bz, h float64) float64 {
	// Target the body's mid-height point.
	tx, ty, tz := bx, by+h*0.5, bz
	t := ((tx-x1)*dx + (ty-y1)*dy + (tz-z1)*dz) / length2
	if t < 0 {
		t = 0
	}
	if t > 1 {
		t = 1
	}
	return t
}

func clampF(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// consumeHits applies deferred arrow hits and collects stuck player-shot
// arrows. Runs on the ticker goroutine after the tick loop, under s.mu.
func (s *Server) consumeArrowHits() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, e := range s.entities {
		a, ok := e.(*arrowEntity)
		if !ok {
			continue
		}
		a.mu.Lock()
		hit := a.pendingHit
		a.pendingHit = 0
		mobHit := a.pendingMob
		a.pendingMob = 0
		speed := math.Sqrt(a.vx*a.vx + a.vy*a.vy + a.vz*a.vz)
		ax, ay, az := a.x, a.y, a.z
		collect := a.stuck && a.pickup
		a.mu.Unlock()

		dmg := float32(math.Ceil(speed * arrowBaseDamage))
		if dmg < 1 {
			dmg = 1
		}

		if hit != 0 {
			var victim *player
			for _, p := range s.players {
				if p.id == hit {
					victim = p
					break
				}
			}
			if victim != nil && !victim.dead && victim.gameMode == 0 {
				s.damagePlayerLocked(victim, dmg, v776.DamageTypeArrow, a.shooter, a.id)
				// Knockback along the arrow's travel direction.
				dx := victim.x - ax
				dz := victim.z - az
				d := math.Sqrt(dx*dx + dz*dz)
				if d > 0.001 {
					kx, kz := dx/d*mobKnockbackH, dz/d*mobKnockbackH
					kb := protocol.NewWriter()
					kb.VarInt(v776.PacketPlaySetEntityMotion)
					java.WriteSetEntityMotion(kb, victim.id, kx, 0.35, kz)
					_ = victim.conn.sendPacket(kb.Bytes())
				}
			}
		}

		if mobHit != 0 {
			// M11: a player arrow that lands on a mob discards
			// itself like vanilla and credits the shooter.
			a.mu.Lock()
			a.dead = true
			a.mu.Unlock()
			m, isMob := s.entities[mobHit].(*mobEntity)
			if !isMob {
				continue
			}
			var shooter *player
			for _, p := range s.players {
				if p.id == a.shooter {
					shooter = p
					break
				}
			}
			if shooter != nil {
				s.hurtMobTypeLocked(m, shooter, dmg, v776.DamageTypeArrow)
			} else {
				s.damageMobLocked(m, dmg, v776.DamageTypeArrow, -1, -1)
			}
			continue
		}

		if collect {
			// Walk-over collection for stuck survival arrows:
			// any survival player within 1.2 blocks takes the
			// arrow back (vanilla entity.arrow.pickup).
			for _, p := range s.players {
				if p.dead || p.gameMode != 0 {
					continue
				}
				dx := p.x - ax
				dy := (p.y + 0.9) - ay
				dz := p.z - az
				if dx*dx+dy*dy+dz*dz > 1.44 {
					continue
				}
				remaining := p.giveItem(itemIDByName[arrowItemName], 1)
				if remaining == 0 {
					a.mu.Lock()
					a.dead = true
					a.mu.Unlock()
					s.broadcastSoundLocked("minecraft:entity.arrow.pickup",
						v776.SoundSourcePlayers, float32(ax), float32(ay), float32(az), 1.0, randomPitch())
				}
				break
			}
		}
	}
}

// encodeArrowSpawn writes the add_entity payload for an arrow, carrying
// the launch velocity so the client can free-simulate between server
// corrections.
func encodeArrowSpawn(w *protocol.Writer, a *arrowEntity) {
	a.mu.Lock()
	defer a.mu.Unlock()
	java.WriteAddEntity(w, a.id, a.uuid, a.typeID(), a.x, a.y, a.z,
		a.vx, a.vy, a.vz,
		angleByte(a.pitch), angleByte(a.yaw), angleByte(a.yaw), 0)
}

// writeArrowMetadata encodes the set_entity_data frame for an arrow: the
// crit flag (index 8) and the tipped-arrow potion color (index 11) —
// entries are emitted only when non-default, mirroring vanilla's
// getNonDefaultValues (M14).
func writeArrowMetadata(w *protocol.Writer, a *arrowEntity) {
	a.mu.Lock()
	defer a.mu.Unlock()
	flags := byte(0)
	if a.crit {
		flags |= v776.ArrowFlagCrit
	}
	color := int32(-1) // Arrow.NO_EFFECT_COLOR
	if a.potion > 0 {
		color = potionColor(a.potion - 1)
	}
	if flags == 0 && color == -1 {
		return // 无差异：不发空帧（空帧会中断客户端解码器）
	}
	w.VarInt(v776.PacketPlaySetEntityData)
	w.VarInt(a.id)
	if flags != 0 {
		w.Byte(v776.MetaIndexArrowFlags)
		w.VarInt(v776.MetaSerializerByte)
		w.Byte(flags)
	}
	if color != -1 {
		w.Byte(v776.MetaIndexArrowEffectColor)
		w.VarInt(v776.MetaSerializerVarInt)
		w.VarInt(color) // INT 序列化器即 VAR_INT；负数走 5 字节全形
	}
	w.Byte(0xFF)
}
