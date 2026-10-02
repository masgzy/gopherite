package server

import (
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// joinedBot gives a test a fully spawned player plus the server handle.
func joinedBot(t *testing.T, name string) (*Server, *botConn, *player) {
	t.Helper()
	s := startTestServer(t)
	b := joinBotToPlay(t, s, name)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		if p.name == name {
			return s, b, p
		}
	}
	t.Fatal("bot has no player after join")
	return s, b, nil
}

// expectNoise matches want while skipping background traffic (keep-alives
// and entity events from the tick loop).
func expectNoise(b *botConn, want int32) *protocol.Reader {
	b.t.Helper()
	for {
		id, r := b.next()
		if id == want {
			return r
		}
		switch id {
		case v776.PacketPlayCBKeepAlive, v776.PacketPlayEntityEvent,
			v776.PacketPlaySetEntityData, v776.PacketPlayAddEntity,
			v776.PacketPlayMoveEntityPos, v776.PacketPlayMoveEntityPosRot,
			v776.PacketPlayRotateHead, v776.PacketPlaySetPlayerInv,
			v776.PacketPlaySetHealth, v776.PacketPlaySetExperience,
			// M9 background traffic: clock resyncs, sounds and the
			// explosion packet can interleave anywhere.
			v776.PacketPlaySetTime, v776.PacketPlaySound,
			v776.PacketPlayExplode,
			// M12 background: weather game events (join snapshot and
			// ticker-driven transitions).
			v776.PacketPlayGameEvent:
			continue
		default:
			b.t.Fatalf("want packet 0x%x, got 0x%x", want, id)
			return nil
		}
	}
}

// TestDamagePlayerHealthSync verifies the damage path: health drop, damage
// feedback packets and the client vitals sync.
func TestDamagePlayerHealthSync(t *testing.T) {
	s, b, p := joinedBot(t, "Victim")

	// One heart of fall damage.
	s.damagePlayer(p, 2, v776.DamageTypeFall, -1, -1)

	r := expectNoise(b, v776.PacketPlayDamageEvent)
	id, _ := r.VarInt()
	dtype, _ := r.VarInt()
	if id != p.id || dtype != v776.DamageTypeFall {
		t.Fatalf("damage event: id=%d type=%d", id, dtype)
	}
	expectNoise(b, v776.PacketPlayHurtAnimation)
	r = expectNoise(b, v776.PacketPlaySetHealth)
	health, _ := r.Float()
	food, _ := r.VarInt()
	if health != 18 || food != 20 {
		t.Fatalf("vitals: health=%v food=%v", health, food)
	}
	s.mu.Lock()
	hp := p.health
	s.mu.Unlock()
	if hp != 18 {
		t.Fatalf("server model health: %v", hp)
	}
}

// TestPlayerDeathAndRespawn walks the full loop: lethal damage -> death
// screen packet + inventory drop -> client respawn request -> respawn
// handshake with fresh vitals.
func TestPlayerDeathAndRespawn(t *testing.T) {
	s, b, p := joinedBot(t, "Martyr")

	// Give the player something to drop so the death scatter is visible.
	s.mu.Lock()
	p.slots[0] = invSlot{item: 921, count: 3} // apple
	s.mu.Unlock()

	s.mu.Lock()
	s.damagePlayerLocked(p, 100, v776.DamageTypeGenericKill, -1, -1)
	s.mu.Unlock()

	r := expectNoise(b, v776.PacketPlayPlayerCombatKill)
	killerID, _ := r.VarInt()
	if killerID != p.id {
		t.Fatalf("combat kill victim: %d", killerID)
	}
	s.mu.Lock()
	dead, empty := p.dead, p.slots[0].count == 0
	s.mu.Unlock()
	if !dead {
		t.Fatal("player not marked dead")
	}
	if !empty {
		t.Fatal("inventory not dropped on death")
	}
	// The dropped apple must surface as an item entity.
	s.mu.Lock()
	drops := 0
	for _, e := range s.entities {
		if _, ok := e.(*itemEntity); ok {
			drops++
		}
	}
	s.mu.Unlock()
	if drops == 0 {
		t.Fatal("no item entity spawned for the death drop")
	}

	// Client asks to respawn: PERFORM_RESPAWN via client_command.
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlaySBClientCommand).VarInt(0)
	b.write(w.Bytes())

	expectNoise(b, v776.PacketPlayRespawn)
	expectNoise(b, v776.PacketPlayUpdateAttributes)
	r = expectNoise(b, v776.PacketPlayPlayerPosition)
	_, _ = r.VarInt()  // teleport id
	_, _ = r.Double()  // x
	y, _ := r.Double() // y
	r.Double()         // z
	s.mu.Lock()
	alive := !p.dead && p.health == maxHealth
	s.mu.Unlock()
	if !alive {
		t.Fatal("respawn did not restore vitals")
	}
	if y != -60 { // superflat surface at spawn
		t.Fatalf("respawn Y: %v", y)
	}
}

// TestFallDamageFromMovement replays a real fall through movement packets
// and expects the landing damage.
func TestFallDamageFromMovement(t *testing.T) {
	s, b, p := joinedBot(t, "Faller")
	_ = s

	sendPos := func(y float64, ground bool) {
		s.mu.Lock()
		bx, bz := p.x, p.z
		s.mu.Unlock()
		w := protocol.NewWriter()
		w.VarInt(v776.PacketPlayMovePos)
		w.Double(bx).Double(y).Double(bz)
		w.Bool(ground)
		b.write(w.Bytes())
		time.Sleep(5 * time.Millisecond) // let the conn goroutine digest
	}

	sendPos(-55, false) // high in the air
	for y := -56.0; y >= -59.5; y-- {
		sendPos(y, false) // free fall
	}
	sendPos(-60, true) // landing: 4 blocks over the safe 3 -> 1 damage

	r := expectNoise(b, v776.PacketPlayDamageEvent)
	_, _ = r.VarInt()
	dtype, _ := r.VarInt()
	if dtype != v776.DamageTypeFall {
		t.Fatalf("fall damage type: %d", dtype)
	}
	expectNoise(b, v776.PacketPlayHurtAnimation)
	r = expectNoise(b, v776.PacketPlaySetHealth)
	health, _ := r.Float()
	if health != 19 {
		t.Fatalf("health after 1-block-over fall: %v", health)
	}
}

// TestEatingRestoresFood covers the timed eat flow: use item with food,
// completion after eatDurationTicks, item consumption and vitals sync.
func TestEatingRestoresFood(t *testing.T) {
	s, b, p := joinedBot(t, "Eater")

	bread, ok := itemIDByName["minecraft:bread"]
	if !ok {
		t.Fatal("bread missing from item registry")
	}
	s.mu.Lock()
	p.slots[0] = invSlot{item: bread, count: 2}
	p.heldSlot = 0
	p.food = 10
	p.saturation = 0
	p.sendHealth()
	s.mu.Unlock()

	// Right click in air with the food held.
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayUseItem).VarInt(0).VarInt(1).Float(0).Float(0)
	b.write(w.Bytes())
	time.Sleep(20 * time.Millisecond)

	s.mu.Lock()
	left := p.eatTicksLeft
	if left <= 0 || left > eatDurationTicks {
		t.Fatalf("eating did not start: %d", left)
	}
	s.mu.Unlock()

	// Drive the survival ticks by hand for determinism.
	for i := 0; i < eatDurationTicks; i++ {
		s.tickSurvival(p)
	}

	s.mu.Lock()
	food := p.food
	count := p.slots[0].count
	s.mu.Unlock()
	if food != 15 { // 10 + bread nutrition 5
		t.Fatalf("food after eating: %d", food)
	}
	if count != 1 {
		t.Fatalf("bread stack after eating: %d", count)
	}
	r := expectNoise(b, v776.PacketPlaySetPlayerInv)
	_ = r
}

// TestHungerDrainAndRegen: exhaustion converts to food loss, high food
// regenerates health.
func TestHungerDrainAndRegen(t *testing.T) {
	s, _, p := joinedBot(t, "Hungry")

	s.mu.Lock()
	p.exhaustion = exhaustionPerFood
	p.saturation = 2
	s.mu.Unlock()
	s.tickSurvival(p)
	s.mu.Lock()
	sat := p.saturation
	s.mu.Unlock()
	if sat != 1 {
		t.Fatalf("saturation after one exhaustion unit: %v", sat)
	}

	// Regen: food 20, damaged, four seconds of ticks.
	s.mu.Lock()
	p.health = 15
	p.exhaustion = 0
	s.mu.Unlock()
	for i := 0; i < regenIntervalTicks; i++ {
		s.tickSurvival(p)
	}
	s.mu.Lock()
	hp := p.health
	s.mu.Unlock()
	if hp != 16 {
		t.Fatalf("health after regen window: %v", hp)
	}
}

// TestVoidDamageKills: below y=-64 the void bites every half second.
func TestVoidDamageKills(t *testing.T) {
	s, _, p := joinedBot(t, "Voyager")

	s.mu.Lock()
	p.y = -65
	s.mu.Unlock()
	for i := 0; i < 6*voidDamageInterval; i++ {
		s.tickSurvival(p)
	}
	s.mu.Lock()
	dead := p.dead
	s.mu.Unlock()
	if !dead {
		t.Fatal("void did not kill")
	}
}
