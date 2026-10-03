package server

import (
	"fmt"
	"math"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M8 survival: health, food, eating, fall/void damage, death and respawn.
// Values follow vanilla normal difficulty; the local player is the only
// damageable living entity besides mobs.

// Vanilla survival constants.
const (
	maxHealth            = 20.0
	maxFood              = 20
	startingSaturation   = 5.0
	exhaustionPerFood    = 4.0 // every 4 exhaustion burns one saturation/food point
	regenFoodThreshold   = 18  // regen requires this much food
	regenIntervalTicks   = 80  // 4 seconds between regeneration ticks
	regenExhaustion      = 6.0 // vanilla adds exhaustion when regenerating
	starveIntervalTicks  = 80
	starveFloorHealth    = 1.0 // normal difficulty stops starving at half a heart
	voidStartY           = -64.0
	voidDamage           = 4.0
	voidDamageInterval   = 10
	fallSafeDistance     = 3.0 // blocks of free fall taken without damage
	walkExhaustion       = 0.005
	sprintExhaustion     = 0.1
	eatDurationTicks     = 32 // ~1.6s for every gopherite food
	sprintJumpExhaustion = 0.2
)

// foodValue is the vanilla nutrition/saturation restoration pair.
type foodValue struct {
	nutrition  float32
	saturation float32
}

// foodByItem maps item registry ids to their food values, built from the
// name table once. Saturation is the effective restored amount
// (nutrition * modifier * 2 in vanilla terms).
var foodByItem = map[int32]foodValue{}

// weaponDamage maps weapon item ids to their vanilla attack damage; any
// other item (or a bare hand) deals 1.
var weaponDamage = map[int32]float32{}

// weaponSpeed maps weapon item ids to their vanilla attack speed
// (attacks per second); anything absent attacks at the bare-hand 4/s.
var weaponSpeed = map[int32]float32{}

func init() {
	foods := map[string]foodValue{
		"minecraft:apple":           {4, 2.4},
		"minecraft:bread":           {5, 6.0},
		"minecraft:porkchop":        {3, 1.8},
		"minecraft:cooked_porkchop": {8, 12.8},
		"minecraft:beef":            {3, 1.8},
		"minecraft:cooked_beef":     {8, 12.8},
		"minecraft:chicken":         {2, 1.2},
		"minecraft:cooked_chicken":  {6, 7.2},
		"minecraft:mutton":          {2, 1.2},
		"minecraft:cooked_mutton":   {6, 9.6},
		"minecraft:golden_apple":    {4, 9.6},
		"minecraft:rotten_flesh":    {4, 0.8},
	}
	for name, v := range foods {
		if id, ok := itemIDByName[name]; ok {
			foodByItem[id] = v
		}
	}
	// 26.2 weapon attack damage: 1 (base) + material bonus + type bonus,
	// extracted from the official jar's Items bootstrap. Axes were
	// rebalanced in 26.x — every tier converges at 9 (netherite 10).
	weapons := map[string]float32{
		// Swords (1.6 attacks/s).
		"minecraft:wooden_sword":    4,
		"minecraft:stone_sword":     5,
		"minecraft:copper_sword":    5,
		"minecraft:golden_sword":    4,
		"minecraft:iron_sword":      6,
		"minecraft:diamond_sword":   7,
		"minecraft:netherite_sword": 8,
		// Axes (0.8-1.0 attacks/s).
		"minecraft:wooden_axe":    7,
		"minecraft:stone_axe":     9,
		"minecraft:copper_axe":    9,
		"minecraft:golden_axe":    7,
		"minecraft:iron_axe":      9,
		"minecraft:diamond_axe":   9,
		"minecraft:netherite_axe": 10,
		// Pickaxes (1.2 attacks/s).
		"minecraft:wooden_pickaxe":    2,
		"minecraft:stone_pickaxe":     3,
		"minecraft:copper_pickaxe":    3,
		"minecraft:golden_pickaxe":    2,
		"minecraft:iron_pickaxe":      4,
		"minecraft:diamond_pickaxe":   5,
		"minecraft:netherite_pickaxe": 6,
		// Shovels (1 attack/s).
		"minecraft:wooden_shovel":    2.5,
		"minecraft:stone_shovel":     2.5,
		"minecraft:copper_shovel":    2.5,
		"minecraft:golden_shovel":    2.5,
		"minecraft:iron_shovel":      2.5,
		"minecraft:diamond_shovel":   2.5,
		"minecraft:netherite_shovel": 2.5,
		// Hoes (1-4 attacks/s, all deal 1).
		"minecraft:wooden_hoe":    1,
		"minecraft:stone_hoe":     1,
		"minecraft:copper_hoe":    1,
		"minecraft:golden_hoe":    1,
		"minecraft:iron_hoe":      1,
		"minecraft:diamond_hoe":   1,
		"minecraft:netherite_hoe": 1,
		// Specials: mace (0.6/s) and trident (1.1/s).
		"minecraft:mace":    6,
		"minecraft:trident": 9,
	}
	for name, dmg := range weapons {
		if id, ok := itemIDByName[name]; ok {
			weaponDamage[id] = dmg
		}
	}
	// M11 attack speeds (attacks per second) for the same items; the bare
	// hand attacks 4/s. Cooldown ticks = round(20 / speed).
	speeds := map[string]float32{
		"minecraft:wooden_sword":      1.6,
		"minecraft:stone_sword":       1.6,
		"minecraft:copper_sword":      1.6,
		"minecraft:golden_sword":      1.6,
		"minecraft:iron_sword":        1.6,
		"minecraft:diamond_sword":     1.6,
		"minecraft:netherite_sword":   1.6,
		"minecraft:wooden_axe":        0.8,
		"minecraft:stone_axe":         0.8,
		"minecraft:copper_axe":        0.8,
		"minecraft:golden_axe":        1.0,
		"minecraft:iron_axe":          0.9,
		"minecraft:diamond_axe":       1.0,
		"minecraft:netherite_axe":     1.0,
		"minecraft:wooden_pickaxe":    1.2,
		"minecraft:stone_pickaxe":     1.2,
		"minecraft:copper_pickaxe":    1.2,
		"minecraft:golden_pickaxe":    1.2,
		"minecraft:iron_pickaxe":      1.2,
		"minecraft:diamond_pickaxe":   1.2,
		"minecraft:netherite_pickaxe": 1.2,
		"minecraft:wooden_shovel":     1.0,
		"minecraft:stone_shovel":      1.0,
		"minecraft:copper_shovel":     1.0,
		"minecraft:golden_shovel":     1.0,
		"minecraft:iron_shovel":       1.0,
		"minecraft:diamond_shovel":    1.0,
		"minecraft:netherite_shovel":  1.0,
		"minecraft:wooden_hoe":        1.0,
		"minecraft:stone_hoe":         2.0,
		"minecraft:copper_hoe":        2.0,
		"minecraft:golden_hoe":        1.0,
		"minecraft:iron_hoe":          3.0,
		"minecraft:diamond_hoe":       4.0,
		"minecraft:netherite_hoe":     4.0,
		"minecraft:mace":              0.6,
		"minecraft:trident":           1.1,
	}
	for name, speed := range speeds {
		if id, ok := itemIDByName[name]; ok {
			weaponSpeed[id] = speed
		}
	}
}

// attackCooldownTicks returns the swing cooldown for the held weapon in
// ticks: round(20 / attack speed), bare hand 5 (4 attacks/s).
// Caller holds s.mu.
func (p *player) attackCooldownTicks() int64 {
	speed := float32(4.0)
	if held := p.slots[p.heldSlot]; held.count > 0 {
		if s, ok := weaponSpeed[held.item]; ok {
			speed = s
		}
	}
	ticks := int64(math.Round(20.0 / float64(speed)))
	if ticks < 1 {
		ticks = 1
	}
	return ticks
}

// --- packet helpers --------------------------------------------------------

func (c *conn) sendDamageEvent(entityID, dmgType, cause, direct int32) error {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayDamageEvent)
	java.WriteDamageEvent(w, entityID, dmgType, cause, direct)
	return c.sendPacket(w.Bytes())
}

func (c *conn) sendHurtAnimation(entityID int32, yaw float32) error {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayHurtAnimation)
	java.WriteHurtAnimation(w, entityID, yaw)
	return c.sendPacket(w.Bytes())
}

func (c *conn) sendEntityEvent(entityID int32, event byte) error {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayEntityEvent)
	java.WriteEntityEvent(w, entityID, event)
	return c.sendPacket(w.Bytes())
}

func (c *conn) sendCombatKill(playerID int32, message string) error {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayPlayerCombatKill)
	java.WritePlayerCombatKill(w, playerID, message)
	return c.sendPacket(w.Bytes())
}

func (c *conn) sendRespawn(info java.CommonSpawnInfo, dataToKeep byte) error {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayRespawn)
	java.WriteRespawn(w, info, dataToKeep)
	return c.sendPacket(w.Bytes())
}

func (c *conn) sendAttributes(entityID int32, baseHealth float64) error {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayUpdateAttributes)
	java.WriteUpdateAttributes(w, entityID, []java.AttributeSnapshot{
		{ID: v776.AttributeMaxHealth, Base: baseHealth},
	})
	return c.sendPacket(w.Bytes())
}

// sendHealth pushes the current vitals to the client; caller holds mu.
func (p *player) sendHealth() {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlaySetHealth)
	java.WriteSetHealth(w, p.health, p.saturation, p.food)
	_ = p.conn.sendPacket(w.Bytes())
}

// sendExperience pushes the XP bar state; caller holds mu.
func (p *player) sendExperience() {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlaySetExperience)
	java.WriteSetExperience(w, p.expProgress, p.expLevel, p.expTotal)
	_ = p.conn.sendPacket(w.Bytes())
}

// --- damage, death and respawn ---------------------------------------------

// damagePlayer applies damage from any goroutine.
func (s *Server) damagePlayer(p *player, amount float32, dmgType int32, cause, direct int32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.damagePlayerLocked(p, amount, dmgType, cause, direct)
}

// damagePlayerLocked applies damage; caller holds s.mu. Creative players
// are immune; dead players cannot be hurt twice.
func (s *Server) damagePlayerLocked(p *player, amount float32, dmgType int32, cause, direct int32) {
	if p == nil || p.dead || amount <= 0 {
		return
	}
	if p.gameMode != 0 { // creative/adventure/spectator: no damage in M8
		return
	}
	// M13: fire_resistance 免除火焰类伤害（vanilla FireImmunity 检查在
	// damage 管线最前面）。
	if fireResistant(&p.effectTarget) && isFireDamage(dmgType) {
		return
	}
	// M11: worn armor absorbs physical hits (see armor.go for the 26.2
	// values and the vanilla bypasses_armor list).
	amount = reduceDamageByArmor(amount, dmgType, p)
	if amount <= 0 {
		// Maxed armor cannot fully negate a hit in vanilla (floor at 20%
		// of the raw hit), but keep the guard for safety.
		return
	}
	// M13: resistance 按 20%/级减伤（starve / out_of_world 例外）。
	amount *= resistanceFactor(&p.effectTarget, dmgType)
	if amount <= 0 {
		return
	}
	// M13: 吸收心优先扣减（vanilla absorption 在护甲之后、生命之前）。
	if p.absorption > 0 && amount > 0 {
		absorbed := float32(math.Min(float64(p.absorption), float64(amount)))
		p.absorption -= absorbed
		amount -= absorbed
	}
	if amount <= 0 {
		p.sendHealth()
		return
	}
	p.health -= amount
	if p.health < 0 {
		p.health = 0
	}
	p.eatTicksLeft = 0 // pain interrupts eating
	p.usingBow = false // pain interrupts drawing a bow
	if p.health <= 0 {
		s.killPlayerLocked(p, deathMessage(dmgType))
		return
	}
	_ = p.conn.sendDamageEvent(p.id, dmgType, cause, direct)
	_ = p.conn.sendHurtAnimation(p.id, p.yaw)
	p.sendHealth()
	s.broadcastSoundLocked("minecraft:entity.player.hurt", v776.SoundSourcePlayers,
		float32(p.x), float32(p.y+0.9), float32(p.z), 1.0, randomPitch())
}

// killPlayerLocked finishes off a player: death screen, inventory drop,
// XP reset. Caller holds s.mu.
func (s *Server) killPlayerLocked(p *player, message string) {
	p.health = 0
	p.dead = true
	p.eatTicksLeft = 0
	p.usingBow = false
	p.mining = nil
	// Vanilla drops the whole inventory and the XP on death. Items go out
	// through the locked spawn path (spawnPlayerDrop would re-lock mu).
	for i := range p.slots {
		if p.slots[i].count > 0 {
			for count := p.slots[i].count; count > 0; {
				take := count
				if take > itemMaxStack {
					take = itemMaxStack
				}
				var e *itemEntity
				if p.slots[i].potion > 0 {
					// M14：药水组件随掉落保留（药水/药水箭死亡不掉色）。
					e = newItemEntityWithPotion(s.allocEntityID(), p.x, p.y+0.5, p.z, p.slots[i].item, take, p.slots[i].potion)
				} else {
					e = newItemEntity(s.allocEntityID(), p.x, p.y+0.5, p.z, p.slots[i].item, take)
				}
				s.spawnEntityLocked(e)
				count -= take
			}
			p.slots[i] = invSlot{}
			p.conn.sendSlot(int32(i), p.slots[i])
		}
	}
	p.expProgress, p.expLevel, p.expTotal = 0, 0, 0
	s.clearPlayerEffectsLocked(p) // vanilla：死亡清除全部状态效果
	p.absorption = 0
	_ = p.conn.sendCombatKill(p.id, message)
	// Everyone tracking the victim sees the fall-over animation.
	s.broadcastEntityEventToTrackers(p.id, 3, p)
}

// deathMessage renders the chat line for a damage type (zh_CN flavour,
// matching the server's log language).
func deathMessage(dmgType int32) string {
	name := "死亡"
	switch dmgType {
	case v776.DamageTypeFall:
		name = "摔死了"
	case v776.DamageTypeOutOfWorld:
		name = "掉出了世界"
	case v776.DamageTypeStarve:
		name = "饿死了"
	case v776.DamageTypeLava:
		name = "试图在岩浆里游泳"
	case v776.DamageTypeDrown:
		name = "淹死了"
	case v776.DamageTypeInWall:
		name = "在墙里窒息而亡"
	case v776.DamageTypeFreeze:
		name = "被冻死了"
	case v776.DamageTypeMobAttack:
		name = "被怪物杀死了"
	case v776.DamageTypeArrow:
		name = "被箭射死了"
	case v776.DamageTypeExplosion:
		name = "被炸死了"
	case v776.DamageTypeOnFire, v776.DamageTypeInFire:
		name = "被烧死了"
	}
	return fmt.Sprintf("玩家%s", name)
}

// handleClientCommand processes the client command packet; only
// PERFORM_RESPAWN matters for M8.
func (c *conn) handleClientCommand() error {
	action, err := java.ReadClientCommand(c.rd)
	if err != nil {
		return err
	}
	if action != 0 { // stats / gamerule requests: nothing to answer yet
		return nil
	}
	s := c.s
	s.mu.Lock()
	p := c.player
	if p == nil || !p.dead {
		s.mu.Unlock()
		return nil
	}
	p.dead = false
	p.health = maxHealth
	p.food = maxFood
	p.saturation = startingSaturation
	p.exhaustion = 0
	p.foodTickTimer = 0
	p.fallDistance = 0
	p.eatTicksLeft = 0
	respawnPositionLocked(s, p)
	p.cx, p.cz = 0, 0
	p.onGround = true
	p.sendHealth()
	p.sendExperience()
	s.mu.Unlock()

	// Respawn handshake: the client keeps its chunks (same dimension) and
	// waits for a fresh teleport.
	if err := c.sendRespawn(java.CommonSpawnInfo{
		DimensionType:    "minecraft:overworld",
		DimensionTypeID:  1,
		Dimension:        "minecraft:overworld",
		Seed:             s.world.seed,
		GameType:         0,
		PreviousGameType: 0xFF,
		IsFlat:           true,
		PortalCooldown:   0,
		SeaLevel:         63,
	}, 0); err != nil {
		return err
	}
	if err := c.sendAttributes(p.id, maxHealth); err != nil {
		return err
	}
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayPlayerPosition)
	s.mu.Lock()
	tp := s.nextTeleportID()
	s.mu.Unlock()
	java.WritePlayPlayerPosition(c.wr, java.ClientboundPlayerPosition{
		TeleportID: tp,
		X:          p.x,
		Y:          p.y,
		Z:          p.z,
		Relatives:  0,
	})
	return c.sendPacket(c.wr.Bytes())
}

// --- per-tick survival -----------------------------------------------------

// tickSurvival advances hunger, regeneration, starvation, void damage and
// eating for one player, one tick. Runs on the ticker goroutine; takes
// Server.mu itself.
func (s *Server) tickSurvival(p *player) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.dead || p.gameMode != 0 {
		return
	}

	// M13: 状态效果时长递减与周期结算（再生/中毒/凋零/饥饿）。
	s.tickPlayerEffectsLocked(p)
	if p.dead {
		return
	}

	// Eating completes after its duration.
	if p.eatTicksLeft > 0 {
		p.eatTicksLeft--
		if p.eatTicksLeft == 0 {
			s.finishEating(p)
		}
	}

	// Void: 4 damage every half second below the world floor.
	if p.y < voidStartY {
		p.voidTicks++
		if p.voidTicks >= voidDamageInterval {
			p.voidTicks = 0
			s.damagePlayerLocked(p, voidDamage, v776.DamageTypeOutOfWorld, -1, -1)
			if p.dead {
				return
			}
		}
	} else {
		p.voidTicks = 0
	}

	// Exhaustion burns saturation first, then food.
	for p.exhaustion >= exhaustionPerFood {
		p.exhaustion -= exhaustionPerFood
		if p.saturation > 0 {
			p.saturation = float32(math.Max(0, float64(p.saturation-1)))
		} else if p.food > 0 {
			p.food--
			p.sendHealth()
		}
	}

	// Regeneration while well fed; starvation while empty.
	if p.food >= regenFoodThreshold && p.health < maxHealth {
		p.foodTickTimer++
		if p.foodTickTimer >= regenIntervalTicks {
			p.foodTickTimer = 0
			p.health = float32(math.Min(float64(maxHealth), float64(p.health+1)))
			p.exhaustion += regenExhaustion
			p.sendHealth()
		}
	} else if p.food <= 0 && p.health > starveFloorHealth {
		p.foodTickTimer++
		if p.foodTickTimer >= starveIntervalTicks {
			p.foodTickTimer = 0
			s.damagePlayerLocked(p, 1, v776.DamageTypeStarve, -1, -1)
		}
	} else {
		p.foodTickTimer = 0
	}
}

// --- movement-driven survival ----------------------------------------------

// isFireDamage reports fire-class damage types (fire_resistance grants
// full immunity to all of them).
func isFireDamage(dmgType int32) bool {
	switch dmgType {
	case v776.DamageTypeInFire, v776.DamageTypeOnFire, v776.DamageTypeLava, v776.DamageTypeHotFloor:
		return true
	}
	return false
}

// moveFallDamageLocked accumulates fall distance from a movement packet
// and applies the landing hit. Called from the connection goroutine with
// s.mu already held (movement state and the ticker share these fields).
func (s *Server) moveFallDamageLocked(p *player, prevY, newY float64, onGround bool) {
	noDmg, safeBonus := fallDamageOverride(&p.effectTarget)
	if onGround {
		// M13: jump_boost 提升 1 格/级的安全摔落高度。
		safe := fallSafeDistance + float32(safeBonus)
		if !noDmg && p.fallDistance > safe {
			dmg := float32(math.Floor(float64(p.fallDistance - safe)))
			if dmg > 0 {
				s.damagePlayerLocked(p, dmg, v776.DamageTypeFall, -1, -1)
			}
		}
		p.fallDistance = 0
		return
	}
	if newY < prevY {
		p.fallDistance += float32(prevY - newY)
	}
}

// moveExhaustion charges the movement cost of one movement packet.
// Caller holds s.mu (sprinting flag and exhaustion are tick-shared).
func (p *player) moveExhaustion(dx, dz float64) {
	dist := math.Sqrt(dx*dx + dz*dz)
	if p.sprinting {
		p.exhaustion += float32(dist) * sprintExhaustion
	} else {
		p.exhaustion += float32(dist) * walkExhaustion
	}
}

// --- eating ------------------------------------------------------------------

// useItemStart routes a right-click use: a bow begins the M11 draw,
// throwable items take the M15 throw path, everything else takes the M8
// eating path. Called from the connection goroutine.
func (c *conn) useItemStart(hand int32) {
	s := c.s
	s.mu.Lock()
	heldBow := false
	var throwPotion invSlot
	var thrower *player
	var tKind throwableKind
	if p := c.player; p != nil && !p.dead {
		held := p.slots[p.heldSlot]
		switch {
		case held.item == itemIDByName[bowItemName]:
			heldBow = true
		case held.item == itemIDByName["minecraft:splash_potion"],
			held.item == itemIDByName["minecraft:lingering_potion"]:
			// M13: 喷溅/滞留药水右键即掷（不蓄力，vanilla ThrowableItemProjectile）。
			// M15 修正：创造模式同样可掷（消耗只在生存生效，vanilla
			// ItemStack.consume 对 creative 是 no-op）。
			if held.count > 0 {
				throwPotion = held
				thrower = p
			}
		case held.item == itemIDByName["minecraft:snowball"]:
			if held.count > 0 {
				thrower = p
				tKind = throwSnowball
			}
		case held.item == itemIDByName["minecraft:egg"]:
			if held.count > 0 {
				thrower = p
				tKind = throwEgg
			}
		case held.item == itemIDByName["minecraft:ender_pearl"]:
			if held.count > 0 {
				thrower = p
				tKind = throwPearl
			}
		}
	}
	s.mu.Unlock()
	if heldBow {
		c.startBowDraw()
		return
	}
	if thrower != nil {
		if tKind == 0 {
			c.throwPotion(throwPotion, hand)
		} else {
			c.throwThrowable(tKind)
		}
		return
	}
	c.startEating(hand)
}

// startEating begins consuming the held item if it is food and there is
// room for it. Potions are always drinkable regardless of hunger (vanilla
// alwaysEdible). Called from the connection goroutine.
func (c *conn) startEating(hand int32) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := c.player
	if p == nil || p.dead || p.gameMode != 0 || p.eatTicksLeft > 0 {
		return
	}
	slot := p.slots[p.heldSlot]
	if slot.count <= 0 {
		return
	}
	fv, ok := foodByItem[slot.item]
	if !ok {
		// M13: 药水饮用：满饱食度也可饮用，同原版 alwaysEdible。
		if isDrinkablePotionSlot(slot) {
			_ = hand
			p.eatTicksLeft = eatDurationTicks
			p.eatingFood = foodValue{} // 药水走 finishEating 的 potion 分支
			p.drinkingPotion = slot.potion
		}
		return
	}
	if p.food >= maxFood {
		return
	}
	_ = hand // off-hand eating behaves identically in M8
	p.eatTicksLeft = eatDurationTicks
	p.eatingFood = fv
}

// cancelEating stops an in-progress bite, drink or bow draw (release,
// hotbar swap, damage). Caller holds no lock.
func (c *conn) cancelEating() {
	s := c.s
	s.mu.Lock()
	if p := c.player; p != nil {
		p.eatTicksLeft = 0
		p.usingBow = false // M11: an abort kills a draw without firing
		p.drinkingPotion = 0
	}
	s.mu.Unlock()
}

// finishEating applies the food values and consumes one item. Caller
// holds s.mu.
func (s *Server) finishEating(p *player) {
	slot := p.slots[p.heldSlot]
	if slot.count <= 0 {
		return
	}
	// M13: 药水饮用完成 —— 施加效果、返还玻璃瓶。
	if p.drinkingPotion > 0 {
		potionID := p.drinkingPotion - 1
		p.drinkingPotion = 0
		slot.count--
		if slot.count == 0 {
			slot = invSlot{}
		}
		p.slots[p.heldSlot] = slot
		p.conn.sendSlot(p.heldSlot, slot)
		s.applyPotionEffectsToPlayer(p, potionID, 1.0)
		// vanilla：喝完返还一个玻璃瓶（进入背包或掉落）。
		s.giveOrDropItemLocked(p, itemIDByName["minecraft:glass_bottle"], 1)
		return
	}
	fv, ok := foodByItem[slot.item]
	if !ok {
		return
	}
	// M13: 食用附加效果需要在物品被消耗前取名称。
	var foodName string
	if n := itemNameOf(slot.item); n != "" {
		foodName = n
	}
	slot.count--
	if slot.count == 0 {
		slot = invSlot{}
	}
	p.slots[p.heldSlot] = slot
	p.conn.sendSlot(p.heldSlot, slot)
	p.food = int32(math.Min(float64(maxFood), float64(p.food)+float64(fv.nutrition)))
	p.saturation = float32(math.Min(float64(p.food), float64(p.saturation+fv.saturation)))
	// M13: 食用附加效果（金苹果/腐肉/蜘蛛眼/河豚/奶桶）。
	if foodName != "" {
		s.applyConsumableEffects(p, foodName)
	}
	p.sendHealth()
}

// giveOrDropItemLocked pushes one item into the player inventory or
// spawns an item entity when full. Caller holds s.mu.
func (s *Server) giveOrDropItemLocked(p *player, itemID, count int32) {
	if itemID <= 0 || count <= 0 {
		return
	}
	for i := range p.slots {
		if p.slots[i].count == 0 {
			p.slots[i] = invSlot{item: itemID, count: count}
			p.conn.sendSlot(int32(i), p.slots[i])
			return
		}
	}
	e := newItemEntity(s.allocEntityID(), p.x, p.y+0.5, p.z, itemID, count)
	s.spawnEntityLocked(e)
}
