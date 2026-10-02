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
	weapons := map[string]float32{
		"minecraft:wooden_sword":    4,
		"minecraft:stone_sword":     5,
		"minecraft:iron_sword":      6,
		"minecraft:diamond_sword":   7,
		"minecraft:netherite_sword": 8,
		"minecraft:wooden_axe":      7,
		"minecraft:stone_axe":       9,
		"minecraft:iron_axe":        10,
		"minecraft:diamond_axe":     11,
		"minecraft:netherite_axe":   12,
	}
	for name, dmg := range weapons {
		if id, ok := itemIDByName[name]; ok {
			weaponDamage[id] = dmg
		}
	}
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
	p.health -= amount
	if p.health < 0 {
		p.health = 0
	}
	p.eatTicksLeft = 0 // pain interrupts eating
	if p.health <= 0 {
		s.killPlayerLocked(p, deathMessage(dmgType))
		return
	}
	_ = p.conn.sendDamageEvent(p.id, dmgType, cause, direct)
	_ = p.conn.sendHurtAnimation(p.id, p.yaw)
	p.sendHealth()
}

// killPlayerLocked finishes off a player: death screen, inventory drop,
// XP reset. Caller holds s.mu.
func (s *Server) killPlayerLocked(p *player, message string) {
	p.health = 0
	p.dead = true
	p.eatTicksLeft = 0
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
				e := newItemEntity(s.allocEntityID(), p.x, p.y+0.5, p.z, p.slots[i].item, take)
				s.spawnEntityLocked(e)
				count -= take
			}
			p.slots[i] = invSlot{}
			p.conn.sendSlot(int32(i), p.slots[i])
		}
	}
	p.expProgress, p.expLevel, p.expTotal = 0, 0, 0
	_ = p.conn.sendCombatKill(p.id, message)
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
	p.x, p.y, p.z = 0.5, float64(s.surfaceY(0, 0)), 0.5
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

// moveFallDamage accumulates fall distance from a movement packet and
// applies the landing hit. Called from the connection goroutine.
func (s *Server) moveFallDamage(p *player, prevY, newY float64, onGround bool) {
	if onGround {
		if p.fallDistance > fallSafeDistance {
			dmg := float32(math.Floor(float64(p.fallDistance - fallSafeDistance)))
			if dmg > 0 {
				s.damagePlayer(p, dmg, v776.DamageTypeFall, -1, -1)
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
func (p *player) moveExhaustion(dx, dz float64) {
	dist := math.Sqrt(dx*dx + dz*dz)
	if p.sprinting {
		p.exhaustion += float32(dist) * sprintExhaustion
	} else {
		p.exhaustion += float32(dist) * walkExhaustion
	}
}

// --- eating ------------------------------------------------------------------

// startEating begins consuming the held item if it is food and there is
// room for it. Called from the connection goroutine.
func (c *conn) startEating(hand int32) {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := c.player
	if p == nil || p.dead || p.gameMode != 0 || p.eatTicksLeft > 0 {
		return
	}
	if p.food >= maxFood {
		return
	}
	slot := p.slots[p.heldSlot]
	fv, ok := foodByItem[slot.item]
	if !ok || slot.count <= 0 {
		return
	}
	_ = hand // off-hand eating behaves identically in M8
	p.eatTicksLeft = eatDurationTicks
	p.eatingFood = fv
}

// cancelEating stops an in-progress bite (release, hotbar swap, damage).
// Caller holds no lock.
func (c *conn) cancelEating() {
	s := c.s
	s.mu.Lock()
	if p := c.player; p != nil {
		p.eatTicksLeft = 0
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
	fv, ok := foodByItem[slot.item]
	if !ok {
		return
	}
	slot.count--
	if slot.count == 0 {
		slot = invSlot{}
	}
	p.slots[p.heldSlot] = slot
	p.conn.sendSlot(p.heldSlot, slot)
	p.food = int32(math.Min(float64(maxFood), float64(p.food)+float64(fv.nutrition)))
	p.saturation = float32(math.Min(float64(p.food), float64(p.saturation+fv.saturation)))
	p.sendHealth()
}
