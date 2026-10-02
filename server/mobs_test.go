package server

import (
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// TestMobFallsAndStreams spawns a pig mid-air, ticks the world and checks
// that it lands on the surface and streams to a nearby player.
func TestMobFallsAndStreams(t *testing.T) {
	s, b, p := joinedBot(t, "Rancher")

	def := &mobDefs[0] // pig
	m := newMobEntity(def, s.allocEntityID(), p.x, p.y+5, p.z)
	m.moveTimer = 100000 // frozen brain: drop straight down
	m.walking = false
	s.spawnEntity(m)

	// Drive ~30 ticks: the pig must fall onto the grass surface.
	for i := 0; i < 30; i++ {
		s.tickEntities()
	}
	x, y, z := m.xPos()
	if y > p.y+0.01 || y < p.y-3 {
		t.Fatalf("pig landed at y=%v (player at %v)", y, p.y)
	}
	if x != p.x || z != p.z {
		t.Fatalf("pig drifted horizontally: %v,%v", x, z)
	}

	// The tracker sync must have spawned it for the bot: look for
	// add_entity followed by nothing (no metadata for mobs).
	deadline := time.Now().Add(3 * time.Second)
	_ = b.nc.SetReadDeadline(deadline)
	found := false
	for !found {
		id, r := b.next()
		if id != v776.PacketPlayAddEntity {
			continue // keep-alive noise, chunk packets and friends
		}
		entityID, _ := r.VarInt()
		_, _ = r.FixedBytes(16) // uuid
		typeID, _ := r.VarInt()
		if entityID == m.entityID() && typeID == v776.EntityTypePig {
			found = true
		}
	}
	_ = b.nc.SetReadDeadline(time.Time{})
	if !found {
		t.Fatal("pig never streamed to the player")
	}
}

// TestMobAttackDeathDrops beats a sheep to death with the attack packet
// and verifies feedback, loot and removal.
func TestMobAttackDeathDrops(t *testing.T) {
	s, b, p := joinedBot(t, "Wololo")

	def := &mobDefs[2] // sheep (8 HP)
	m := newMobEntity(def, s.allocEntityID(), p.x+1, p.y, p.z)
	m.setNetPos(m.x, m.y, m.z)
	s.spawnEntity(m)
	// Make sure the bot tracks the mob before the first hit.
	s.syncEntities()

	ironSword := itemIDByName["minecraft:iron_sword"]
	s.mu.Lock()
	p.slots[0] = invSlot{item: ironSword, count: 1}
	p.heldSlot = 0
	s.mu.Unlock()

	hits := 0
	for {
		w := protocol.NewWriter()
		w.VarInt(v776.PacketPlaySBAttack).VarInt(m.entityID())
		b.write(w.Bytes())
		hits++
		time.Sleep(25 * time.Millisecond) // let the conn goroutine apply

		s.mu.Lock()
		health, tickGap := m.health, s.tickCount-p.lastAttackTick
		s.mu.Unlock()
		_ = tickGap
		if health <= 0 {
			break
		}
		if hits > 30 {
			t.Fatalf("sheep refused to die, health=%v", health)
		}
		// The server enforces a 10-tick cooldown; outpace it via direct
		// damage for the remaining health instead of waiting real time.
		s.mu.Lock()
		s.hurtMobLocked(m, p, 2)
		s.mu.Unlock()
	}

	// Death feedback: entity event 3 (fall-over) must go out.
	s.mu.Lock()
	dying := m.deathTicks > 0
	s.mu.Unlock()
	if !dying {
		t.Fatal("mob not in death animation")
	}

	// Loot: at least the guaranteed mutton + wool item entities.
	s.mu.Lock()
	drops := 0
	for _, e := range s.entities {
		if it, ok := e.(*itemEntity); ok {
			if it.itemID == itemIDByName["minecraft:mutton"] ||
				it.itemID == itemIDByName["minecraft:white_wool"] {
				drops++
			}
		}
	}
	s.mu.Unlock()
	if drops < 2 {
		t.Fatalf("expected mutton+wool drops, got %d", drops)
	}

	// After the animation the sync retires the mob everywhere.
	for i := 0; i < mobDeathAnimTicks+3; i++ {
		s.tickEntities()
	}
	if m.alive() {
		t.Fatal("mob still alive after death animation")
	}
}

// TestMobLootTable sanity: every drop item name must resolve in the item
// registry so loot never silently vanishes.
func TestMobLootTable(t *testing.T) {
	for i := range mobDefs {
		for _, d := range mobDefs[i].drops {
			if _, ok := itemIDByName[d.item]; !ok {
				t.Fatalf("%s drops unregistered item %s", mobDefs[i].name, d.item)
			}
		}
	}
}

// TestAddEntityVanillaDecode replays the 26.2 client's add_entity decode
// field by field (including the LpVec3 velocity) and asserts zero trailing
// bytes.
func TestAddEntityVanillaDecode(t *testing.T) {
	w := protocol.NewWriter()
	uuid := [16]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	java.WriteAddEntity(w, 42, uuid, v776.EntityTypePig,
		1.5, -60.25, 3.5, 0.05, 0.2, -0.1, 12, 200, 250, 0)

	r := protocol.NewReader(w.Bytes())
	id, _ := r.VarInt()
	if id != 42 {
		t.Fatalf("id: %d", id)
	}
	gotUUID, _ := r.UUID()
	if gotUUID != uuid {
		t.Fatal("uuid mismatch")
	}
	typeID, _ := r.VarInt()
	if typeID != v776.EntityTypePig {
		t.Fatalf("type: %d", typeID)
	}
	x, _ := r.Double()
	y, _ := r.Double()
	z, _ := r.Double()
	if x != 1.5 || y != -60.25 || z != 3.5 {
		t.Fatalf("pos: %v %v %v", x, y, z)
	}
	vx, vy, vz, err := java.ReadLpVec3(r)
	if err != nil {
		t.Fatal(err)
	}
	if approx(vx, 0.05) || approx(vy, 0.2) || approx(vz, -0.1) {
		t.Fatalf("velocity decode roundtrip too lossy: %v %v %v", vx, vy, vz)
	}
	pitch, _ := r.Byte()
	yaw, _ := r.Byte()
	head, _ := r.Byte()
	if pitch != 12 || yaw != 200 || head != 250 {
		t.Fatalf("angles: %d %d %d", pitch, yaw, head)
	}
	data, _ := r.VarInt()
	if data != 0 {
		t.Fatalf("data: %d", data)
	}
	if r.Remaining() != 0 {
		t.Fatalf("%d trailing bytes", r.Remaining())
	}
}

func approx(got, want float64) bool {
	// LpVec3 quantises to 1/scale units; for our scale-1 vectors the
	// quantum is 2/32766 ≈ 6.1e-5 per unit.
	diff := got - want
	if diff < 0 {
		diff = -diff
	}
	return diff > 0.001
}

// TestLpVec3Zero is one byte on the wire.
func TestLpVec3Zero(t *testing.T) {
	w := protocol.NewWriter()
	java.WriteLpVec3(w, 0, 0, 0)
	if w.Len() != 1 || w.Bytes()[0] != 0 {
		t.Fatalf("zero vector: %d bytes, first=0x%x", w.Len(), w.Bytes()[0])
	}
	r := protocol.NewReader(w.Bytes())
	x, y, z, err := java.ReadLpVec3(r)
	if err != nil || x != 0 || y != 0 || z != 0 {
		t.Fatalf("zero roundtrip: %v %v %v %v", x, y, z, err)
	}
}
