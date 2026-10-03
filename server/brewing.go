package server

// M13 酿造台与抛射药水。数值与流程对照 26.2 反编译源：
//   - BrewingStandBlockEntity.serverTick：燃料自动装填（烈焰粉 20 次）、
//     brewTime 400t 递减、酿造完成时三瓶全部混合、原料减一、
//     levelEvent 1035（block.brewing_stand.brew 音效）。
//   - BrewingStandMenu：槽位 0-2 药水瓶（可放玻璃瓶，上限 1）、3 原料、
//     4 燃料；玩家背包 5..40；ContainerData 0=brewTime 1=fuel。
//   - ThrownPotion：右键即掷，shootFromRotation(-20° 俯仰补偿, 1.5 速度,
//     1.0 散布)；落地时对 4.0x2.0x4.0 范围实体施加效果，距离越近时长越
//     长（vanilla applySplash：1-dist/4 的线性折扣，下限 0.5），直接命中
//     者全额。滞留药水沿用喷溅逻辑、时长打 0.25 折（药水云实体暂缺，
//     见 README）。

import (
	"math"
	"math/rand"
	"sync"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

const (
	brewingTotalTicks  = 400 // BREWING_TIME_SECONDS(20) * 20 tps
	brewingFuelPerItem = 20  // one blaze powder funds 20 operations
	potionThrowSpeed   = 1.5 // vanilla ThrownPotion shoot speed
	potionThrowPitch   = -20 // degrees added to the throw pitch
	potionGravity      = 0.03
	potionInertia      = 0.99
	potionLifespan     = 300 // ticks before a stray potion despawns
)

// isDrinkablePotionSlot reports whether the stack is a drinkable potion
// (minecraft:potion only; splash/lingering are thrown instead).
func isDrinkablePotionSlot(s invSlot) bool {
	pot, ok := itemIDByName["minecraft:potion"]
	return ok && s.count > 0 && s.potion > 0 && s.item == pot
}

// isSplashPotionSlot reports splash/lingering potions (throwable).
func isSplashPotionSlot(s invSlot) bool {
	splash, ok1 := itemIDByName["minecraft:splash_potion"]
	lingering, ok2 := itemIDByName["minecraft:lingering_potion"]
	return s.count > 0 && s.potion > 0 && ((ok1 && s.item == splash) || (ok2 && s.item == lingering))
}

// throwPotion consumes one splash/lingering potion and spawns the thrown
// entity. Called from the connection goroutine (lock-free entry).
func (c *conn) throwPotion(held invSlot, hand int32) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := c.player
	if p == nil || p.dead || held.count <= 0 {
		return
	}
	// Consume the thrown bottle. M15 修正：消耗只在生存生效（vanilla
	// ItemStack.consume 对 creative 是 no-op），创造玩家同样可以掷药水。
	if p.gameMode == 0 {
		slot := p.slots[p.heldSlot]
		slot.count--
		if slot.count <= 0 {
			slot = invSlot{}
		}
		p.slots[p.heldSlot] = slot
		p.conn.sendSlot(p.heldSlot, slot)
	}

	lingering := held.item == itemIDByName["minecraft:lingering_potion"]
	e := newThrownPotion(s, p, held.potion-1, lingering)
	s.spawnEntityLocked(e)
	s.broadcastSwing(p)
}

// potionEntity is one thrown splash/lingering potion.
type potionEntity struct {
	mu          sync.Mutex
	id          int32
	uuid        [16]byte
	x, y, z     float64
	vx, vy, vz  float64
	yaw, pitch  float32
	potionID    int32 // potion registry id
	lingering   bool
	ownerID     int32
	age         int32
	dead        bool
	impacted    bool // hit something; consumed by consumePotionImpacts
	nx, ny, nz  float64
	hasNet      bool
	nyaw, nhead float32
	hasRot      bool
}

func newThrownPotion(s *Server, p *player, potionID int32, lingering bool) *potionEntity {
	// vanilla ThrownPotion: shootFromRotation(-20° pitch offset, speed
	// 1.5, spread 1.0); spread jitters each axis by 0.0075 * speed.
	yawRad := float64(p.yaw) * math.Pi / 180
	pitchRad := (float64(p.pitch) + potionThrowPitch) * math.Pi / 180
	ex, ey, ez := p.x, p.y+1.62, p.z
	vx := -math.Sin(yawRad) * math.Cos(pitchRad) * potionThrowSpeed
	vy := -math.Sin(pitchRad) * potionThrowSpeed
	vz := math.Cos(yawRad) * math.Cos(pitchRad) * potionThrowSpeed
	e := &potionEntity{
		id:        s.allocEntityID(),
		potionID:  potionID,
		lingering: lingering,
		ownerID:   p.id,
		x:         ex, y: ey, z: ez,
		vx:  vx + rand.NormFloat64()*0.0075*potionThrowSpeed,
		vy:  vy + rand.NormFloat64()*0.0075*potionThrowSpeed,
		vz:  vz + rand.NormFloat64()*0.0075*potionThrowSpeed,
		yaw: p.yaw, pitch: p.pitch,
	}
	_, _ = rand.Read(e.uuid[:])
	e.uuid[6] = (e.uuid[6] & 0x0F) | 0x40
	e.uuid[8] = (e.uuid[8] & 0x3F) | 0x80
	return e
}

func (e *potionEntity) entityID() int32 { return e.id }
func (e *potionEntity) typeID() int32 {
	if e.lingering {
		return v776.EntityTypeLingeringPotion
	}
	return v776.EntityTypeSplashPotion
}
func (e *potionEntity) xPos() (float64, float64, float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.x, e.y, e.z
}
func (e *potionEntity) onGroundFlag() bool { return false }
func (e *potionEntity) alive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.dead
}
func (e *potionEntity) metadataDirty() bool { return false }
func (e *potionEntity) clearMetadataDirty() {}
func (e *potionEntity) netPos() (float64, float64, float64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nx, e.ny, e.nz, e.hasNet
}
func (e *potionEntity) setNetPos(x, y, z float64) {
	e.mu.Lock()
	e.nx, e.ny, e.nz, e.hasNet = x, y, z, true
	e.mu.Unlock()
}
func (e *potionEntity) curRot() (float32, float32) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.yaw, e.pitch
}
func (e *potionEntity) netRot() (float32, float32, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nyaw, e.nhead, e.hasRot
}
func (e *potionEntity) setNetRot(yaw, head float32) {
	e.mu.Lock()
	e.nyaw, e.nhead, e.hasRot = yaw, head, true
	e.mu.Unlock()
}

// tick advances the thrown potion physics. Impact handling is deferred to
// consumePotionImpacts (arrow.go pattern: packets never run under e.mu).
func (e *potionEntity) tick(s *Server) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.age++
	if e.age >= potionLifespan {
		e.dead = true
		return
	}
	e.vy -= potionGravity
	e.vx *= potionInertia
	e.vy *= potionInertia
	e.vz *= potionInertia
	e.x += e.vx
	e.y += e.vy
	e.z += e.vz
	if s.entityBoxCollides(e.x, e.y, e.z) {
		// 退回本步位移并标记命中；效果结算由消费趟完成（附近实体按
		// 距离折扣施加效果，命中点本身即覆盖直接命中者）。
		e.x -= e.vx
		e.y -= e.vy
		e.z -= e.vz
		e.vx, e.vy, e.vz = 0, 0, 0
		e.impacted = true
	}
}

// consumePotionImpacts resolves impacted potions: splash potions apply
// their effect cloud to nearby entities; lingering potions spawn the real
// AreaEffectCloud entity. Runs on the ticker with Server.mu held by the
// caller (mirrors consumeArrowHits lock order).
func (s *Server) consumePotionImpacts() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ent := range s.entities {
		pot, ok := ent.(*potionEntity)
		if !ok {
			continue
		}
		pot.mu.Lock()
		hit := pot.impacted && !pot.dead
		pot.impacted = false
		var px, py, pz float64
		var potionID int32
		var lingering bool
		var ownerID int32
		if hit {
			px, py, pz = pot.x, pot.y, pot.z
			potionID = pot.potionID
			lingering = pot.lingering
			ownerID = pot.ownerID
			pot.dead = true
		}
		pot.mu.Unlock()
		if hit {
			if lingering {
				// M14：滞留药水落点生成真实的效果云实体
				// （ThrownLingeringPotion.onHitAsPotion 移植）。
				cloud := newCloudEntity(s.allocEntityID(), px, py, pz, potionID)
				s.entities[cloud.id] = cloud
			} else {
				s.applySplashAt(px, py, pz, potionID, ownerID)
			}
		}
	}
}

// applySplashAt grants the potion's effects to every entity near the
// impact point (vanilla PotionUtils.applySplash radius 4.0 x 2.0 x 4.0,
// duration scaled by 1 - dist/4 clamped to [0.25, 1]; direct hits are not
// part of this simplified model — the thrower owns the closest box).
// M14：滞留药水不再走此路径（改生成 AreaEffectCloud 实体）。
func (s *Server) applySplashAt(x, y, z float64, potionID int32, ownerID int32) {
	if potionID < 0 || int(potionID) >= len(potionDefs) {
		return
	}
	for _, p := range s.players {
		if p.dead || p.gameMode != 0 {
			continue
		}
		dx, dy, dz := p.x-x, p.y-y, p.z-z
		hd := math.Sqrt(dx*dx+dz*dz) / 4.0
		vd := math.Abs(dy) / 2.0
		if hd >= 1 || vd >= 1 {
			continue
		}
		f := float32((1 - hd) * (1 - vd))
		if f < 0.25 {
			f = 0.25
		}
		s.applyPotionEffectsToPlayer(p, potionID, f)
	}
	for _, e := range s.entities {
		m, ok := e.(*mobEntity)
		if !ok {
			continue
		}
		m.mu.Lock()
		dying := m.deathTicks > 0 || m.removed
		mx, my, mz := m.x, m.y, m.z
		m.mu.Unlock()
		if dying {
			continue
		}
		dx, dy, dz := mx-x, my-y, mz-z
		hd := math.Sqrt(dx*dx+dz*dz) / 4.0
		vd := math.Abs(dy) / 2.0
		if hd >= 1 || vd >= 1 {
			continue
		}
		f := float32((1 - hd) * (1 - vd))
		if f < 0.25 {
			f = 0.25
		}
		m.mu.Lock()
		s.applyPotionEffectsToMob(m, potionID, f)
		m.mu.Unlock()
	}
	_ = ownerID
	// vanilla AbstractThrownPotion.onHit 末尾的 levelEvent(2002/2007)
	// 由客户端播放同款音效；这里直接等效播放。
	s.broadcastSoundLocked("minecraft:entity.potion.splash", v776.SoundSourcePlayers,
		float32(x), float32(y), float32(z), 1.0, randomPitch())
}

// encodePotionSpawn writes the add_entity payload for a thrown potion:
// the launch velocity lets the client free-simulate the parabola between
// server move deltas (M14: previously potions spawned at the origin due
// to the missing encoder branch).
func encodePotionSpawn(w *protocol.Writer, e *potionEntity) {
	e.mu.Lock()
	defer e.mu.Unlock()
	java.WriteAddEntity(w, e.id, e.uuid, e.typeID(), e.x, e.y, e.z,
		e.vx, e.vy, e.vz,
		angleByte(e.pitch), angleByte(e.yaw), angleByte(e.yaw), 0)
}

// ---- brewing stand block entity -------------------------------------------

// brewingHasBottleBits re-renders the brewing_stand blockstate property
// set after the bottle counts change (vanilla BrewingStandBlock update).
func (s *Server) updateBrewingBlockStateLocked(b *blockEntity, prev [3]bool) {
	var now [3]bool
	for i := 0; i < 3; i++ {
		now[i] = b.slots[i].count > 0
	}
	if now == prev {
		return
	}
	props := blockPropsOf(int(s.world.getBlock(b.x, b.y, b.z)))
	if props == nil {
		props = map[string]string{}
	}
	props["has_bottle_0"] = boolStr(now[0])
	props["has_bottle_1"] = boolStr(now[1])
	props["has_bottle_2"] = boolStr(now[2])
	if sid := stateIDOf("minecraft:brewing_stand", props); sid > 0 {
		if s.world.setBlock(b.x, b.y, b.z, sid) {
			s.broadcastBlockUpdateLocked(int32(b.x), int32(b.y), int32(b.z), sid)
		}
	}
}

// bottleBits snapshots the three has_bottle properties of the current
// blockstate (nil when the state is unknown).
func bottleBits(state int32) [3]bool {
	var bits [3]bool
	props := blockPropsOf(int(state))
	if props == nil {
		return bits
	}
	for i, key := range []string{"has_bottle_0", "has_bottle_1", "has_bottle_2"} {
		bits[i] = props[key] == "true"
	}
	return bits
}

// tickBrewingLocked is the vanilla BrewingStandBlockEntity.serverTick
// port: fuel auto-load, 400t brew countdown, triple mix on completion and
// the has_bottle_* blockstate refresh. Caller holds Server.mu.
func (s *Server) tickBrewingLocked(b *blockEntity) {
	prevBits := bottleBits(s.world.getBlock(b.x, b.y, b.z))
	changed := false

	// Fuel auto-load: one blaze powder funds 20 operations.
	fuel := b.slots[4]
	if b.fuel <= 0 && fuel.count > 0 && bareItemName(fuel.item) == "blaze_powder" {
		b.fuel = brewingFuelPerItem
		fuel.count--
		if fuel.count <= 0 {
			b.slots[4] = invSlot{}
		} else {
			b.slots[4] = fuel
		}
		changed = true
	}

	brewable := brewingBrewable(b)
	if b.brewTime > 0 {
		b.brewTime--
		ingredient := b.slots[3]
		if b.brewTime == 0 && brewable {
			s.brewOneRound(b)
		} else if !brewable || ingredient.count <= 0 || ingredient.item != b.lastIngredient {
			b.brewTime = 0 // 原料被换走或不再可酿：进度作废
		}
		changed = true
	} else if brewable && b.fuel > 0 {
		b.fuel--
		b.brewTime = brewingTotalTicks
		b.lastIngredient = b.slots[3].item
		changed = true
	}

	if changed {
		s.world.markDirty(int32(b.x>>4), int32(b.z>>4))
	}
	s.updateBrewingBlockStateLocked(b, prevBits)
	b.pushBrewingData(s)
}

// brewingBrewable mirrors vanilla isBrewable: an ingredient that can mix
// with at least one non-empty bottle slot.
func brewingBrewable(b *blockEntity) bool {
	ingredient := b.slots[3]
	if ingredient.count <= 0 || !brewingIsIngredient(ingredient.item) {
		return false
	}
	for i := 0; i < 3; i++ {
		st := b.slots[i]
		if st.count <= 0 {
			continue
		}
		if st.potion <= 0 {
			continue
		}
		if brewingHasMix(st.potion-1, ingredient.item) {
			return true
		}
		// 容器混合：水瓶/药水 + 火药 → 喷溅、喷溅 + 龙息 → 滞留。
		if brewingContainerMixTarget(st, ingredient.item) > 0 {
			return true
		}
	}
	return false
}

// brewingContainerMixTarget resolves the gunpowder/dragon's breath
// container recipes (vanilla containerMixes) for one bottle slot.
func brewingContainerMixTarget(st invSlot, ingredient int32) int32 {
	potion, ok := itemIDByName["minecraft:potion"]
	splash, ok1 := itemIDByName["minecraft:splash_potion"]
	lingering, ok2 := itemIDByName["minecraft:lingering_potion"]
	gunpowder, ok3 := itemIDByName["minecraft:gunpowder"]
	breath, ok4 := itemIDByName["minecraft:dragon_breath"]
	if !ok || !ok1 || !ok2 || !ok3 || !ok4 {
		return 0
	}
	switch {
	case st.item == potion && ingredient == gunpowder:
		return splash
	case st.item == splash && ingredient == gunpowder:
		return splash // 原版允许继续转化：喷溅+火药仍是喷溅（混合无效果）
	case st.item == splash && ingredient == breath:
		return lingering
	}
	return 0
}

// brewOneRound converts every mixable bottle, consumes the ingredient and
// plays the brew sound (vanilla doBrew + levelEvent 1035).
func (s *Server) brewOneRound(b *blockEntity) {
	ingredient := b.slots[3]
	if ingredient.count <= 0 {
		return
	}
	for i := 0; i < 3; i++ {
		st := b.slots[i]
		if st.count <= 0 {
			continue
		}
		// 普通药水配方：水瓶/药水 + 原料 → 新药水。
		if st.potion > 0 {
			if to, ok := brewingMix(st.potion-1, ingredient.item); ok {
				st.potion = to + 1
				b.slots[i] = st
				continue
			}
		}
		// 容器配方：水瓶 + 火药 → 喷溅；喷溅 + 龙息 → 滞留。
		if item := brewingContainerMixTarget(st, ingredient.item); item > 0 {
			st.item = item
			b.slots[i] = st
			continue
		}
		// 水瓶 + 原料但无药水成分：跳过（vanilla mix 需要 potion 成分）。
	}
	ingredient.count--
	if ingredient.count <= 0 {
		b.slots[3] = invSlot{}
	} else {
		b.slots[3] = ingredient
	}
	b.brewTime = 0
	s.broadcastSoundLocked("minecraft:block.brewing_stand.brew", v776.SoundSourceBlocks,
		float32(b.x)+0.5, float32(b.y)+0.5, float32(b.z)+0.5, 1.0, randomPitch())
}

// pushBrewingData streams the two brewing data slots (0 brewTime,
// 1 fuel — BrewingStandMenu BrewingStandData order) to open menus.
func (b *blockEntity) pushBrewingData(s *Server) {
	vals := [2]int{int(b.brewTime), int(b.fuel)}
	for _, p := range s.players {
		m := p.openMenu
		if m == nil || m.be != b {
			continue
		}
		for i, v := range vals {
			if v == b.lastData[i] {
				continue
			}
			b.lastData[i] = v
			body := protocol.NewWriter()
			body.VarInt(v776.PacketPlayContainerSetData)
			java.WriteContainerSetData(body, m.id, int16(i), int16(v))
			_ = p.conn.sendPacket(body.Bytes())
		}
	}
}
