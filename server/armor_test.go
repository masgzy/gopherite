package server

import (
	"testing"

	"github.com/masgzy/gopherite/protocol/java/v776"
)

// id returns the registry id of a named item, failing the test if the
// generated table lacks it (26.2 data drift guard).
func id(t *testing.T, name string) int32 {
	t.Helper()
	v, ok := itemIDByName[name]
	if !ok {
		t.Fatalf("item %s missing from the registry table", name)
	}
	return v
}

// TestArmorPointsSumWornPieces checks the worn-slot scan: mixed tiers
// sum defense and toughness, empty slots contribute nothing.
func TestArmorPointsSumWornPieces(t *testing.T) {
	s, _, p := joinedBot(t, "Knight")

	s.mu.Lock()
	defer s.mu.Unlock()
	p.armor[0] = invSlot{item: id(t, "minecraft:iron_helmet"), count: 1}        // 2
	p.armor[1] = invSlot{item: id(t, "minecraft:diamond_chestplate"), count: 1} // 8 + t2
	p.armor[3] = invSlot{item: id(t, "minecraft:leather_boots"), count: 1}      // 1
	armor, tough := p.armorPointsLocked()
	if armor != 11 {
		t.Fatalf("armor points: %v, want 11", armor)
	}
	if tough != 2 {
		t.Fatalf("toughness: %v, want 2", tough)
	}
}

// TestArmorFormula matches the vanilla reduction math against hand
// computed values for iron and diamond gear.
func TestArmorFormula(t *testing.T) {
	s, _, p := joinedBot(t, "Calculator")

	s.mu.Lock()
	defer s.mu.Unlock()

	// Full iron (15 points, 0 toughness), raw 5:
	// red = max(3, 15 - 5/2) = 12.5 -> damage 5 * (1 - 12.5/25) = 2.5.
	for i := range p.armor {
		p.armor[i] = invSlot{}
	}
	p.armor[0] = invSlot{item: id(t, "minecraft:iron_helmet"), count: 1}
	p.armor[1] = invSlot{item: id(t, "minecraft:iron_chestplate"), count: 1}
	p.armor[2] = invSlot{item: id(t, "minecraft:iron_leggings"), count: 1}
	p.armor[3] = invSlot{item: id(t, "minecraft:iron_boots"), count: 1}
	if got := reduceDamageByArmor(5, v776.DamageTypeMobAttack, p); got < 2.49 || got > 2.51 {
		t.Fatalf("full iron vs 5 damage: %v, want 2.5", got)
	}

	// Full netherite (20 points, 4x3=12 toughness) near-caps reduction:
	// red = max(4, 20 - 5/(2+12/4)) = 19 -> 5 * (1 - 19/25) = 1.2.
	for i := range p.armor {
		p.armor[i] = invSlot{}
	}
	p.armor[0] = invSlot{item: id(t, "minecraft:netherite_helmet"), count: 1}
	p.armor[1] = invSlot{item: id(t, "minecraft:netherite_chestplate"), count: 1}
	p.armor[2] = invSlot{item: id(t, "minecraft:netherite_leggings"), count: 1}
	p.armor[3] = invSlot{item: id(t, "minecraft:netherite_boots"), count: 1}
	got := reduceDamageByArmor(5, v776.DamageTypeMobAttack, p)
	if got < 1.19 || got > 1.21 {
		t.Fatalf("full netherite vs 5 damage: %v, want ~1.2", got)
	}

	// Bypassed types pass through untouched: falls never care about
	// armor in 26.2 (bypasses_armor includes minecraft:fall).
	if got := reduceDamageByArmor(5, v776.DamageTypeFall, p); got != 5 {
		t.Fatalf("fall damage must bypass armor: %v", got)
	}
	if got := reduceDamageByArmor(5, v776.DamageTypeStarve, p); got != 5 {
		t.Fatalf("starvation must bypass armor: %v", got)
	}
}

// TestArmorReducesMobDamage drives the integration path: the same
// hostile-melee hit hurts an armored player less than a bare one.
func TestArmorReducesMobDamage(t *testing.T) {
	s, _, p := joinedBot(t, "Tank")

	s.mu.Lock()
	p.health = maxHealth
	s.damagePlayerLocked(p, 6, v776.DamageTypeMobAttack, -1, -1)
	bare := p.health
	// Full iron (15 points): red = max(3, 15 - 6/2 = 12) -> 6 * 13/25 = 3.12.
	p.armor[0] = invSlot{item: id(t, "minecraft:iron_helmet"), count: 1}
	p.armor[1] = invSlot{item: id(t, "minecraft:iron_chestplate"), count: 1}
	p.armor[2] = invSlot{item: id(t, "minecraft:iron_leggings"), count: 1}
	p.armor[3] = invSlot{item: id(t, "minecraft:iron_boots"), count: 1}
	p.health = maxHealth
	s.damagePlayerLocked(p, 6, v776.DamageTypeMobAttack, -1, -1)
	armored := p.health
	p.health = maxHealth
	s.damagePlayerLocked(p, 6, v776.DamageTypeFall, -1, -1)
	fallen := p.health
	s.mu.Unlock()

	if bare != maxHealth-6 {
		t.Fatalf("bare health: %v", bare)
	}
	if armored < maxHealth-3.13 || armored > maxHealth-3.11 {
		t.Fatalf("armored health: %v, want ~%v", armored, maxHealth-3.12)
	}
	if fallen != maxHealth-6 {
		t.Fatalf("fall must bypass armor: %v", fallen)
	}
}
