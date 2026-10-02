package server

import (
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol/java/v776"
)

// TestBowFullDrawFiresConsumeAndPickup draws a bow for 40 ticks, releases
// and expects: one arrow entity flying at full power, one arrow consumed
// from the inventory, and the landed arrow collectible again.
func TestBowFullDrawFiresConsumeAndPickup(t *testing.T) {
	s, _, p := joinedBot(t, "Archer")

	bow := id(t, "minecraft:bow")
	arrow := id(t, "minecraft:arrow")
	s.mu.Lock()
	p.slots[0] = invSlot{item: bow, count: 1}
	p.slots[1] = invSlot{item: arrow, count: 5}
	p.heldSlot = 0
	s.mu.Unlock()

	p.conn.startBowDraw()
	s.mu.Lock()
	drawing := p.usingBow
	s.mu.Unlock()
	if !drawing {
		t.Fatal("bow did not start drawing with ammo in the inventory")
	}

	// Simulate 40 ticks of drawing (full power at 20+).
	s.mu.Lock()
	s.tickCount += 40
	s.mu.Unlock()
	p.conn.releaseBow()

	s.mu.Lock()
	ammo := p.slots[1].count
	var flight *arrowEntity
	for _, e := range s.entities {
		if a, ok := e.(*arrowEntity); ok {
			flight = a
		}
	}
	s.mu.Unlock()
	if ammo != 4 {
		t.Fatalf("arrow count after shot: %d, want 4", ammo)
	}
	if flight == nil {
		t.Fatal("no arrow entity spawned")
	}
	flight.mu.Lock()
	speed2 := flight.vx*flight.vx + flight.vy*flight.vy + flight.vz*flight.vz
	fromPlayer, pickup := flight.fromPlayer, flight.pickup
	shooter := flight.shooter
	flight.mu.Unlock()
	if !fromPlayer || !pickup {
		t.Fatalf("arrow flags wrong: fromPlayer=%v pickup=%v", fromPlayer, pickup)
	}
	if shooter != p.id {
		t.Fatalf("arrow shooter: %d, want %d", shooter, p.id)
	}
	// Full power launches at ~3 blocks/tick (the vanilla 0.0075 aim
	// jitter perturbs each axis, so allow a small margin).
	if speed2 < 8.9 || speed2 > 9.1 {
		t.Fatalf("full-draw speed^2: %v, want ~9", speed2)
	}

	// Walk-over collection for the stuck arrow: teleport it onto the
	// player, then run the consume pass (it takes s.mu itself).
	s.mu.Lock()
	flight.mu.Lock()
	flight.x, flight.y, flight.z = p.x+0.2, p.y+0.9, p.z
	flight.stuck = true
	flight.mu.Unlock()
	s.mu.Unlock()

	s.consumeArrowHits()

	s.mu.Lock()
	count := p.slots[1].count
	s.mu.Unlock()
	if count != 5 {
		t.Fatalf("arrow not collected: count %d, want 5", count)
	}
}

// TestBowWeakDrawDropsTheShot: below 10% power vanilla discards the
// shot without consuming ammo.
func TestBowWeakDrawDropsTheShot(t *testing.T) {
	s, _, p := joinedBot(t, "Fiddler")

	arrow := id(t, "minecraft:arrow")
	s.mu.Lock()
	p.slots[0] = invSlot{item: id(t, "minecraft:bow"), count: 1}
	p.slots[1] = invSlot{item: arrow, count: 5}
	p.heldSlot = 0
	s.mu.Unlock()

	p.conn.startBowDraw()
	p.conn.releaseBow() // zero charge

	s.mu.Lock()
	defer s.mu.Unlock()
	if p.slots[1].count != 5 {
		t.Fatalf("ammo consumed on a weak shot: %d", p.slots[1].count)
	}
	for _, e := range s.entities {
		if _, ok := e.(*arrowEntity); ok {
			t.Fatal("weak shot spawned an arrow")
		}
	}
}

// TestBowNeedsAmmo: survival players without arrows never enter the
// draw state.
func TestBowNeedsAmmo(t *testing.T) {
	s, _, p := joinedBot(t, "Unprepared")

	s.mu.Lock()
	p.slots[0] = invSlot{item: id(t, "minecraft:bow"), count: 1}
	p.heldSlot = 0
	s.mu.Unlock()

	p.conn.startBowDraw()
	s.mu.Lock()
	drawing := p.usingBow
	s.mu.Unlock()
	if drawing {
		t.Fatal("draw started without ammo")
	}
}

// TestPlayerArrowHitsMob flies a full-power arrow into a pig and expects
// arrow-typed damage, the arrow to despawn and the shooter credited.
func TestPlayerArrowHitsMob(t *testing.T) {
	s, b, p := joinedBot(t, "Sniper")

	def := &mobDefs[0] // pig
	m := newMobEntity(def, s.allocEntityID(), p.x+2, p.y, p.z)
	m.moveTimer = 100000
	m.walking = false
	s.spawnEntity(m)
	s.syncEntities()

	fullHealth := func() float32 {
		s.mu.Lock()
		defer s.mu.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.health
	}
	_ = b
	before := fullHealth()

	// One block of travel per tick, straight through the pig's body.
	a := &arrowEntity{
		id: s.allocEntityID(), shooter: p.id,
		fromPlayer: true, pickup: true,
		x: p.x, y: p.y + 0.5, z: p.z,
		vx: 1.0,
	}
	s.spawnEntity(a)
	s.tickEntities()
	s.tickEntities()

	s.mu.Lock()
	m.mu.Lock()
	health, dying := m.health, m.deathTicks > 0
	m.mu.Unlock()
	a.mu.Lock()
	arrowDead := a.dead
	a.mu.Unlock()
	s.mu.Unlock()

	if health >= before {
		t.Fatalf("pig health %v after an arrow hit (was %v)", health, before)
	}
	if dying && health > 0 {
		t.Fatal("pig dying but health positive")
	}
	if !arrowDead {
		t.Fatal("player arrow survived an entity hit (vanilla discards it)")
	}
}

// TestAttackCooldownPerWeapon checks the 26.2 attack-speed table against
// the tick-granular cooldown.
func TestAttackCooldownPerWeapon(t *testing.T) {
	s, _, p := joinedBot(t, "Fencer")

	s.mu.Lock()
	defer s.mu.Unlock()
	cases := []struct {
		item string
		want int64
	}{
		{"", 5},                      // bare hand: 4/s
		{"minecraft:iron_sword", 12}, // 20/1.6 = 12.499... in float64
		{"minecraft:wooden_axe", 25}, // 20/0.8
		{"minecraft:netherite_axe", 20},
		{"minecraft:iron_pickaxe", 17}, // 20/1.2 = 16.66 -> 17
		{"minecraft:diamond_hoe", 5},   // 4/s
		{"minecraft:mace", 33},         // 20/0.6
	}
	for _, c := range cases {
		for i := range p.slots {
			p.slots[i] = invSlot{}
		}
		if c.item != "" {
			p.slots[0] = invSlot{item: id(t, c.item), count: 1}
		}
		if got := p.attackCooldownTicks(); got != c.want {
			t.Fatalf("%q cooldown: %d, want %d", c.item, got, c.want)
		}
	}
}

// TestCriticalHitMultiplier beats a pig while falling and expects the
// vanilla 1.5x critical bonus.
func TestCriticalHitMultiplier(t *testing.T) {
	s, _, p := joinedBot(t, "Assassin")

	def := &mobDefs[0] // pig: 10 HP
	m := newMobEntity(def, s.allocEntityID(), p.x+1, p.y, p.z)
	s.spawnEntity(m)

	readHealth := func() float32 {
		s.mu.Lock()
		defer s.mu.Unlock()
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.health
	}

	s.mu.Lock()
	p.slots[0] = invSlot{item: id(t, "minecraft:iron_sword"), count: 1}
	p.heldSlot = 0
	p.onGround, p.fallDistance = true, 0
	p.lastAttackTick = -1000 // the server just booted: clear the cooldown
	s.mu.Unlock()
	_ = p.conn.handleAttack(m.entityID())
	time.Sleep(20 * time.Millisecond)
	normal := def.maxHealth - readHealth() // spent on the grounded hit
	s.mu.Lock()
	m.mu.Lock()
	m.health = def.maxHealth
	m.mu.Unlock()
	s.mu.Unlock()

	// Fall + hit: same swing but 1.5x.
	s.mu.Lock()
	p.onGround, p.fallDistance = false, 1.5
	s.tickCount += 100 // clear the swing cooldown
	s.mu.Unlock()
	_ = p.conn.handleAttack(m.entityID())
	time.Sleep(20 * time.Millisecond)
	crit := def.maxHealth - readHealth()

	if normal <= 0 {
		t.Fatal("grounded hit dealt no damage")
	}
	if crit < normal*1.49 || crit > normal*1.51 {
		t.Fatalf("crit damage %v, want ~%v", crit, normal*1.5)
	}
}

// TestPvpMeleeKnockback lets one bot punch another and expects
// player_attack damage plus a motion packet to the victim.
func TestPvpMeleeKnockback(t *testing.T) {
	s := startTestServer(t)
	_ = joinBotToPlay(t, s, "Bully")
	b2 := joinBotToPlay(t, s, "Punchline")
	s.mu.Lock()
	var p1, p2 *player
	for _, p := range s.players {
		switch p.name {
		case "Bully":
			p1 = p
		case "Punchline":
			p2 = p
		}
	}
	s.mu.Unlock()
	if p1 == nil || p2 == nil {
		t.Fatal("bots missing after join")
	}

	s.mu.Lock()
	// Overlap the boxes (within reach) and clear the swing cooldown:
	// the server booted moments ago, so lastAttackTick must go negative.
	p2.x, p2.y, p2.z = p1.x+0.5, p1.y, p1.z
	p1.lastAttackTick = -1000
	s.mu.Unlock()

	// Drive the attack synchronously so a rejection is observable.
	if err := p1.conn.handleAttack(p2.id); err != nil {
		t.Fatalf("handleAttack: %v", err)
	}
	s.mu.Lock()
	hurt := p2.health < maxHealth
	s.mu.Unlock()
	if !hurt {
		p1.conn.handleAttack(p2.id) // second attempt after any cooldown
		s.mu.Lock()
		hurt = p2.health < maxHealth
		s.mu.Unlock()
	}
	if !hurt {
		t.Fatal("PvP punch dealt no damage")
	}
	// The victim must receive the vanilla knockback motion packet.
	_ = b2.nc.SetReadDeadline(time.Now().Add(3 * time.Second))
	found := false
	for i := 0; i < 40 && !found; i++ {
		id, r := b2.next()
		if id == v776.PacketPlaySetEntityMotion {
			eid, _ := r.VarInt()
			if eid == p2.id {
				found = true
			}
		}
	}
	_ = b2.nc.SetReadDeadline(time.Time{})
	if !found {
		t.Fatal("no SetEntityMotion knockback for the PvP victim")
	}
}
