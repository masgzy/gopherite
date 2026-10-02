package server

import (
	"math"

	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M11 armor defense. Values come from the official 26.2 jar
// (net.minecraft.world.item.equipment.ArmorMaterials, decompiled):
// each material defines per-piece defense points and a global toughness
// bonus (diamond 2.0, netherite 3.0, everything else 0). Reduction uses
// the vanilla generic.armor formula:
//
//	reduction = max(armor/5, armor - damage/(2 + toughness/4)), capped at 20
//	damage    = damage * (1 - reduction/25)

// armorStats is one wearable item's defense profile.
type armorStats struct {
	armor     float32
	toughness float32
}

// armorItemStats maps wearable item registry ids to their defense
// profile; anything absent contributes nothing.
var armorItemStats = map[int32]armorStats{}

// bypassesArmor mirrors 26.2 data/minecraft/tags/damage_type/
// bypasses_armor.json for the damage types this server actually emits:
// armor never reduces starvation, void, suffocation, drowning, freezing
// or burning-tick damage (falls included — verified against the tag).
var bypassesArmor = map[int32]bool{
	v776.DamageTypeFall:        true,
	v776.DamageTypeStarve:      true,
	v776.DamageTypeDrown:       true,
	v776.DamageTypeInWall:      true,
	v776.DamageTypeOnFire:      true,
	v776.DamageTypeFreeze:      true,
	v776.DamageTypeGeneric:     true,
	v776.DamageTypeOutOfWorld:  true,
	v776.DamageTypeGenericKill: true,
}

func init() {
	// name -> {defense, toughness}; values straight from ArmorMaterials.
	defs := map[string]armorStats{
		// Leather: 5 total.
		"minecraft:leather_helmet":     {1, 0},
		"minecraft:leather_chestplate": {3, 0},
		"minecraft:leather_leggings":   {2, 0},
		"minecraft:leather_boots":      {1, 0},
		// Copper (new in 26.x): between leather and chainmail.
		"minecraft:copper_helmet":     {2, 0},
		"minecraft:copper_chestplate": {4, 0},
		"minecraft:copper_leggings":   {3, 0},
		"minecraft:copper_boots":      {1, 0},
		// Chainmail.
		"minecraft:chainmail_helmet":     {2, 0},
		"minecraft:chainmail_chestplate": {5, 0},
		"minecraft:chainmail_leggings":   {4, 0},
		"minecraft:chainmail_boots":      {1, 0},
		// Iron.
		"minecraft:iron_helmet":     {2, 0},
		"minecraft:iron_chestplate": {6, 0},
		"minecraft:iron_leggings":   {5, 0},
		"minecraft:iron_boots":      {2, 0},
		// Gold.
		"minecraft:golden_helmet":     {2, 0},
		"minecraft:golden_chestplate": {5, 0},
		"minecraft:golden_leggings":   {3, 0},
		"minecraft:golden_boots":      {1, 0},
		// Diamond: toughness 2.
		"minecraft:diamond_helmet":     {3, 2},
		"minecraft:diamond_chestplate": {8, 2},
		"minecraft:diamond_leggings":   {6, 2},
		"minecraft:diamond_boots":      {3, 2},
		// Netherite: toughness 3.
		"minecraft:netherite_helmet":     {3, 3},
		"minecraft:netherite_chestplate": {8, 3},
		"minecraft:netherite_leggings":   {6, 3},
		"minecraft:netherite_boots":      {3, 3},
		// Turtle helmet (turtle scute material).
		"minecraft:turtle_helmet": {2, 0},
	}
	for name, st := range defs {
		if id, ok := itemIDByName[name]; ok {
			armorItemStats[id] = st
		}
	}
}

// armorPointsLocked sums the worn armor's defense points and toughness.
// Caller holds s.mu (the armor array is Server.mu-guarded).
func (p *player) armorPointsLocked() (armor, toughness float32) {
	for _, slot := range p.armor {
		if st, ok := armorItemStats[slot.item]; ok && slot.count > 0 {
			armor += st.armor
			toughness += st.toughness
		}
	}
	return armor, toughness
}

// reduceDamageByArmor applies the vanilla armor formula to one hit.
// Bypassed damage types pass through untouched.
func reduceDamageByArmor(amount float32, dmgType int32, p *player) float32 {
	if bypassesArmor[dmgType] {
		return amount
	}
	armor, toughness := p.armorPointsLocked()
	if armor <= 0 {
		return amount
	}
	red := math.Max(float64(armor)/5, float64(armor)-float64(amount)/(2+float64(toughness)/4))
	if red > 20 {
		red = 20
	}
	if red <= 0 {
		return amount
	}
	return amount * float32(1-red/25)
}
