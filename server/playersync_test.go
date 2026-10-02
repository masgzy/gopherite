package server

import (
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// TestTwoPlayersSeeEachOther joins two bots and verifies the multiplayer
// basics: tab-list updates both ways, player entity spawn, live movement
// deltas and removal on disconnect.
func TestTwoPlayersSeeEachOther(t *testing.T) {
	s := startTestServer(t)
	alice := joinBotToPlay(t, s, "Alice")
	bob := joinBotToPlayVD(t, s, "Bob", 2)

	// Alice must have received Bob's tab entry + player entity.
	deadline := time.Now().Add(3 * time.Second)
	_ = alice.nc.SetReadDeadline(deadline)
	sawInfo := false
	sawEntity := false
	for !(sawInfo && sawEntity) {
		id, r := alice.next()
		switch id {
		case v776.PacketPlayPlayerInfo:
			raw := r.Raw()
			for i := 0; i+3 <= len(raw); i++ {
				if raw[i] == 'B' && raw[i+1] == 'o' && raw[i+2] == 'b' {
					sawInfo = true
					break
				}
			}
		case v776.PacketPlayAddEntity:
			entityID, _ := r.VarInt()
			_, _ = r.FixedBytes(16)
			typeID, _ := r.VarInt()
			if typeID == v776.EntityTypePlayer {
				s.mu.Lock()
				bobID := s.playersByName("Bob")
				s.mu.Unlock()
				if bobID == 0 || entityID == bobID {
					sawEntity = true
				}
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("Bob never streamed to Alice (info=%v entity=%v)", sawInfo, sawEntity)
		}
	}
	_ = alice.nc.SetReadDeadline(time.Time{})

	// Bob moves: Alice must see the pos_rot delta.
	s.mu.Lock()
	var bobP *player
	for _, p := range s.players {
		if p.name == "Bob" {
			bobP = p
		}
	}
	s.mu.Unlock()
	if bobP == nil {
		t.Fatal("Bob missing from the player list")
	}

	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayMovePosRot)
	w.Double(bobP.x + 1).Double(bobP.y).Double(bobP.z)
	w.Float(90).Float(0)
	w.Bool(true)
	bob.write(w.Bytes())
	time.Sleep(120 * time.Millisecond) // ticker sync pass

	moveDeadline := time.Now().Add(3 * time.Second)
	_ = alice.nc.SetReadDeadline(moveDeadline)
	moved := false
	for !moved {
		id, r := alice.next()
		if id == v776.PacketPlayMoveEntityPosRot {
			entityID, _ := r.VarInt()
			if entityID == bobP.id {
				moved = true
			}
		}
		if time.Now().After(moveDeadline) {
			t.Fatal("Alice never saw Bob move")
		}
	}
	_ = alice.nc.SetReadDeadline(time.Time{})
}

// TestXPOrbPickup: an orb near the player magnetises over, grants its
// value to the XP bar and disappears.
func TestXPOrbPickup(t *testing.T) {
	s, b, p := joinedBot(t, "Thirsty")

	orb := newXPOrbEntity(s.allocEntityID(), p.x+2, p.y, p.z, 7)
	s.spawnEntity(orb)
	// Warm the tracker baseline so deltas stay valid.
	s.syncEntities()

	for i := 0; i < 60 && orb.alive(); i++ {
		s.tickEntities()
	}
	if orb.alive() {
		t.Fatal("orb never picked up")
	}
	s.mu.Lock()
	total := p.expTotal
	s.mu.Unlock()
	if total != 7 {
		t.Fatalf("xp total: %d", total)
	}
	// The collector animation went out.
	expectNoise(b, v776.PacketPlayTakeItemEntity)
}

// TestMobFallDamage drops a cow off a ledge and expects fall damage.
func TestMobFallDamage(t *testing.T) {
	s, _, p := joinedBot(t, "Vet")

	def := &mobDefs[1] // cow
	m := newMobEntity(def, s.allocEntityID(), p.x, p.y+6, p.z)
	m.moveTimer = 100000
	m.walking = false
	m.setNetPos(m.x, m.y, m.z)
	s.spawnEntity(m)
	s.syncEntities() // stream to the bot so feedback packets have a tracker

	for i := 0; i < 40 && m.alive(); i++ {
		s.tickEntities()
	}
	s.mu.Lock()
	health := m.health
	s.mu.Unlock()
	if health >= def.maxHealth {
		t.Fatalf("cow took no fall damage: health=%v", health)
	}
}

// playersByName is a test helper resolving a player's entity id.
func (s *Server) playersByName(name string) int32 {
	for _, p := range s.players {
		if p.name == name {
			return p.id
		}
	}
	return 0
}

var _ = protocol.NewReader(nil) // keep import when assertions shrink
