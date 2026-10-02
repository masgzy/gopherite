package server

import (
	"log"

	"github.com/masgzy/gopherite/internal/ui"
	"math"
	"math/rand"
	"sync"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M8 passive mobs: pig, cow, sheep and chicken share one physics/AI
// skeleton. They wander, hop up single blocks, panic when hurt and die
// with the vanilla fall-over animation plus item drops. Mobs are not
// persisted across restarts (the superflat world has no natural spawn
// biome data yet); a starter herd is seeded around the world spawn on
// every boot.

// mobDef carries the per-species constants.
type mobDef struct {
	name      string
	typeID    int32 // entity_type registry id
	width     float64
	height    float64
	maxHealth float32
	speed     float64 // wander target speed, blocks per tick
	hostile   bool    // M9: zombie/skeleton/creeper chase brains
	drops     []mobDrop
}

// mobDrop is one weighted drop: min..max count of an item.
type mobDrop struct {
	item string
	min  int32
	max  int32
}

var mobDefs = []mobDef{
	{name: "pig", typeID: v776.EntityTypePig, width: 0.9, height: 0.9, maxHealth: 10, speed: 0.10, drops: []mobDrop{
		{item: "minecraft:porkchop", min: 1, max: 2},
	}},
	{name: "cow", typeID: v776.EntityTypeCow, width: 0.9, height: 1.4, maxHealth: 10, speed: 0.10, drops: []mobDrop{
		{item: "minecraft:beef", min: 1, max: 2},
		{item: "minecraft:leather", min: 0, max: 2},
	}},
	{name: "sheep", typeID: v776.EntityTypeSheep, width: 0.9, height: 1.3, maxHealth: 8, speed: 0.10, drops: []mobDrop{
		{item: "minecraft:mutton", min: 1, max: 1},
		{item: "minecraft:white_wool", min: 1, max: 1},
	}},
	{name: "chicken", typeID: v776.EntityTypeChicken, width: 0.4, height: 0.7, maxHealth: 4, speed: 0.11, drops: []mobDrop{
		{item: "minecraft:chicken", min: 1, max: 1},
		{item: "minecraft:feather", min: 0, max: 2},
	}},
}

// Vanilla living-entity physics constants.
const (
	mobGravity        = 0.08
	mobAirDrag        = 0.98
	mobGroundFriction = 0.55 // 0.6 slipperiness * 0.91
	mobJumpVelocity   = 0.42
	mobTerminalVy     = -3.92
	mobKnockbackH     = 0.45
	mobKnockbackV     = 0.4
	mobDeathAnimTicks = 20
	mobAttackCooldown = 10 // ticks between melee swings
)

// rotator is implemented by entities whose yaw the client tracks
// separately from position (move_entity_pos_rot + rotate_head).
type rotator interface {
	curRot() (yaw, headYaw float32)
	netRot() (yaw, headYaw float32, ok bool)
	setNetRot(yaw, headYaw float32)
}

func angleByte(deg float32) byte {
	return byte(int32(deg*256.0/360.0) & 0xFF)
}

// mobEntity is one animal or hostile monster.
type mobEntity struct {
	mu                  sync.Mutex
	def                 *mobDef
	id                  int32
	uuid                [16]byte
	x, y, z, vx, vy, vz float64
	yaw, pitch, headYaw float32
	ground              bool
	health              float32

	// AI state.
	moveTimer  int32 // ticks until the next wander decision
	walking    bool
	panicTicks int32 // flee straight after being hurt

	// M9 hostile state (zombie/skeleton/creeper chase brains).
	targetID           int32   // snapshot of the chased player's entity id
	tx, ty, tz         float64 // target position snapshot (1 tick stale)
	dayLight           bool    // sunlight snapshot from the pre-pass
	attackCD           int32   // melee swing cooldown
	shootCD            int32   // arrow cooldown
	swell              int32   // creeper fuse progress 0..30
	swellDir           int32   // creeper metadata: -1 decay, +1 priming
	wasPrimed          bool    // fuse sound played for this charge
	fireTicks          int32   // sunburn timer, visual + damage
	fireDmgCounter     int32   // ticks since the last burn damage
	pendingMelee       bool    // intents consumed by the ticker
	pendingShoot       bool
	pendingExplode     bool
	pendingFireDmg     bool
	pendingPrimedSound bool

	deathTicks int32 // >0 while the fall-over animation plays

	blocked bool // horizontal collision flag for the hop logic

	fallDistance   float32
	pendingFallDmg float32 // consumed by tickEntities after tick()

	metaDirty   bool
	nx, ny, nz  float64
	hasNet      bool
	nyaw, nhead float32
	hasRot      bool
	removed     bool
}

// newMobEntity seeds a mob at (x,y,z) facing a random direction.
func newMobEntity(def *mobDef, id int32, x, y, z float64) *mobEntity {
	var u [16]byte
	_, _ = rand.Read(u[:])
	u[6] = (u[6] & 0x0F) | 0x40
	u[8] = (u[8] & 0x3F) | 0x80
	return &mobEntity{
		def:       def,
		id:        id,
		uuid:      u,
		x:         x,
		y:         y,
		z:         z,
		yaw:       rand.Float32() * 360,
		health:    def.maxHealth,
		moveTimer: int32(rand.Intn(60)),
		swellDir:  -1, // creeper idle metadata
	}
}

func (m *mobEntity) entityID() int32 { return m.id }
func (m *mobEntity) typeID() int32   { return m.def.typeID }
func (m *mobEntity) alive() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return !m.removed
}
func (m *mobEntity) metadataDirty() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.metaDirty
}
func (m *mobEntity) clearMetadataDirty() {
	m.mu.Lock()
	m.metaDirty = false
	m.mu.Unlock()
}
func (m *mobEntity) xPos() (float64, float64, float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.x, m.y, m.z
}
func (m *mobEntity) onGroundFlag() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ground
}
func (m *mobEntity) netPos() (float64, float64, float64, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nx, m.ny, m.nz, m.hasNet
}
func (m *mobEntity) setNetPos(x, y, z float64) {
	m.mu.Lock()
	m.nx, m.ny, m.nz, m.hasNet = x, y, z, true
	m.mu.Unlock()
}
func (m *mobEntity) netRot() (float32, float32, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.nyaw, m.nhead, m.hasRot
}
func (m *mobEntity) setNetRot(yaw, head float32) {
	m.mu.Lock()
	m.nyaw, m.nhead, m.hasRot = yaw, head, true
	m.mu.Unlock()
}
func (m *mobEntity) curRot() (float32, float32) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.yaw, m.headYaw
}

// boxCollides reports whether the entity box (width w, height h, bottom
// center at x/z, bottom at y) intersects any solid block.
func (s *Server) boxCollides(x, y, z, w, h float64) bool {
	r := w / 2
	for bx := int(math.Floor(x - r)); bx <= int(math.Floor(x+r)); bx++ {
		for by := int(math.Floor(y)); by <= int(math.Floor(y+h)); by++ {
			for bz := int(math.Floor(z - r)); bz <= int(math.Floor(z+r)); bz++ {
				if s.world.getBlock(bx, by, bz) != stateAir {
					return true
				}
			}
		}
	}
	return false
}

// tick advances the mob: wander AI, gravity, axis-separated collision and
// the death animation countdown.
func (m *mobEntity) tick(s *Server) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.deathTicks > 0 {
		m.deathTicks--
		if m.deathTicks == 0 {
			m.removed = true
		}
		return
	}

	h := m.def.height

	// Brain: hostiles chase players via the pre-pass snapshot; passive
	// animals alternate between strolling and standing.
	if m.def.hostile {
		m.hostileBrain()
	} else {
		m.wanderBrain()
	}

	// Speed scales up while fleeing.
	speed := m.def.speed
	if m.panicTicks > 0 {
		speed *= 1.8
	}
	if m.walking {
		rad := float64(m.yaw) * math.Pi / 180
		m.vx = -math.Sin(rad) * speed
		m.vz = math.Cos(rad) * speed
	} else {
		m.vx *= mobGroundFriction
		m.vz *= mobGroundFriction
		if math.Abs(m.vx) < 0.003 {
			m.vx = 0
		}
		if math.Abs(m.vz) < 0.003 {
			m.vz = 0
		}
	}

	// Gravity + vertical integration.
	m.vy -= mobGravity
	m.vy *= mobAirDrag
	if m.vy < mobTerminalVy {
		m.vy = mobTerminalVy
	}
	ny := m.y + m.vy
	if m.vy < 0 && s.boxCollides(m.x, ny, m.z, m.def.width, h) {
		ny = math.Floor(ny) + 1
		m.vy = 0
		m.ground = true
	} else if m.vy > 0 && s.boxCollides(m.x, ny, m.z, m.def.width, h) {
		ny = m.y
		m.vy = 0
	}
	m.y = ny
	if m.vy != 0 {
		m.ground = false
	}
	if !m.ground && s.boxCollides(m.x, m.y-0.01, m.z, m.def.width, h) {
		m.ground = true
		if m.vy < 0 {
			m.vy = 0
		}
	}

	// Horizontal integration + one-block hop when bumped.
	nx := m.x + m.vx
	if m.vx != 0 && s.boxCollides(nx, m.y, m.z, m.def.width, h) {
		nx = m.x
		if m.ground && s.boxCollides(nx, m.y-1, m.z, m.def.width, h) == false {
			// solid ground below: hop the obstacle
		}
		m.vx = 0
		m.blocked = true
	}
	m.x = nx
	nz := m.z + m.vz
	if m.vz != 0 && s.boxCollides(m.x, m.y, nz, m.def.width, h) {
		nz = m.z
		m.vz = 0
		m.blocked = true
	}
	m.z = nz
	if m.blocked && m.ground && (m.walking || m.panicTicks > 0) {
		// only hop when there is headroom
		if !s.boxCollides(m.x, m.y+1.05, m.z, m.def.width, h) {
			m.vy = mobJumpVelocity
			m.ground = false
		}
		m.blocked = false
	} else {
		m.blocked = false
	}

	// Fall damage bookkeeping (vanilla: safe fall <= 3 blocks).
	if m.ground && m.fallDistance > 0 {
		if m.fallDistance > 3.0 {
			m.pendingFallDmg = float32(math.Floor(float64(m.fallDistance - 3.0)))
		}
		m.fallDistance = 0
	} else if m.vy < 0 {
		m.fallDistance -= float32(m.vy)
	}

	// Head follows the body with a little life-like lag.
	m.headYaw = m.yaw
	m.pitch = 0
}

// wanderBrain is the passive idle brain: alternate between strolling and
// standing. Caller holds m.mu.
func (m *mobEntity) wanderBrain() {
	m.moveTimer--
	if m.moveTimer <= 0 {
		if m.panicTicks <= 0 && rand.Intn(100) < 55 {
			m.walking = false
			m.moveTimer = int32(30 + rand.Intn(80))
		} else {
			m.walking = true
			m.yaw = rand.Float32() * 360
			m.moveTimer = int32(20 + rand.Intn(50))
		}
	}
	if m.panicTicks > 0 {
		m.panicTicks--
		m.walking = true
	}
}

// hurt applies player melee damage: knockback, hurt feedback and death.
// Caller holds Server.mu.
func (s *Server) hurtMobLocked(m *mobEntity, attacker *player, dmg float32) {
	if m.deathTicks > 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.health -= dmg

	// Knockback away from the attacker.
	dx := m.x - attacker.x
	dz := m.z - attacker.z
	d := math.Sqrt(dx*dx + dz*dz)
	if d > 0.001 {
		m.vx = dx / d * mobKnockbackH
		m.vz = dz / d * mobKnockbackH
	}
	if m.ground {
		m.vy = mobKnockbackV
		m.ground = false
	}

	// Panic away from the attacker for ~3 seconds (animals only: hostiles
	// retaliate instead, and their nearest-player pre-pass already re-targets
	// the attacker).
	if !m.def.hostile {
		m.panicTicks = 60
		m.walking = true
	}
	if d > 0.001 {
		m.yaw = float32(math.Atan2(-dx, dz) * 180 / math.Pi)
	}

	sound, hasSound := mobSound[m.def.name]
	if hasSound {
		s.broadcastSoundLocked(sound[0], mobSource(m.def.name),
			float32(m.x), float32(m.y+float64(m.def.height)/2), float32(m.z), 1.0, randomPitch())
	}

	// Damage feedback to everyone tracking the mob (and the attacker).
	tyaw := float32(math.Atan2(-(attacker.x-m.x), attacker.z-m.z) * 180 / math.Pi)
	dmgBody := protocol.NewWriter()
	dmgBody.VarInt(v776.PacketPlayDamageEvent)
	java.WriteDamageEvent(dmgBody, m.id, v776.DamageTypePlayerAtk, attacker.id, attacker.id)
	hurtBody := protocol.NewWriter()
	hurtBody.VarInt(v776.PacketPlayHurtAnimation)
	java.WriteHurtAnimation(hurtBody, m.id, tyaw)
	for _, p := range s.players {
		if p.seenEnt[m.id] || p.id == attacker.id {
			_ = p.conn.sendPacket(dmgBody.Bytes())
			_ = p.conn.sendPacket(hurtBody.Bytes())
		}
	}

	if m.health <= 0 {
		m.deathTicks = mobDeathAnimTicks
		m.walking = false
		m.vx, m.vy, m.vz = 0, 0, 0
		m.fireTicks = 0
		m.metaDirty = true
		eventBody := protocol.NewWriter()
		eventBody.VarInt(v776.PacketPlayEntityEvent)
		java.WriteEntityEvent(eventBody, m.id, 3)
		for _, p := range s.players {
			if p.seenEnt[m.id] || p.id == attacker.id {
				_ = p.conn.sendPacket(eventBody.Bytes())
			}
		}
		if hasSound {
			s.broadcastSoundLocked(sound[1], mobSource(m.def.name),
				float32(m.x), float32(m.y+float64(m.def.height)/2), float32(m.z), 1.0, randomPitch())
		}
		s.dropMobLootLocked(m)
	}
}

// damageMobLocked applies any server-side damage (fire, explosions).
// Caller holds Server.mu; the mob mutex is taken and released inside.
func (s *Server) damageMobLocked(m *mobEntity, dmg float32, dmgType int32, cause, direct int32) {
	m.mu.Lock()
	if m.deathTicks > 0 || m.removed || dmg <= 0 {
		m.mu.Unlock()
		return
	}
	m.health -= dmg
	dying := false
	if m.health <= 0 {
		dying = true
		m.deathTicks = mobDeathAnimTicks
		m.walking = false
		m.vx, m.vy, m.vz = 0, 0, 0
	}
	if dmgType == v776.DamageTypeOnFire || dying {
		m.fireTicks = 0
	}
	m.metaDirty = true
	mx, my, mz := m.x, m.y, m.z
	seen := make([]*player, 0, len(s.players))
	for _, p := range s.players {
		if p.seenEnt[m.id] {
			seen = append(seen, p)
		}
	}
	m.mu.Unlock()

	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayDamageEvent)
	java.WriteDamageEvent(body, m.id, dmgType, cause, direct)
	hurtBody := protocol.NewWriter()
	hurtBody.VarInt(v776.PacketPlayHurtAnimation)
	java.WriteHurtAnimation(hurtBody, m.id, 0)
	for _, p := range seen {
		_ = p.conn.sendPacket(body.Bytes())
		_ = p.conn.sendPacket(hurtBody.Bytes())
	}
	if dying {
		eventBody := protocol.NewWriter()
		eventBody.VarInt(v776.PacketPlayEntityEvent)
		java.WriteEntityEvent(eventBody, m.id, 3)
		for _, p := range seen {
			_ = p.conn.sendPacket(eventBody.Bytes())
		}
		if sound, ok := mobSound[m.def.name]; ok {
			s.broadcastSoundLocked(sound[1], mobSource(m.def.name),
				float32(mx), float32(my+float64(m.def.height)/2), float32(mz), 1.0, randomPitch())
		}
		s.dropMobLootLocked(m)
	}
}

// dropMobLootLocked scatters the mob's drops at its position. Caller
// holds Server.mu (spawnEntityLocked expects it).
func (s *Server) dropMobLootLocked(m *mobEntity) {
	// XP: adults drop 1-3 orbs' worth (chicken 1) in vanilla.
	xp := int32(1)
	if m.def.name != "chicken" {
		xp = 1 + rand.Int31n(3)
	}
	orb := newXPOrbEntity(s.allocEntityID(), m.x, m.y+0.3, m.z, xp)
	s.spawnEntityLocked(orb)
	for _, d := range m.def.drops {
		if d.max == 0 {
			continue
		}
		count := d.min
		if d.max > d.min {
			count += rand.Int31n(d.max - d.min + 1)
		}
		if count <= 0 {
			continue
		}
		id, ok := itemIDByName[d.item]
		if !ok {
			continue
		}
		e := newItemEntity(s.allocEntityID(), m.x, m.y+0.3, m.z, id, count)
		s.spawnEntityLocked(e)
	}
}

// spawnEntityLocked registers an entity under an already-held lock.
func (s *Server) spawnEntityLocked(e entity) {
	s.entities[e.entityID()] = e
}

// spawnStarterMobs seeds a small herd per species around the world spawn.
func (s *Server) spawnStarterMobs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	spawned := 0
	for i := range mobDefs {
		def := &mobDefs[i]
		for n := 0; n < 6; n++ {
			// Scatter in a ring 10..30 blocks out, skipping blocked spots.
			angle := rand.Float64() * 2 * math.Pi
			radius := 10 + rand.Float64()*20
			x := math.Round(math.Cos(angle)*radius) + 0.5
			z := math.Round(math.Sin(angle)*radius) + 0.5
			y := float64(s.surfaceY(int(x), int(z)))
			if s.boxCollides(x, y, z, def.width, def.height) {
				continue
			}
			m := newMobEntity(def, s.allocEntityID(), x, y, z)
			s.entities[m.id] = m
			spawned++
		}
	}
	log.Printf(ui.Success("OK ")+"已在出生点周围生成 %d 只被动生物（猪/牛/羊/鸡）", spawned)
}

// surfaceY returns the feet level above the topmost solid block at (x,z).
func (s *Server) surfaceY(x, z int) int32 {
	for y := int32(80); y >= -64; y-- {
		if s.world.getBlock(x, int(y), z) != stateAir {
			return y + 1
		}
	}
	return -60
}

// writeMobMetadata encodes the set_entity_data frame for a mob: the
// shared flags byte (on fire) and, for creepers, the swell direction the
// client animates. Indices follow the 26.2 defineId declaration order
// (see the constants in v776).
func writeMobMetadata(w *protocol.Writer, m *mobEntity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w.VarInt(v776.PacketPlaySetEntityData)
	w.VarInt(m.id)
	flags := byte(0)
	if m.fireTicks > 0 {
		flags |= v776.EntityFlagOnFire
	}
	w.Byte(v776.MetaIndexEntityFlags)
	w.VarInt(v776.MetaSerializerByte)
	w.Byte(flags)
	if m.def.hostile && m.def.name == "creeper" {
		w.Byte(v776.MetaIndexCreeperSwell)
		w.VarInt(v776.MetaSerializerVarInt)
		w.VarInt(m.swellDir)
	}
	w.Byte(0xFF) // end of metadata list
}

// encodeMobSpawn writes the add_entity payload for a mob.
func encodeMobSpawn(w *protocol.Writer, m *mobEntity) {
	m.mu.Lock()
	defer m.mu.Unlock()
	java.WriteAddEntity(w, m.id, m.uuid, m.def.typeID, m.x, m.y, m.z,
		m.vx, m.vy, m.vz,
		angleByte(m.pitch), angleByte(m.yaw), angleByte(m.headYaw), 0)
}

// handleAttack processes the 26.2 dedicated attack packet: reach check,
// weapon damage, cooldown, then the mob's hurt feedback.
func (c *conn) handleAttack(targetID int32) error {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := c.player
	if p == nil || p.dead {
		return nil
	}
	if s.tickCount-p.lastAttackTick < mobAttackCooldown {
		return nil
	}
	m, ok := s.entities[targetID].(*mobEntity)
	if !ok {
		return nil // attacking players/items/orbs is not a thing here
	}

	// Vanilla-style reach: eye position to the nearest point of the mob's
	// bounding box, capped at 3 blocks.
	m.mu.Lock()
	eyeY := p.y + 1.62
	cx := math.Max(m.x-m.def.width/2, math.Min(p.x, m.x+m.def.width/2))
	cy := math.Max(m.y, math.Min(eyeY, m.y+m.def.height))
	cz := math.Max(m.z-m.def.width/2, math.Min(p.z, m.z+m.def.width/2))
	dx, dy, dz := p.x-cx, eyeY-cy, p.z-cz
	withinReach := dx*dx+dy*dy+dz*dz <= 9.0
	health := m.health
	m.mu.Unlock()
	if !withinReach || health <= 0 {
		return nil
	}

	p.lastAttackTick = s.tickCount
	s.broadcastSwing(p)
	dmg := float32(1)
	if held := p.slots[p.heldSlot]; held.count > 0 {
		if w, ok := weaponDamage[held.item]; ok {
			dmg = w
		}
	}
	// Melee costs exhaustion like vanilla (0.1 per swing).
	p.exhaustion += 0.1
	s.hurtMobLocked(m, p, dmg)
	return nil
}

// mobFallDamage applies a pending landing hit; runs after tick() on the
// ticker goroutine with the lock order Server.mu -> mob.mu.
func (s *Server) mobFallDamage(m *mobEntity, dmg float32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.deathTicks > 0 || dmg <= 0 {
		return
	}
	m.health -= dmg
	tyaw := float32(0)
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayDamageEvent)
	java.WriteDamageEvent(body, m.id, v776.DamageTypeFall, -1, -1)
	hurtBody := protocol.NewWriter()
	hurtBody.VarInt(v776.PacketPlayHurtAnimation)
	java.WriteHurtAnimation(hurtBody, m.id, tyaw)
	for _, p := range s.players {
		if p.seenEnt[m.id] {
			_ = p.conn.sendPacket(body.Bytes())
			_ = p.conn.sendPacket(hurtBody.Bytes())
		}
	}
	if m.health <= 0 {
		m.deathTicks = mobDeathAnimTicks
		m.walking = false
		m.vx, m.vy, m.vz = 0, 0, 0
		eventBody := protocol.NewWriter()
		eventBody.VarInt(v776.PacketPlayEntityEvent)
		java.WriteEntityEvent(eventBody, m.id, 3)
		for _, p := range s.players {
			if p.seenEnt[m.id] {
				_ = p.conn.sendPacket(eventBody.Bytes())
			}
		}
		s.dropMobLootLocked(m)
	}
}

// topUpMobs refills the passive herd toward its 24-head cap, spawning a
// few animals around a random online player every pass.
func (s *Server) topUpMobs() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.players) == 0 {
		return
	}
	const mobCap = 24
	count := 0
	for _, e := range s.entities {
		if _, ok := e.(*mobEntity); ok {
			count++
		}
	}
	if count >= mobCap {
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
	budget := mobCap - count
	if budget > 4 {
		budget = 4
	}
	for n := 0; n < budget; n++ {
		def := &mobDefs[rand.Intn(len(mobDefs))]
		angle := rand.Float64() * 2 * math.Pi
		radius := 16 + rand.Float64()*24
		x := math.Round(anchor.x+math.Cos(angle)*radius) + 0.5
		z := math.Round(anchor.z+math.Sin(angle)*radius) + 0.5
		y := float64(s.surfaceY(int(x), int(z)))
		if s.boxCollides(x, y, z, def.width, def.height) {
			continue
		}
		m := newMobEntity(def, s.allocEntityID(), x, y, z)
		s.entities[m.id] = m
	}
}
