package server

import (
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// waitFor polls cond every 10 ms until it holds or the timeout elapses;
// the server's own ticker does the work, so tests never call tickOnce
// directly (that would race the 50 ms loop).
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}

// botPlayer finds the joined player model of a named bot.
func botPlayer(t *testing.T, s *Server, name string) *player {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		if p.name == name {
			return p
		}
	}
	t.Fatalf("player %s not in play", name)
	return nil
}

// TestItemPhysicsLanding drops an item in mid-air and lets the real
// ticker run: it must settle on the grass surface (bottom at y=-60) with
// ground contact and damped horizontal motion.
func TestItemPhysicsLanding(t *testing.T) {
	s := startTestServer(t)
	e := newItemEntity(s.allocEntityID(), 0.5, -55, 0.5, itemIDByName["minecraft:cobblestone"], 1)
	s.spawnEntity(e)

	if !waitFor(t, 5*time.Second, func() bool {
		_, _, _, _, vy, _, ground := e.snapshot()
		return ground && vy == 0
	}) {
		t.Fatalf("item never landed (ground/vy via snapshot)")
	}
	_, y, _, _, _, _, _ := e.snapshot()
	if diff := y - (-60.0); diff < -0.05 || diff > 0.0001 {
		t.Fatalf("rest height y=%f, want ~-60.0", y)
	}
	// Ground friction damps the spawn jitter: settled items are still.
	waitFor(t, 2*time.Second, func() bool {
		_, _, _, vx, _, vz, _ := e.snapshot()
		return vx*vx+vz*vz < 1e-4
	})
	_, _, _, vx, _, vz, _ := e.snapshot()
	if vx > 0.01 || vz > 0.01 || vx < -0.01 || vz < -0.01 {
		t.Fatalf("ground friction failed: vx=%f vz=%f", vx, vz)
	}
}

// TestMineDropsItem digs the grass surface and expects an item entity
// spawn whose metadata carries the dirt stack (grass drops dirt).
func TestMineDropsItem(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Dropper")
	_ = b

	// Dig at (4,-61,4): far enough from the spawn point (0.5,-60,0.5)
	// that the drop is not instantly collected (pickup radius ~1).
	const seq = int32(11)
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayPlayerAction)
	w.VarInt(int32(java.ActionStartDestroy))
	java.WriteBlockPos(w, 4, -61, 4)
	w.VarInt(0)
	w.VarInt(seq)
	b.write(w.Bytes())

	dirt := itemIDByName["minecraft:dirt"]
	if dirt == 0 {
		t.Fatal("dirt item id missing")
	}

	b.nc.SetReadDeadline(time.Now().Add(8 * time.Second))
	spawned, meta := false, false
	var itemEntityID int32
	for !(spawned && meta) {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayAddEntity:
			gotID, _ := rr.VarInt()
			raw, _ := rr.FixedBytes(16)
			_ = raw
			typ, _ := rr.VarInt()
			if typ != v776.EntityTypeItem {
				t.Fatalf("entity type %d, want item(%d)", typ, v776.EntityTypeItem)
			}
			itemEntityID = gotID
			spawned = true
		case v776.PacketPlaySetEntityData:
			if _, err := rr.VarInt(); err != nil {
				t.Fatal(err)
			}
			idx, _ := rr.Byte()
			if idx != 8 {
				t.Fatalf("metadata index %d, want 8", idx)
			}
			typ, _ := rr.VarInt()
			if typ != 7 {
				t.Fatalf("metadata type %d, want item_stack(7)", typ)
			}
			count, _ := rr.VarInt()
			if count != 1 {
				t.Fatalf("stack count %d, want 1", count)
			}
			item, _ := rr.VarInt()
			if item != dirt {
				t.Fatalf("item id %d, want dirt registry id %d (raw, 非 holder id+1)", item, dirt)
			}
			adds, _ := rr.VarInt()
			removes, _ := rr.VarInt()
			if adds != 0 || removes != 0 {
				t.Fatalf("component patches %d/%d", adds, removes)
			}
			end, _ := rr.Byte()
			if end != 0xFF {
				t.Fatalf("metadata terminator 0x%x", end)
			}
			meta = true
		case v776.PacketPlayCBKeepAlive, v776.PacketPlayBlockChangedAck,
			v776.PacketPlayBlockUpdate, v776.PacketPlayEntityEvent:
			continue
		}
	}
	if itemEntityID == 0 {
		t.Fatal("no entity id in spawn")
	}
}

// TestItemPickupWalkover drops an item right under the bot: the ticker
// picks it up, and the client sees take animation + entity removal +
// inventory slot refresh.
func TestItemPickupWalkover(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Collector")
	p := botPlayer(t, s, "Collector")

	cobble := itemIDByName["minecraft:cobblestone"]
	s.mu.Lock()
	px, py, pz := p.x, p.y, p.z
	s.mu.Unlock()
	e := newItemEntity(s.allocEntityID(), px, py, pz, cobble, 1)
	e.pickupDelay = 25 // first sync pass streams it, then the walkover
	// Kill the spawn pop: a stationary item stays under the player so
	// the walkover is deterministic (real drops slide up to ~3 blocks).
	e.vx, e.vy, e.vz = 0, 0, 0
	s.spawnEntity(e)

	if !waitFor(t, 3*time.Second, func() bool { return !e.alive() }) {
		t.Fatal("item survived walkover pickup")
	}
	s.mu.Lock()
	got := p.slots[9] // hotbar 0-8 starts full, so the stack lands at 9
	s.mu.Unlock()
	if got.item != cobble || got.count != 1 {
		t.Fatalf("slot 9 = %+v, want cobblestone x1", got)
	}

	// Wire sequence: take_item_entity + remove_entities + slot sync.
	b.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	var took, removed, slotted bool
	deadline := time.Now().Add(5 * time.Second)
	for !(took && removed && slotted) && time.Now().Before(deadline) {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayTakeItemEntity:
			itemID, _ := rr.VarInt()
			playerID, _ := rr.VarInt()
			amount, _ := rr.VarInt()
			if itemID != e.id || playerID != p.id || amount != 1 {
				t.Fatalf("take %d by %d x%d", itemID, playerID, amount)
			}
			took = true
		case v776.PacketPlayRemoveEntities:
			n, _ := rr.VarInt()
			for i := int32(0); i < n; i++ {
				got, _ := rr.VarInt()
				if got == e.id {
					removed = true
				}
			}
		case v776.PacketPlaySetPlayerInv:
			slot, _ := rr.VarInt()
			count, _ := rr.VarInt()
			item, _ := rr.VarInt()
			if slot == 9 && count == 1 && item == cobble {
				slotted = true
			}
		case v776.PacketPlayCBKeepAlive:
			continue
		}
	}
	if !(took && removed && slotted) {
		t.Fatalf("pickup sequence incomplete: took=%v removed=%v slotted=%v", took, removed, slotted)
	}
}

// TestItemDespawn ages an item past the 6000-tick lifespan and expects it
// to retire and broadcast removal.
func TestItemDespawn(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Waiter")
	_ = b
	p := botPlayer(t, s, "Waiter")

	s.mu.Lock()
	px, py, pz := p.x, p.y, p.z
	s.mu.Unlock()
	e := newItemEntity(s.allocEntityID(), px, py, pz, itemIDByName["minecraft:dirt"], 1)
	e.pickupDelay = 1 << 20 // never picked up: aging only
	e.age = itemLifespanTicks - 1
	s.spawnEntity(e)

	if !waitFor(t, 3*time.Second, func() bool { return !e.alive() }) {
		t.Fatal("item survived its lifespan")
	}
	s.mu.Lock()
	_, live := s.entities[e.id]
	s.mu.Unlock()
	if live {
		t.Fatal("dead entity still registered")
	}
}
