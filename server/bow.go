package server

import (
	"math"
	"math/rand"

	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M11 player bow. Drawing mirrors the M8 eating model: UseItem with a
// bow starts the draw, ActionReleaseUseItem fires (or aborts). The
// server is authoritative over the charge time; the client free-
// simulates the arrow from the spawn velocity like every other entity.

// Vanilla AbstractArrow bow numbers.
const (
	bowMaxPower       = 3.0 // full-draw launch speed in blocks/tick
	bowSecondsPerShot = 1.0 // charge seconds for full power
	bowItemName       = "minecraft:bow"
	arrowItemName     = "minecraft:arrow"
)

// startBowDraw begins drawing if the held item is a bow and ammo (or
// creative mode) allows a shot. Called from the connection goroutine
// inside the UseItem dispatch; takes s.mu itself.
func (c *conn) startBowDraw() {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := c.player
	if p == nil || p.dead || p.gameMode != 0 || p.usingBow {
		return
	}
	if p.slots[p.heldSlot].item != itemIDByName[bowItemName] {
		return // not a bow — the caller already routed food elsewhere
	}
	if p.gameMode != 1 && !p.hasItemLocked(itemIDByName[arrowItemName]) {
		return // survival without ammo: vanilla never starts the draw
	}
	p.usingBow = true
	p.useStartTick = s.tickCount
}

// releaseBow fires the drawn arrow (if any) with the vanilla power
// curve. Called on ActionReleaseUseItem; takes s.mu itself.
func (c *conn) releaseBow() {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := c.player
	if p == nil || p.dead || !p.usingBow {
		return
	}
	charge := s.tickCount - p.useStartTick
	p.usingBow = false
	s.fireBowLocked(p, charge)
}

// cancelBowDraw aborts a draw without firing (damage, hotbar swap,
// death). Caller holds s.mu.
func cancelBowDraw(p *player) {
	p.usingBow = false
}

// hasItemLocked reports whether the main inventory (hotbar + storage,
// not the off-hand) holds at least one of the item. Caller holds s.mu.
func (p *player) hasItemLocked(itemID int32) bool {
	for i := range p.slots {
		if p.slots[i].item == itemID && p.slots[i].count > 0 {
			return true
		}
	}
	return false
}

// fireBowLocked launches one arrow with vanilla's charge curve:
// power = (t + t²) / 3 for t = chargeSeconds, clamped to [0.1, 1];
// launch speed = power * 3. Survival consumes one arrow. Caller holds
// s.mu.
func (s *Server) fireBowLocked(p *player, chargeTicks int64) {
	t := float64(chargeTicks) / 20.0 / bowSecondsPerShot
	power := (t*t + t*2) / 3
	if power < 0.1 {
		return // vanilla drops the shot entirely below 10 % power
	}
	if power > 1 {
		power = 1
	}
	arrowID := itemIDByName[arrowItemName]
	// M14：记录消耗的那支箭的药水组件（药水箭发射后保留药效）。
	firedPotion := int32(0)
	if p.gameMode != 1 {
		idx := -1
		for i := range p.slots {
			if p.slots[i].item == arrowID && p.slots[i].count > 0 {
				idx = int(i)
				break
			}
		}
		if idx < 0 {
			return // ran dry mid-draw: vanilla silently cancels
		}
		held := p.slots[idx]
		firedPotion = held.potion // M14：药水箭的 potion_contents 随箭飞行
		held.count--
		if held.count == 0 {
			held = invSlot{}
		}
		p.slots[idx] = held
		p.conn.sendSlot(int32(idx), held)
	}

	// Eye position, launch along the view vector (vanilla
	// shootFromRotation: -sin(yaw)·cos(pitch), -sin(pitch), cos(yaw)·cos(pitch)).
	yawRad := float64(p.yaw) * math.Pi / 180
	pitchRad := float64(p.pitch) * math.Pi / 180
	speed := power * bowMaxPower
	ex, ey, ez := p.x, p.y+1.62, p.z
	vx := -math.Sin(yawRad) * math.Cos(pitchRad) * speed
	vy := -math.Sin(pitchRad) * speed
	vz := math.Cos(yawRad) * math.Cos(pitchRad) * speed
	// Vanilla jitters the aim by 0.0075 per axis at full power; scale it
	// with the power like vanilla's velocity-based spread.
	j := 0.0075 * float64(power)
	e := &arrowEntity{
		id:         s.allocEntityID(),
		shooter:    p.id,
		fromPlayer: true,
		pickup:     p.gameMode != 1, // creative arrows are not collectible
		potion:     firedPotion,
		crit:       power == 1, // vanilla BowItem：满蓄力置 FLAG_CRIT
		x:          ex, y: ey, z: ez,
		vx: vx + rand.NormFloat64()*j,
		vy: vy + rand.NormFloat64()*j,
		vz: vz + rand.NormFloat64()*j,
	}
	_, _ = rand.Read(e.uuid[:])
	e.uuid[6] = (e.uuid[6] & 0x0F) | 0x40
	e.uuid[8] = (e.uuid[8] & 0x3F) | 0x80
	e.updateRotation()
	s.spawnEntityLocked(e)
	s.broadcastSoundLocked("minecraft:entity.arrow.shoot", v776.SoundSourcePlayers,
		float32(ex), float32(ey), float32(ez), 1.0, randomPitch())
}
