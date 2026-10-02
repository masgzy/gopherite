package server

// Redstone tests against a real superflat world: state lookups, lever
// toggling, dust power decay and lamp lighting.

import (
	"testing"

	"github.com/masgzy/gopherite/protocol/java"
)

func newRedstoneTestServer(t *testing.T) *Server {
	t.Helper()
	s := startTestServer(t)
	return s
}

func TestStateIDRoundTrip(t *testing.T) {
	sid := stateIDOf("minecraft:redstone_wire", map[string]string{
		"north": "side", "south": "side", "east": "none", "west": "none",
		"power": "7",
	})
	if sid <= 0 {
		t.Fatalf("wire state lookup failed: %d", sid)
	}
	if blockNameOf(int(sid)) != "minecraft:redstone_wire" {
		t.Fatalf("round trip mismatch: %s", blockNameOf(int(sid)))
	}
	props := blockPropsOf(int(sid))
	if props["power"] != "7" || props["north"] != "side" {
		t.Fatalf("props mismatch: %+v", props)
	}
	lit := stateIDOf("minecraft:redstone_lamp", map[string]string{"lit": "true"})
	if blockPropsOf(int(lit))["lit"] != "true" {
		t.Fatal("lamp lit state lookup failed")
	}
}

func TestLeverToggle(t *testing.T) {
	s := newRedstoneTestServer(t)
	// Pick a position and place a lever manually.
	x, y, z := 5, -60, 5
	leverState := stateIDOf("minecraft:lever", map[string]string{
		"face": "floor", "facing": "north", "powered": "false",
	})
	if !s.world.setBlock(x, y, z, leverState) {
		t.Fatal("place lever failed")
	}
	s.mu.Lock()
	s.toggleLever(x, y, z)
	s.mu.Unlock()
	props := blockPropsOf(int(s.world.getBlock(x, y, z)))
	if props["powered"] != "true" {
		t.Fatalf("lever not powered: %+v", props)
	}
	s.mu.Lock()
	s.toggleLever(x, y, z)
	s.mu.Unlock()
	props = blockPropsOf(int(s.world.getBlock(x, y, z)))
	if props["powered"] != "false" {
		t.Fatalf("lever did not toggle back: %+v", props)
	}
}

func TestDustPropagationAndLamp(t *testing.T) {
	s := newRedstoneTestServer(t)
	// lever - wire - wire - lamp along +X at y=-60 (superflat surface).
	y := -60
	lever := stateIDOf("minecraft:lever", map[string]string{
		"face": "floor", "facing": "east", "powered": "false",
	})
	wireOff := stateIDOf("minecraft:redstone_wire", map[string]string{
		"north": "none", "south": "none", "east": "none", "west": "none", "power": "0",
	})
	lampOff := stateIDOf("minecraft:redstone_lamp", map[string]string{"lit": "false"})
	s.world.setBlock(10, y, 10, lever)
	s.world.setBlock(11, y, 10, wireOff)
	s.world.setBlock(12, y, 10, wireOff)
	s.world.setBlock(13, y, 10, lampOff)

	// Toggle the lever on: recomputation must power the dust chain and
	// light the lamp.
	s.mu.Lock()
	s.toggleLever(10, y, 10)
	s.mu.Unlock()

	w1 := blockPropsOf(int(s.world.getBlock(11, y, 10)))
	w2 := blockPropsOf(int(s.world.getBlock(12, y, 10)))
	if w1["power"] != "15" || w1["west"] != "side" || w1["east"] != "side" {
		t.Fatalf("wire1 = %+v", w1)
	}
	if w2["power"] != "14" {
		t.Fatalf("wire2 power = %+v", w2)
	}
	lamp := blockPropsOf(int(s.world.getBlock(13, y, 10)))
	if lamp["lit"] != "true" {
		t.Fatalf("lamp not lit: %+v", lamp)
	}

	// Toggle off: everything resets.
	s.mu.Lock()
	s.toggleLever(10, y, 10)
	s.mu.Unlock()
	w1 = blockPropsOf(int(s.world.getBlock(11, y, 10)))
	if w1["power"] != "0" {
		t.Fatalf("wire1 power after off: %+v", w1)
	}
	lamp = blockPropsOf(int(s.world.getBlock(13, y, 10)))
	if lamp["lit"] != "false" {
		t.Fatalf("lamp still lit: %+v", lamp)
	}
}

func TestDustDecayLimits(t *testing.T) {
	s := newRedstoneTestServer(t)
	y := -60
	lever := stateIDOf("minecraft:lever", map[string]string{
		"face": "floor", "facing": "east", "powered": "false",
	})
	wireOff := stateIDOf("minecraft:redstone_wire", map[string]string{
		"north": "none", "south": "none", "east": "none", "west": "none", "power": "0",
	})
	s.world.setBlock(0, y, 40, lever)
	for i := 1; i <= 20; i++ {
		s.world.setBlock(i, y, 40, wireOff)
	}
	s.mu.Lock()
	s.toggleLever(0, y, 40)
	s.mu.Unlock()

	// Wire adjacent to the source reads 15, then decays by 1 per step.
	for i, want := 1, 15; i <= 20; i, want = i+1, want-1 {
		props := blockPropsOf(int(s.world.getBlock(i, y, 40)))
		got := props["power"]
		if want >= 0 && got != itoaTest(want) {
			t.Fatalf("wire %d power = %s want %d", i, got, want)
		}
		if want < 0 && got != "0" {
			t.Fatalf("wire %d should be 0: %s", i, got)
		}
	}
}

func itoaTest(v int) string {
	if v == 0 {
		return "0"
	}
	var b []byte
	for v > 0 {
		b = append([]byte{byte('0' + v%10)}, b...)
		v /= 10
	}
	return string(b)
}

func TestWireDropsRedstoneItem(t *testing.T) {
	drop, ok := blockDrops("minecraft:redstone_wire")
	if !ok || drop != "minecraft:redstone" {
		t.Fatalf("wire drop = %s", drop)
	}
	if _, has := itemIDByName[drop]; !has {
		t.Fatal("redstone item id missing")
	}
	_ = java.ParserBool
}
