package server

// Container block entities (M10): the persistent contents and burn state
// behind chests and furnaces, mirroring the 26.2 vanilla block entity
// classes (RandomizableContainer / AbstractFurnaceBlockEntity).
//
// Locking: every blockEntity lives in Server.blockEnts and its fields are
// guarded by Server.mu like the rest of the player/entity model. The save
// path takes a deep snapshot under mu before serialising (save.go), so
// nothing here is ever touched without mu.

import (
	"math/rand"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// beKind discriminates the implemented block entities.
type beKind int

const (
	beChest beKind = iota
	beFurnace
	beBrewing
)

// vanilla block entity ids for the NBT round-trip.
func (k beKind) id() string {
	switch k {
	case beFurnace:
		return "minecraft:furnace"
	case beBrewing:
		return "minecraft:brewing_stand"
	}
	return "minecraft:chest"
}

func (k beKind) slots() int {
	switch k {
	case beFurnace:
		return 3 // ingredient, fuel, result (AbstractFurnaceBlockEntity)
	case beBrewing:
		return 5 // 0-2 potions, 3 ingredient, 4 fuel (BrewingStandBlockEntity)
	}
	return 27 // 3x9 chest grid (RandomizableContainer)
}

// blockEntity is one placed container's server-side state.
type blockEntity struct {
	kind    beKind
	x, y, z int

	// slots is the raw container: chest grid order for chests,
	// ingredient/fuel/result for furnaces.
	slots []invSlot

	// furnace burn state (vanilla AbstractFurnaceBlockEntity fields).
	litTimeRemaining int
	litTotalTime     int
	cookingTimer     int
	cookingTotalTime int

	// M13 brewing stand state (vanilla BrewingStandBlockEntity fields).
	brewTime int32 // 0..400 递减
	fuel     int32 // 剩余可操作次数（一柱烈焰粉 = 20）

	// lastIngredient tracks the ingredient item between tick visits so a
	// same-item top-up does not reset the cook progress (vanilla setItem
	// resets only when the item changes).
	lastIngredient int32

	// xpBank accumulates smelting experience until the result stack is
	// taken (vanilla recipesUsed + createExperience).
	xpBank float32

	// lastData caches the four values last pushed to viewers so the
	// per-tick SetData stream only fires on change.
	lastData [4]int
}

func newBlockEntity(kind beKind, x, y, z int) *blockEntity {
	return &blockEntity{kind: kind, x: x, y: y, z: z, slots: make([]invSlot, kind.slots())}
}

// posKey is the blockEnts map key.
func (b *blockEntity) posKey() [3]int { return [3]int{b.x, b.y, b.z} }

// ---- server-side registry -------------------------------------------------

// blockEntsAt returns the block entity at a position or nil.
// Caller holds Server.mu.
func (s *Server) blockEntsAt(x, y, z int) *blockEntity {
	return s.blockEnts[[3]int{x, y, z}]
}

// ensureBlockEntity lazily creates the container state for a placed chest
// or furnace (blocks placed before M10 or freshly loaded chunks). Caller
// holds Server.mu.
func (s *Server) ensureBlockEntity(kind beKind, x, y, z int) *blockEntity {
	if b := s.blockEntsAt(x, y, z); b != nil {
		return b
	}
	b := newBlockEntity(kind, x, y, z)
	if s.blockEnts == nil {
		s.blockEnts = make(map[[3]int]*blockEntity)
	}
	s.blockEnts[b.posKey()] = b
	return b
}

// removeBlockEntity drops the container's contents as item entities at the
// block and unregisters it (break/explosion path). Caller holds Server.mu.
func (s *Server) removeBlockEntity(b *blockEntity) {
	for _, st := range b.slots {
		if st.count > 0 {
			var e entity
			if st.potion > 0 {
				// M13: 药水掉落保留 potion_contents 组件。
				e = newItemEntityWithPotion(s.allocEntityID(), float64(b.x)+0.5, float64(b.y)+0.4, float64(b.z)+0.5, st.item, st.count, st.potion)
			} else {
				e = newItemEntity(s.allocEntityID(), float64(b.x)+0.5, float64(b.y)+0.4, float64(b.z)+0.5, st.item, st.count)
			}
			s.spawnEntityLocked(e)
		}
	}
	delete(s.blockEnts, b.posKey())
}

// ---- fuel and smelting tables ---------------------------------------------

// fuelBurnTicks maps item ids to their vanilla burn duration (FuelValues
// vanillaBurnTimes, baseUnit 200).
var fuelBurnTicks = map[int32]int32{}

// smeltByInput maps a furnace input item id to its smelting recipe.
var smeltByInput = map[int32]*recipeDef{}

func init() {
	// Tag-driven fuels first (logs/planks 300, wool 100, ...), then the
	// explicit item list from the vanilla fuel table.
	burnFor := func(names []string, ticks int32) {
		for _, n := range names {
			if id, ok := itemIDByName[n]; ok {
				fuelBurnTicks[id] = ticks
			}
		}
	}
	burnFor(tagMembers("#minecraft:logs"), 300)
	burnFor(tagMembers("#minecraft:planks"), 300)
	burnFor(tagMembers("#minecraft:wooden_stairs"), 300)
	burnFor(tagMembers("#minecraft:wooden_slabs"), 150)
	burnFor(tagMembers("#minecraft:wooden_fences"), 300)
	burnFor(tagMembers("#minecraft:wooden_doors"), 200)
	burnFor(tagMembers("#minecraft:wooden_pressure_plates"), 300)
	burnFor(tagMembers("#minecraft:wooden_buttons"), 100)
	burnFor(tagMembers("#minecraft:saplings"), 100)
	burnFor(tagMembers("#minecraft:wool"), 100)
	burnFor(tagMembers("#minecraft:wool_carpets"), 67)
	burnFor(tagMembers("#minecraft:banners"), 300)
	burnFor(tagMembers("#minecraft:signs"), 200)
	burnFor(tagMembers("#minecraft:hanging_signs"), 800)
	burnFor(tagMembers("#minecraft:boats"), 1200)

	fuelNames := map[string]int32{
		"minecraft:lava_bucket":        20000,
		"minecraft:coal_block":         16000,
		"minecraft:blaze_rod":          2400,
		"minecraft:dried_kelp_block":   4001,
		"minecraft:coal":               1600,
		"minecraft:charcoal":           1600,
		"minecraft:bamboo_block":       300,
		"minecraft:bamboo_mosaic":      300,
		"minecraft:bamboo":             50,
		"minecraft:bookshelf":          300,
		"minecraft:chiseled_bookshelf": 300,
		"minecraft:lectern":            300,
		"minecraft:jukebox":            300,
		"minecraft:chest":              300,
		"minecraft:trapped_chest":      300,
		"minecraft:crafting_table":     300,
		"minecraft:daylight_detector":  300,
		"minecraft:ladder":             300,
		"minecraft:loom":               300,
		"minecraft:barrel":             300,
		"minecraft:cartography_table":  300,
		"minecraft:fletching_table":    300,
		"minecraft:smithing_table":     300,
		"minecraft:composter":          300,
		"minecraft:note_block":         300,
		"minecraft:bow":                300,
		"minecraft:crossbow":           300,
		"minecraft:fishing_rod":        300,
		"minecraft:mangrove_roots":     300,
		"minecraft:azalea":             100,
		"minecraft:flowering_azalea":   100,
		"minecraft:dead_bush":          100,
		"minecraft:short_dry_grass":    100,
		"minecraft:tall_dry_grass":     100,
		"minecraft:leaf_litter":        100,
		"minecraft:bowl":               100,
		"minecraft:scaffolding":        50,
		"minecraft:stick":              100,
		"minecraft:wooden_shovel":      200,
		"minecraft:wooden_sword":       200,
		"minecraft:wooden_hoe":         200,
		"minecraft:wooden_axe":         200,
		"minecraft:wooden_pickaxe":     200,
	}
	for n, t := range fuelNames {
		if id, ok := itemIDByName[n]; ok {
			fuelBurnTicks[id] = t
		}
	}

	// Furnace recipes: smelting kind, first definition per input wins.
	for _, r := range recipesOfKind("minecraft:smelting") {
		if len(r.Slots) == 0 || r.Result == "" {
			continue
		}
		for _, id := range ingItemIDs(r.Slots[0].Ingr) {
			if _, taken := smeltByInput[id]; !taken {
				smeltByInput[id] = r
			}
		}
	}
}

// fuelTicksOf returns the burn duration of an item, 0 when not a fuel.
func fuelTicksOf(itemID int32) int32 { return fuelBurnTicks[itemID] }

// recipeCookTime is the vanilla default 200 ticks unless the recipe says
// otherwise.
func recipeCookTime(r *recipeDef) int {
	if r.CookTime > 0 {
		return int(r.CookTime)
	}
	return 200
}

// smeltResultOf resolves the recipe output of an input item.
func smeltResultOf(itemID int32) (*recipeDef, bool) {
	r, ok := smeltByInput[itemID]
	return r, ok
}

// ---- furnace tick ----------------------------------------------------------

// furnace data slot indexes (AbstractFurnaceBlockEntity.DATA_*).
const (
	dataLitTime          = 0
	dataLitDuration      = 1
	dataCookingProgress  = 2
	dataCookingTotalTime = 3
)

// burnCoolSpeed is vanilla's cooldown per tick when unlit (2/tick).
const burnCoolSpeed = 2

// tickBlockEntities advances every furnace/brewing stand once. Runs on
// the ticker with Server.mu held by tickOnce — the whole loop is locked
// work, mirroring the vanilla serverTick methods.
func (s *Server) tickBlockEntities() {
	for _, b := range s.blockEnts {
		switch b.kind {
		case beFurnace:
			s.tickFurnaceLocked(b)
		case beBrewing:
			s.tickBrewingLocked(b)
		}
	}
}

// tickFurnaceLocked is the vanilla serverTick port: burn-down, fuel
// ignition, cook progress, result production and the lit block state.
// Caller holds Server.mu.
func (s *Server) tickFurnaceLocked(b *blockEntity) {
	wasLit := b.litTimeRemaining > 0
	isLit := wasLit
	if isLit {
		b.litTimeRemaining--
		isLit = b.litTimeRemaining > 0
	}

	fuel := b.slots[1]
	ingredient := b.slots[0]
	changed := false
	if isLit || (fuel.count > 0 && ingredient.count > 0) {
		if ingredient.count > 0 {
			if recipe, ok := smeltResultOf(ingredient.item); ok {
				resultID, ok2 := itemIDByName[recipe.Result]
				resultCount := recipe.resultCount()
				if ok2 && furnaceCanBurn(b, resultID, resultCount) {
					if !isLit {
						newLit := int(fuelTicksOf(fuel.item))
						b.litTimeRemaining = newLit
						b.litTotalTime = newLit
						if newLit > 0 {
							consumeFuel(b)
							isLit = true
							changed = true
						}
					}
					if isLit {
						if b.cookingTotalTime == 0 {
							b.cookingTotalTime = recipeCookTime(recipe)
						}
						b.cookingTimer++
						if b.cookingTimer >= b.cookingTotalTime {
							b.cookingTimer = 0
							b.cookingTotalTime = recipeCookTime(recipe)
							furnaceBurn(b, resultID, resultCount)
							b.xpBank += recipe.Exp
							changed = true
						}
					} else {
						b.cookingTimer = 0
					}
				} else {
					b.cookingTimer = 0
				}
			} else {
				b.cookingTimer = 0
			}
		} else {
			b.cookingTimer = 0
		}
	} else if b.cookingTimer > 0 {
		b.cookingTimer -= burnCoolSpeed
		if b.cookingTimer < 0 {
			b.cookingTimer = 0
		}
	}

	// Lit block state swap (furnace[lit=true]) with a block update.
	if wasLit != isLit {
		changed = true
		// Copy the current property set verbatim and flip lit: the state
		// index matches on the exact property set (furnace has facing+lit
		// only — no waterlogged in 26.2).
		props := blockPropsOf(int(s.world.getBlock(b.x, b.y, b.z)))
		if props == nil {
			props = map[string]string{}
		}
		props["lit"] = boolStr(isLit)
		if sid := stateIDOf("minecraft:furnace", props); sid > 0 {
			if s.world.setBlock(b.x, b.y, b.z, sid) {
				s.broadcastBlockUpdateLocked(int32(b.x), int32(b.y), int32(b.z), sid)
			}
		}
	}

	if changed {
		s.world.markDirty(int32(b.x>>4), int32(b.z>>4))
	}
	b.pushFurnaceData(s, wasLit, isLit)
}

// furnaceCanBurn checks the result slot against the incoming stack
// (vanilla canBurn).
func furnaceCanBurn(b *blockEntity, resultID int32, resultCount int32) bool {
	cur := b.slots[2]
	if cur.count <= 0 {
		return true
	}
	if cur.item != resultID {
		return false
	}
	return cur.count+resultCount <= itemMaxStack
}

// consumeFuel burns one fuel item, honouring the bucket remainder
// (vanilla consumeFuel + craftingRemainder).
func consumeFuel(b *blockEntity) {
	fuel := b.slots[1]
	fuel.count--
	if fuel.count <= 0 {
		if itemNameOf(fuel.item) == "minecraft:lava_bucket" {
			if bucket, ok := itemIDByName["minecraft:bucket"]; ok {
				b.slots[1] = invSlot{item: bucket, count: 1}
				return
			}
		}
		b.slots[1] = invSlot{}
		return
	}
	b.slots[1] = fuel
}

// furnaceBurn moves one cooked stack into the result slot and consumes the
// ingredient (vanilla burn).
func furnaceBurn(b *blockEntity, resultID, resultCount int32) {
	cur := b.slots[2]
	if cur.count <= 0 {
		b.slots[2] = invSlot{item: resultID, count: resultCount}
	} else {
		cur.count += resultCount
		b.slots[2] = cur
	}
	b.slots[0].count--
	if b.slots[0].count <= 0 {
		b.slots[0] = invSlot{}
	}
}

// pushFurnaceData streams the four furnace data slots to every player
// with this furnace's menu open, only when a value changed (vanilla
// ContainerData broadcast).
func (b *blockEntity) pushFurnaceData(s *Server, wasLit, isLit bool) {
	lit := b.litTimeRemaining
	if lit == 0 && isLit {
		lit = b.litTotalTime // client renders the lit progress from data 0/1
	}
	vals := [4]int{lit, b.litTotalTime, b.cookingTimer, b.cookingTotalTime}
	for _, p := range s.players {
		m := p.openMenu
		if m == nil || m.be != b {
			continue
		}
		for i, v := range vals {
			if v == b.lastData[i] {
				continue
			}
			b.lastData[i] = v
			body := protocol.NewWriter()
			body.VarInt(v776.PacketPlayContainerSetData)
			java.WriteContainerSetData(body, m.id, int16(i), int16(v))
			_ = p.conn.sendPacket(body.Bytes())
		}
	}
}

// settleFurnaceXP spawns the banked smelting experience when the result
// stack is emptied. Caller holds Server.mu.
func (s *Server) settleFurnaceXP(b *blockEntity) {
	if b.xpBank <= 0 || len(b.slots) < 3 || b.slots[2].count > 0 {
		return
	}
	whole := int(b.xpBank)
	frac := b.xpBank - float32(whole)
	if frac > 0 && rand.Float64() < float64(frac) {
		whole++
	}
	if whole <= 0 {
		b.xpBank = 0
		return
	}
	e := newXPOrbEntity(s.allocEntityID(), float64(b.x)+0.5, float64(b.y)+0.5, float64(b.z)+0.5, int32(whole))
	s.spawnEntityLocked(e)
	b.xpBank = 0
}

// resetFurnaceInput mirrors vanilla setItem on the ingredient slot: a
// different item restarts the cook timer with the recipe's duration.
func resetFurnaceInput(b *blockEntity) {
	ingredient := b.slots[0]
	if ingredient.count <= 0 {
		b.lastIngredient = 0
		return
	}
	if ingredient.item == b.lastIngredient {
		return
	}
	b.lastIngredient = ingredient.item
	b.cookingTimer = 0
	if recipe, ok := smeltResultOf(ingredient.item); ok {
		b.cookingTotalTime = recipeCookTime(recipe)
	} else {
		b.cookingTotalTime = 0
	}
}
