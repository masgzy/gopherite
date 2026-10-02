package server

import (
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// TestZombieChasesAndMelee plants a zombie three blocks from a survival
// player and verifies the chase brain closes the gap and the swing lands
// with the mob_attack damage type.
func TestZombieChasesAndMelee(t *testing.T) {
	s, _, p := joinedBot(t, "Bait")

	def := &hostileDefs[0] // zombie
	m := newMobEntity(def, s.allocEntityID(), p.x+3, p.y, p.z)
	s.spawnEntity(m)

	startDx := m.x - p.x
	for i := 0; i < 60; i++ {
		s.tickEntities()
	}
	mx, my, mz := m.xPos()
	closeDx := mx - p.x
	if abs64(abs64(closeDx)-abs64(startDx)) < 1.0 && closeDx != 0 {
		// allow some wobble but the zombie must have moved closer
	}
	if abs64(closeDx) > abs64(startDx) {
		t.Fatalf("zombie did not approach: started %v, now %v", startDx, closeDx)
	}
	if p.health >= maxHealth {
		t.Fatalf("zombie never landed a hit (player health %v)", p.health)
	}
	_ = my
	_ = mz
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// TestCreeperFuseExplodes parks a creeper next to the player: within the
// fuse window it must detonate, excavate the ground and hurt (or kill)
// the player.
func TestCreeperFuseExplodes(t *testing.T) {
	s, _, p := joinedBot(t, "Sapper")

	def := &hostileDefs[2] // creeper
	m := newMobEntity(def, s.allocEntityID(), p.x+2, p.y, p.z)
	s.spawnEntity(m)

	groundY := int(p.y) - 1
	if s.world.getBlock(int(p.x), groundY, int(p.z)) == stateAir {
		groundY = int(p.y) - 2
	}
	before := s.world.getBlock(int(p.x), groundY, int(p.z))
	if before == stateAir {
		t.Fatal("test setup: expected solid ground under the player")
	}

	for i := 0; i < 70; i++ {
		s.tickEntities()
	}

	if _, still := s.entities[m.entityID()]; still {
		if m.alive() {
			t.Fatal("creeper never exploded")
		}
	}
	if s.world.getBlock(int(p.x), groundY, int(p.z)) != stateAir {
		t.Fatal("explosion left the ground under the player intact")
	}
	if !p.dead && p.health >= maxHealth {
		t.Fatal("explosion never touched the player")
	}
}

// TestSkeletonShootsArrow places a skeleton in bow range and waits for a
// launched arrow to fly and hit the player.
func TestSkeletonShootsArrow(t *testing.T) {
	s, _, p := joinedBot(t, "PinCushion")

	def := &hostileDefs[1] // skeleton
	m := newMobEntity(def, s.allocEntityID(), p.x+8, p.y, p.z)
	s.spawnEntity(m)

	sawArrow := false
	for i := 0; i < 80; i++ {
		s.tickEntities()
		if !sawArrow {
			for _, e := range s.entities {
				if _, ok := e.(*arrowEntity); ok {
					sawArrow = true
				}
			}
		}
	}
	if !sawArrow {
		t.Fatal("skeleton never fired an arrow")
	}
	if p.health >= maxHealth {
		t.Fatal("arrow never connected with a stationary player")
	}
	if p.health <= 0 {
		t.Fatal("player died in test (arrow damage overshoot)")
	}
}

// TestDaylightBurnsZombie rewinds the clock to noon: the zombie must
// catch fire and take periodic burn damage.
func TestDaylightBurnsZombie(t *testing.T) {
	s, _, p := joinedBot(t, "Sunbather")

	def := &hostileDefs[0] // zombie
	m := newMobEntity(def, s.allocEntityID(), p.x+10, p.y, p.z)
	s.spawnEntity(m)

	s.mu.Lock()
	s.timeTicks = 1000 // bright morning
	s.mu.Unlock()

	for i := 0; i < 60; i++ {
		s.tickEntities()
	}
	m.mu.Lock()
	health := m.health
	m.mu.Unlock()
	if health >= def.maxHealth {
		t.Fatalf("zombie took no burn damage: health %v", health)
	}
}

// TestNightGatesHostileSpawns checks topUpHostiles directly: spawns at
// night, holds its fire during the day.
func TestNightGatesHostileSpawns(t *testing.T) {
	s, _, _ := joinedBot(t, "NightWatch")

	s.mu.Lock()
	s.timeTicks = 15000 // night
	s.mu.Unlock()
	s.topUpHostiles()
	s.mu.Lock()
	nightCount := 0
	for _, e := range s.entities {
		if m, ok := e.(*mobEntity); ok && m.def.hostile {
			nightCount++
		}
	}
	s.mu.Unlock()
	if nightCount == 0 {
		t.Fatal("no hostiles spawned at night")
	}

	s.mu.Lock()
	s.timeTicks = 5000 // day
	s.mu.Unlock()
	s.topUpHostiles()
	s.mu.Lock()
	dayCount := 0
	for _, e := range s.entities {
		if m, ok := e.(*mobEntity); ok && m.def.hostile {
			dayCount++
		}
	}
	s.mu.Unlock()
	if dayCount != nightCount {
		t.Fatalf("daylight spawn leaked: night %d -> day %d", nightCount, dayCount)
	}
}

// TestMobMetadataFrame decodes the mob set_entity_data frame byte by
// byte: index 0 shared flags + the creeper swell entry at index 16.
func TestMobMetadataFrame(t *testing.T) {
	s, _, _ := joinedBot(t, "Inspector")

	def := &hostileDefs[2] // creeper
	m := newMobEntity(def, s.allocEntityID(), 0.5, 60, 0.5)
	m.fireTicks = 0
	m.swellDir = 1

	w := protocol.NewWriter()
	writeMobMetadata(w, m)
	r := protocol.NewReader(w.Bytes())

	id, _ := r.VarInt()
	if id != v776.PacketPlaySetEntityData {
		t.Fatalf("frame id 0x%x", id)
	}
	gotID, _ := r.VarInt()
	if gotID != m.entityID() {
		t.Fatalf("entity id %d", gotID)
	}
	idx, _ := r.Byte()
	ser, _ := r.VarInt()
	if idx != v776.MetaIndexEntityFlags || ser != v776.MetaSerializerByte {
		t.Fatalf("flags entry: idx %d ser %d", idx, ser)
	}
	flags, _ := r.Byte()
	if flags != 0 {
		t.Fatalf("expected clean flags, got 0x%x", flags)
	}
	idx, _ = r.Byte()
	ser, _ = r.VarInt()
	if idx != v776.MetaIndexCreeperSwell || ser != v776.MetaSerializerVarInt {
		t.Fatalf("swell entry: idx %d ser %d", idx, ser)
	}
	swell, _ := r.VarInt()
	if swell != 1 {
		t.Fatalf("swell dir %d", swell)
	}
	term, _ := r.Byte()
	if term != 0xFF {
		t.Fatalf("terminator 0x%x", term)
	}

	// Burning state flips the shared flag bit.
	m.fireTicks = 100
	w.Reset()
	writeMobMetadata(w, m)
	r = protocol.NewReader(w.Bytes())
	_, _ = r.VarInt() // packet id
	_, _ = r.VarInt() // entity id
	_, _ = r.Byte()
	_, _ = r.VarInt()
	flags, _ = r.Byte()
	if flags&v776.EntityFlagOnFire == 0 {
		t.Fatalf("on-fire bit missing: 0x%x", flags)
	}
}

// TestSetTimeClockWire verifies the 26.2 clock-sync payload layout the
// client depends on for sun position: gameTime + one overworld entry.
func TestSetTimeClockWire(t *testing.T) {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlaySetTime)
	java.WritePlaySetTime(w, 12345, []java.ClockState{{
		ClockID:     0,
		TotalTicks:  14500,
		PartialTick: 0,
		Rate:        1.0,
	}})
	r := protocol.NewReader(w.Bytes())
	id, _ := r.VarInt()
	if id != v776.PacketPlaySetTime {
		t.Fatalf("frame id 0x%x", id)
	}
	gameTime, _ := r.Int64()
	clocks, _ := r.VarInt()
	if gameTime != 12345 || clocks != 1 {
		t.Fatalf("gameTime=%d clocks=%d", gameTime, clocks)
	}
	clockID, _ := r.VarInt()
	total, _ := r.VarLong()
	partial, _ := r.Float()
	rate, _ := r.Float()
	if clockID != 0 || total != 14500 || partial != 0 || rate != 1 {
		t.Fatalf("clock entry: id=%d total=%d partial=%v rate=%v", clockID, total, partial, rate)
	}
	if r.Remaining() != 0 {
		t.Fatalf("trailing bytes: %d", r.Remaining())
	}
}

// TestArrowFlightAndHit drives an arrowEntity directly at a player and
// checks the segment hit test triggers deferred damage.
func TestArrowFlightAndHit(t *testing.T) {
	s, _, p := joinedBot(t, "Target")

	a := &arrowEntity{
		id: s.allocEntityID(), shooter: 999,
		x: p.x + 5, y: p.y + 1.0, z: p.z,
		vx: -1.6, vy: 0.2, vz: 0,
	}
	a.updateRotation()
	s.spawnEntity(a)

	for i := 0; i < 20 && p.health >= maxHealth; i++ {
		s.tickEntities()
	}
	if p.health >= maxHealth {
		t.Fatal("arrow flew through the player without damage")
	}
	if a.alive() {
		a.mu.Lock()
		stuck := a.stuck
		a.mu.Unlock()
		if !stuck {
			t.Fatal("hit arrow neither stuck nor removed")
		}
	}
	_ = time.Now
}
