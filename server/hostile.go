package server

import (
	"log"
	"math"
	"math/rand"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M9 hostile mobs: zombie, skeleton and creeper share the passive-mob
// physics skeleton from mobs.go but swap the wander brain for a chase
// brain. Because mobEntity.tick holds the mob's own mutex, nothing here
// may take Server.mu from inside tick: the ticker pre-pass
// (prepareMobTicks) snapshots targets and daylight into the mob, and the
// brain only records *intents* (melee / shoot / explode / fire damage)
// that tickEntities consumes afterwards on the safe s.mu -> m.mu order.

// Vanilla hostile constants (normal difficulty).
const (
	hostileFollowRange2 = 24 * 24   // squared target acquisition range
	hostileDespawn2     = 96 * 96   // squared despawn range (no players inside)
	zombieAttackDamage  = 3.0       // normal-difficulty zombie melee
	zombieAttackReach2  = 1.2 * 1.2 // squared melee trigger distance
	zombieAttackPeriod  = 20        // ticks between swings
	skeletonShootRange2 = 10 * 10   // squared bow range
	skeletonBackoff2    = 5 * 5     // squared retreat distance
	skeletonShootPeriod = 40        // normal-difficulty arrow interval
	arrowSpeed          = 1.6       // blocks per tick at launch
	arrowInaccuracy     = 6         // normal difficulty spread parameter
	creeperSwellStart2  = 3 * 3     // squared fuse trigger distance
	creeperMaxSwell     = 30        // fuse length in ticks
	creeperRadius       = 3         // explosion radius (powered ×2 ignored)
	fireTickDamageEvery = 20        // one fire damage per second
	burnRefreshTicks    = 160       // vanilla 8-second sun fire refresh
)

// hostileDefs carry the per-species combat constants; physics and drops
// reuse the mobDef table.
var hostileDefs = []mobDef{
	{name: "zombie", typeID: v776.EntityTypeZombie, width: 0.6, height: 1.95, maxHealth: 20, speed: 0.23, hostile: true, drops: []mobDrop{
		{item: "minecraft:rotten_flesh", min: 0, max: 2},
	}},
	{name: "skeleton", typeID: v776.EntityTypeSkeleton, width: 0.6, height: 1.99, maxHealth: 20, speed: 0.25, hostile: true, drops: []mobDrop{
		{item: "minecraft:bone", min: 0, max: 2},
		{item: "minecraft:arrow", min: 0, max: 2},
	}},
	{name: "creeper", typeID: v776.EntityTypeCreeper, width: 0.6, height: 1.7, maxHealth: 20, speed: 0.25, hostile: true, drops: []mobDrop{
		{item: "minecraft:gunpowder", min: 0, max: 2},
	}},
}

// burnsInDay reports whether the species ignites in sunlight (undead do).
func (d *mobDef) burnsInDay() bool { return d.hostile && (d.name == "zombie" || d.name == "skeleton") }

// prepareMobTicks runs before the entity tick loop: under s.mu it
// snapshots the nearest valid target and the daylight flag into every
// hostile mob, despawns stragglers and refreshes sunburn. Lock order
// stays s.mu -> m.mu.
func (s *Server) prepareMobTicks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := s.isDayLocked()
	for _, e := range s.entities {
		m, ok := e.(*mobEntity)
		if !ok {
			continue
		}
		m.mu.Lock()
		if m.deathTicks > 0 {
			m.mu.Unlock()
			continue
		}
		m.dayLight = day

		// Hostiles vanish like vanilla monsters when nobody is near.
		if m.def.hostile {
			nearest := int32(0)
			var best float64 = hostileFollowRange2
			var bx, by, bz float64
			for _, p := range s.players {
				if p.dead || p.gameMode != 0 {
					continue
				}
				dx := p.x - m.x
				dy := p.y - m.y
				dz := p.z - m.z
				d2 := dx*dx + dy*dy + dz*dz
				if d2 > hostileDespawn2 {
					continue
				}
				if d2 < best {
					best = d2
					nearest = p.id
					bx, by, bz = p.x, p.y, p.z
				}
			}
			if nearest == 0 {
				// Nobody within despawn range: gone.
				m.removed = true
				m.mu.Unlock()
				continue
			}
			m.targetID = nearest
			m.tx, m.ty, m.tz = bx, by, bz
		}
		m.mu.Unlock()
	}
}

// hostileBrain is the per-tick chase logic; it runs inside
// mobEntity.tick under m.mu and must not lock anything else. All server
// mutations are deferred through the pending* intent flags.
func (m *mobEntity) hostileBrain() {
	// Sunlight burn: while outdoors during the day the fire visual stays
	// pinned (vanilla refreshes it every tick) and a separate counter
	// lands one damage per second.
	if m.def.burnsInDay() && m.dayLight {
		if m.fireTicks <= 0 {
			m.metaDirty = true
		}
		m.fireTicks = burnRefreshTicks
		m.fireDmgCounter++
		if m.fireDmgCounter >= fireTickDamageEvery {
			m.fireDmgCounter = 0
			m.pendingFireDmg = true
		}
	} else if m.fireTicks > 0 {
		m.fireTicks--
		if m.fireTicks == 0 {
			m.metaDirty = true
		}
	}

	m.attackCD--
	m.shootCD--

	// No target in range this tick: creepers decay their fuse, everyone
	// else falls back to idle wandering (never chase a stale snapshot).
	if m.targetID == 0 {
		if m.def.name == "creeper" && m.swell > 0 {
			m.swellDir = -1
			m.swell += m.swellDir
			if m.swell < 0 {
				m.swell = 0
			}
			m.metaDirty = true
		}
		m.wanderBrain()
		return
	}

	dx := m.tx - m.x
	dy := m.ty - m.y
	dz := m.tz - m.z
	hd2 := dx*dx + dz*dz

	switch m.def.name {
	case "zombie":
		// Chase relentlessly and swing when in reach.
		if hd2 > zombieAttackReach2 || math.Abs(dy) > 2 {
			m.faceTowards(dx, dz)
			m.walking = true
		} else {
			m.walking = false
			m.vx, m.vz = 0, 0
			if m.attackCD <= 0 {
				m.attackCD = zombieAttackPeriod
				m.pendingMelee = true
			}
		}

	case "skeleton":
		if hd2 < skeletonBackoff2 {
			// Too close: back away while keeping the target in sight.
			m.faceTowards(-dx, -dz)
			m.walking = true
		} else if hd2 > skeletonShootRange2 {
			m.faceTowards(dx, dz)
			m.walking = true
		} else {
			m.walking = false
			m.vx, m.vz = 0, 0
			if m.shootCD <= 0 {
				m.shootCD = skeletonShootPeriod
				m.pendingShoot = true
			}
		}

	case "creeper":
		if hd2 < creeperSwellStart2 && math.Abs(dy) < 2 {
			// Prime the fuse; vanilla freezes while swelling.
			if m.swellDir <= 0 && m.swell == 0 {
				m.wasPrimed = false
			}
			m.swellDir = 1
			m.walking = false
			m.vx, m.vz = 0, 0
		} else {
			m.swellDir = -1
			m.faceTowards(dx, dz)
			m.walking = true
		}
		if m.swellDir > 0 && m.swell == 0 && !m.wasPrimed {
			m.wasPrimed = true
			m.pendingPrimedSound = true
		}
		m.swell += m.swellDir
		if m.swell < 0 {
			m.swell = 0
		}
		if m.swell >= creeperMaxSwell {
			m.pendingExplode = true
		}
		m.metaDirty = true // swell direction feeds the client animation
	}
}

// faceTowards turns the body toward a horizontal direction. Caller holds
// m.mu.
func (m *mobEntity) faceTowards(dx, dz float64) {
	if dx*dx+dz*dz < 1e-9 {
		return
	}
	m.yaw = float32(math.Atan2(-dx, dz) * 180 / math.Pi)
}

// hostileMeleeLocked delivers a zombie swing. Caller holds s.mu.
func (s *Server) hostileMeleeLocked(m *mobEntity) {
	m.mu.Lock()
	target := m.targetID
	m.mu.Unlock()
	var victim *player
	for _, p := range s.players {
		if p.id == target {
			victim = p
			break
		}
	}
	if victim == nil || victim.dead || victim.gameMode != 0 {
		return
	}
	m.mu.Lock()
	mx, my, mz := m.x, m.y, m.z
	m.mu.Unlock()
	dx := victim.x - mx
	dy := victim.y - my
	dz := victim.z - mz
	if dx*dx+dy*dy+dz*dz > zombieAttackReach2*2 {
		return // target slipped away between tick and consume
	}
	s.damagePlayerLocked(victim, zombieAttackDamage, v776.DamageTypeMobAttack, m.id, m.id)
	// Vanilla knockback: push the victim away and pop it up.
	d := math.Sqrt(dx*dx + dz*dz)
	if d > 0.001 {
		kx, kz := dx/d*mobKnockbackH, dz/d*mobKnockbackH
		kb := protocol.NewWriter()
		kb.VarInt(v776.PacketPlaySetEntityMotion)
		java.WriteSetEntityMotion(kb, victim.id, kx, mobKnockbackV, kz)
		_ = victim.conn.sendPacket(kb.Bytes())
	}
}

// skeletonShootLocked fires one arrow at the snapshotted target.
// Caller holds s.mu.
func (s *Server) skeletonShootLocked(m *mobEntity) {
	m.mu.Lock()
	target := m.targetID
	sx, sy, sz := m.x, m.y+1.45, m.z // eye height
	m.mu.Unlock()
	var victim *player
	for _, p := range s.players {
		if p.id == target {
			victim = p
			break
		}
	}
	if victim == nil || victim.dead {
		return
	}
	dx := victim.x - sx
	dy := (victim.y + 0.6) - sy
	dz := victim.z - sz
	hd := math.Sqrt(dx*dx + dz*dz)
	dir := [3]float64{dx, dy + hd*0.2, dz} // vanilla's ballistic lead
	norm := math.Sqrt(dir[0]*dir[0] + dir[1]*dir[1] + dir[2]*dir[2])
	if norm < 0.001 {
		return
	}
	a := arrowSpeed / norm
	e := &arrowEntity{
		id:      s.allocEntityID(),
		shooter: m.id,
		x:       sx, y: sy, z: sz,
		vx: dir[0]*a + rand.NormFloat64()*0.0075*arrowInaccuracy,
		vy: dir[1]*a + rand.NormFloat64()*0.0075*arrowInaccuracy,
		vz: dir[2]*a + rand.NormFloat64()*0.0075*arrowInaccuracy,
	}
	_, _ = rand.Read(e.uuid[:])
	e.uuid[6] = (e.uuid[6] & 0x0F) | 0x40
	e.uuid[8] = (e.uuid[8] & 0x3F) | 0x80
	e.updateRotation()
	s.spawnEntityLocked(e)
	s.broadcastSoundLocked("minecraft:entity.skeleton.shoot", v776.SoundSourceHostile,
		float32(sx), float32(sy), float32(sz), 1.0, randomPitch())
}

// explodeCreeperLocked runs the creeper blast: flash + sound on the
// client, crater in the world, damage and knockback to everything alive
// nearby. Caller holds s.mu. The creeper itself discards without loot
// (vanilla discard()).
func (s *Server) explodeCreeperLocked(m *mobEntity) {
	m.mu.Lock()
	cx, cy, cz := m.x, m.y+0.0625, m.z
	m.fireTicks = 0
	m.deathTicks = 0
	m.removed = true
	m.mu.Unlock()

	const r = creeperRadius
	const reach = 2 * r

	// Client feedback: one explosion packet per player with its own
	// knockback (vanilla sends the per-player vector inside the packet).
	for _, p := range s.players {
		dx := p.x - cx
		dy := (p.y + 0.9) - cy
		dz := p.z - cz
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		var kx, ky, kz float64
		if d < reach && d > 0.001 {
			frac := 1 - d/reach
			kx = dx / d * frac * 1.2
			ky = math.Max(0.3*frac, 0.15)
			kz = dz / d * frac * 1.2
		}
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlayExplode)
		java.WritePlayExplode(body, cx, cy, cz, r, 0, kx, ky, kz,
			29 /* explosion_emitter */, "minecraft:entity.generic.explode")
		_ = p.conn.sendPacket(body.Bytes())
	}

	// Block destruction: a sphere with vanilla's 1/radius drop chance.
	destroyed := int32(0)
	for bx := int(cx) - r; bx <= int(cx)+r; bx++ {
		for by := int(cy) - r; by <= int(cy)+r; by++ {
			for bz := int(cz) - r; bz <= int(cz)+r; bz++ {
				ddx, ddy, ddz := float64(bx)+0.5-cx, float64(by)+0.5-cy, float64(bz)+0.5-cz
				if ddx*ddx+ddy*ddy+ddz*ddz > r*r {
					continue
				}
				st := s.world.getBlock(bx, by, bz)
				if st == stateAir {
					continue
				}
				if !s.world.setBlock(bx, by, bz, stateAir) {
					continue
				}
				destroyed++
				s.broadcastBlockUpdateLocked(int32(bx), int32(by), int32(bz), stateAir)
				if rand.Int31n(r) == 0 {
					s.spawnExplosionDrop(bx, by, bz, st)
				}
			}
		}
	}

	// Entity damage: vanilla curve (1-d/2R) shaped, both players and mobs.
	for _, p := range s.players {
		dx := p.x - cx
		dy := (p.y + 0.9) - cy
		dz := p.z - cz
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if d >= reach {
			continue
		}
		frac := 1 - d/reach
		dmg := float32((frac*frac+frac)/2*7*r + 1)
		s.damagePlayerLocked(p, dmg, v776.DamageTypeExplosion, m.id, m.id)
	}
	for _, e := range s.entities {
		m2, ok := e.(*mobEntity)
		if !ok || m2 == m {
			continue
		}
		m2.mu.Lock()
		if m2.removed || m2.deathTicks > 0 {
			m2.mu.Unlock()
			continue
		}
		dx := m2.x - cx
		dy := m2.y + float64(m2.def.height)/2 - cy
		dz := m2.z - cz
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if d < reach {
			frac := 1 - d/reach
			dmg := float32((frac*frac+frac)/2*7*r + 1)
			m2.vx += dx / max(d, 0.01) * frac * 1.2
			m2.vy += frac * 0.4
			m2.vz += dz / max(d, 0.01) * frac * 1.2
			m2.mu.Unlock()
			s.damageMobLocked(m2, dmg, v776.DamageTypeExplosion, m.id, m.id)
		} else {
			m2.mu.Unlock()
		}
	}
}

// spawnExplosionDrop scatters a block drop with the crater's 1-in-3
// chance. Caller holds s.mu (uses the locked spawn path).
func (s *Server) spawnExplosionDrop(x, y, z int, state int32) {
	name := blockNameOf(int(state))
	if name == "" {
		return
	}
	drop, ok := blockDrops(name)
	if !ok {
		drop = name
	}
	itemID, ok := itemIDByName[drop]
	if !ok {
		return
	}
	e := newItemEntity(s.allocEntityID(), float64(x)+0.5, float64(y)+0.4, float64(z)+0.5, itemID, 1)
	s.spawnEntityLocked(e)
}

// topUpHostiles refills the hostile population at night (vanilla spawns
// monsters in the dark; the superflat surface has no light data yet, so
// the clock is the only gate). Runs every 20 seconds from tickOnce.
func (s *Server) topUpHostiles() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.players) == 0 || !s.isNightLocked() {
		return
	}
	const hostileCap = 8
	count := 0
	for _, e := range s.entities {
		if m, ok := e.(*mobEntity); ok && m.def.hostile {
			count++
		}
	}
	if count >= hostileCap {
		return
	}
	var anchor *player
	for _, p := range s.players {
		anchor = p
		break
	}
	if anchor == nil {
		return
	}
	budget := hostileCap - count
	if budget > 2 {
		budget = 2
	}
	spawned := 0
	for n := 0; n < budget; n++ {
		def := &hostileDefs[rand.Intn(len(hostileDefs))]
		angle := rand.Float64() * 2 * math.Pi
		radius := 24 + rand.Float64()*16
		x := math.Round(anchor.x+math.Cos(angle)*radius) + 0.5
		z := math.Round(anchor.z+math.Sin(angle)*radius) + 0.5
		y := float64(s.surfaceY(int(x), int(z)))
		if s.boxCollides(x, y, z, def.width, def.height) {
			continue
		}
		m := newMobEntity(def, s.allocEntityID(), x, y, z)
		s.entities[m.id] = m
		spawned++
	}
	if spawned > 0 {
		log.Printf(ui.Success("OK ")+"夜幕降临：已在玩家周围生成 %d 只敌对生物（僵尸/骷髅/爬行者）", spawned)
	}
}
