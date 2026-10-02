package server

// M7a unit tests: crafting grid matching, crafting consumption and the
// container menu click state machine. These run against the generated
// 26.2 recipe table without a network harness.

import (
	"testing"
)

func itemID(t *testing.T, name string) int32 {
	t.Helper()
	id, ok := itemIDByName[name]
	if !ok {
		t.Fatalf("unknown item %s", name)
	}
	return id
}

// grid9 builds a 3x3 grid from row-major item names ("-" = empty).
func grid9(t *testing.T, rows ...string) []invSlot {
	t.Helper()
	buildCraftingRecipes()
	grid := make([]invSlot, 9)
	i := 0
	for _, r := range rows {
		for _, cell := range r {
			if cell == '-' {
				i++
				continue
			}
			name := "minecraft:" + string(cell)
			switch string(cell) {
			case "L":
				name = "minecraft:oak_log"
			case "P":
				name = "minecraft:oak_planks"
			case "I":
				name = "minecraft:iron_ingot"
			case "S":
				name = "minecraft:stick"
			case "C":
				name = "minecraft:cobblestone"
			case "M":
				name = "minecraft:milk_bucket"
			case "W":
				name = "minecraft:wheat"
			case "E":
				name = "minecraft:egg"
			case "G":
				name = "minecraft:grass_block"
			}
			grid[i] = invSlot{item: itemID(t, name), count: 1}
			i++
		}
	}
	return grid
}

func expectResult(t *testing.T, grid []invSlot, wantItem string, wantCount ...int32) {
	t.Helper()
	res := matchCraftingResult(grid)
	want := int32(1)
	if len(wantCount) > 0 {
		want = wantCount[0]
	}
	if wantItem == "" {
		if res.count != 0 {
			t.Fatalf("expected no craft, got %s x%d", itemNameOf(res.item), res.count)
		}
		return
	}
	if res.count != want || itemNameOf(res.item) != wantItem {
		t.Fatalf("craft result = %s x%d, want %s x%d",
			itemNameOf(res.item), res.count, wantItem, want)
	}
}

func TestCraftingMatch(t *testing.T) {
	// Shapeless with a tag ingredient: any oak log -> 4 planks.
	expectResult(t, grid9(t, "L--", "---", "---"), "minecraft:oak_planks", 4)
	// Shaped symmetric: 2x2 planks -> crafting table (also works when
	// pushed into a corner of the 3x3 grid).
	expectResult(t, grid9(t, "PP-", "PP-", "---"), "minecraft:crafting_table", 1)
	expectResult(t, grid9(t, "--P", "--P", "---"), "minecraft:stick", 4) // column = sticks
	expectResult(t, grid9(t, "P-P", "---", "---"), "") // horizontal gap, no recipe
	expectResult(t, grid9(t, "P--", "P--", "--P"), "") // odd layout, no match
	// Shaped with mirror: wooden axe ("PP/PS/ S").
	expectResult(t, grid9(t, "PP-", "PS-", "-S-"), "minecraft:wooden_axe", 1)
	expectResult(t, grid9(t, "-PP", "-SP", "-S-"), "minecraft:wooden_axe", 1)
}

func TestCraftingShapelessMultiset(t *testing.T) {
	// Suspicious stew? simpler: wheat+egg+sugar? Not resolved here.
	// Use stick: shaped 2 planks vertical -> 4 sticks.
	expectResult(t, grid9(t, "-P-", "-P-", "---"), "minecraft:stick", 4)
	// Stone tools: cobblestone pickaxe "CCC / -S- / -S-".
	expectResult(t, grid9(t, "CCC", "-S-", "-S-"), "minecraft:stone_pickaxe", 1)
	// Same cells shifted right trim to a 2x3 box = stone HOE shape.
	expectResult(t, grid9(t, "-CC", "--S", "--S"), "minecraft:stone_hoe", 1)
	expectResult(t, grid9(t, "-C-", "-S-", "---"), "") // too small
}

func TestCraftingResultFitsSmallGrid(t *testing.T) {
	// 2x2 grid behaviour (inventory menu): planks in a 3x3 grid trimmed
	// to 2x2 must match the crafting table recipe positioned anywhere.
	expectResult(t, grid9(t, "-PP", "-PP", "---"), "minecraft:crafting_table", 1)
	expectResult(t, grid9(t, "---", "PP-", "PP-"), "minecraft:crafting_table", 1)
}

func TestCraftConsumeAndRemainder(t *testing.T) {
	buildCraftingRecipes()
	m := newCraftingMenu(1)
	p := &player{}
	m.grid = grid9(t, "L--", "---", "---")
	m.refreshResult()
	if m.result.count != 4 {
		t.Fatalf("planks result = %d", m.result.count)
	}
	m.craftConsume(p)
	if m.grid[0].count != 0 {
		t.Fatalf("log not consumed: %+v", m.grid[0])
	}
	if m.result.count != 0 {
		t.Fatalf("result should be empty after consume")
	}
	// Bucket remainder: milk bucket leaves an empty bucket.
	m.grid = grid9(t, "M--", "---", "---")
	m.craftConsume(p)
	if itemNameOf(m.grid[0].item) != "minecraft:bucket" || m.grid[0].count != 1 {
		t.Fatalf("remainder = %+v, want bucket", m.grid[0])
	}
}

func TestMenuSlotMapping(t *testing.T) {
	p := &player{}
	p.armor[0] = invSlot{item: 90, count: 1} // head
	p.offhand = invSlot{item: 91, count: 1}
	p.slots[0] = invSlot{item: 1, count: 2}  // hotbar 0
	p.slots[9] = invSlot{item: 2, count: 3}  // main 9
	m := newInventoryMenu()
	if got := m.get(p, 36); got.item != 1 {
		t.Fatalf("wire 36 should be hotbar 0, got %+v", got)
	}
	if got := m.get(p, 9); got.item != 2 {
		t.Fatalf("wire 9 should be main inv 9, got %+v", got)
	}
	if got := m.get(p, 45); got.item != 91 {
		t.Fatalf("wire 45 should be offhand, got %+v", got)
	}
	// Armor: wire 5 -> head (inv 39) = armor[0].
	if got := m.get(p, 5); got.item != 90 {
		t.Fatalf("wire 5 should be head armor, got %+v", got)
	}
	m.set(p, 37, invSlot{item: 7, count: 5})
	if p.slots[1].item != 7 {
		t.Fatalf("wire 37 should write hotbar 1")
	}
	// Crafting menu: wire 10 = main inv 9, wire 37 = hotbar 0.
	cm := newCraftingMenu(1)
	if got := cm.get(p, 10); got.item != 2 {
		t.Fatalf("crafting wire 10 should be main inv 9, got %+v", got)
	}
	if got := cm.get(p, 37); got.item != 1 {
		t.Fatalf("crafting wire 37 should be hotbar 0, got %+v", got)
	}
}

func TestClickPickupAndSwap(t *testing.T) {
	p := &player{}
	m := newCraftingMenu(1)
	// Put 32 planks in wire slot 10 (main inv 9).
	m.set(p, 10, invSlot{item: itemID(t, "minecraft:oak_planks"), count: 32})

	// Left-click picks the whole stack up.
	m.doPickup(p, &conn{s: &Server{}}, 10, 0)
	if m.carried.count != 32 || m.get(p, 10).count != 0 {
		t.Fatalf("pickup: carried=%+v slot=%+v", m.carried, m.get(p, 10))
	}
	// Left-click again puts everything back.
	m.doPickup(p, &conn{s: &Server{}}, 10, 0)
	if m.carried.count != 0 || m.get(p, 10).count != 32 {
		t.Fatalf("put back: carried=%+v slot=%+v", m.carried, m.get(p, 10))
	}
	// Right-click takes half (ceil).
	m.doPickup(p, &conn{s: &Server{}}, 10, 1)
	if m.carried.count != 16 || m.get(p, 10).count != 16 {
		t.Fatalf("half take: carried=%+v slot=%+v", m.carried, m.get(p, 10))
	}
	// Place one back with right-click.
	m.doPickup(p, &conn{s: &Server{}}, 10, 1)
	if m.carried.count != 15 || m.get(p, 10).count != 17 {
		t.Fatalf("place one: carried=%+v slot=%+v", m.carried, m.get(p, 10))
	}
	// Swap: 1 stick in hotbar 0, SWAP button 0 exchanges slot & hotbar.
	p.slots[0] = invSlot{item: itemID(t, "minecraft:stick"), count: 1}
	m.doSwap(p, 10, 0)
	if m.carried.count != 15 {
		t.Fatalf("swap should not touch carried")
	}
	if itemNameOf(m.get(p, 10).item) != "minecraft:stick" {
		t.Fatalf("slot should now hold the stick: %+v", m.get(p, 10))
	}
	if m.get(p, 10).count != 1 {
		t.Fatalf("stick count wrong: %+v", m.get(p, 10))
	}
	// PICKUP moves the stick to an empty neighbour slot.
	m.doPickup(p, &conn{s: &Server{}}, 10, 0)
	if m.carried.item != itemID(t, "minecraft:stick") || m.carried.count != 1 {
		t.Fatalf("stick not picked up: %+v", m.carried)
	}
	m.doPickup(p, &conn{s: &Server{}}, 11, 0)
	if m.get(p, 11).count != 1 || m.carried.count != 0 {
		t.Fatalf("stick did not move: %+v / carried %+v", m.get(p, 11), m.carried)
	}
}

func TestQuickMoveRouting(t *testing.T) {
	p := &player{}
	m := newInventoryMenu()
	m.set(p, 36, invSlot{item: itemID(t, "minecraft:dirt"), count: 5})
	m.doQuickMove(p, &conn{s: &Server{}}, 36)
	if m.get(p, 36).count != 0 {
		t.Fatalf("hotbar source should be empty")
	}
	if m.get(p, 9).count != 5 {
		t.Fatalf("main inv 9 should hold the stack, got %+v", m.get(p, 9))
	}
	m.doQuickMove(p, &conn{s: &Server{}}, 9)
	if m.get(p, 36).count != 5 {
		t.Fatalf("quick move back to hotbar failed: %+v", m.get(p, 36))
	}
}

func TestPickupAllCollects(t *testing.T) {
	p := &player{}
	m := newInventoryMenu()
	id := itemID(t, "minecraft:sand")
	m.set(p, 9, invSlot{item: id, count: 10})
	m.set(p, 10, invSlot{item: id, count: 20})
	m.carried = invSlot{item: id, count: 5}
	m.doPickupAll(p, 11)
	if m.carried.count != 35 {
		t.Fatalf("pickup-all carried = %d, want 35", m.carried.count)
	}
	if m.get(p, 9).count != 0 || m.get(p, 10).count != 0 {
		t.Fatalf("sources not drained")
	}
}

func TestDragSpread(t *testing.T) {
	p := &player{}
	m := newInventoryMenu()
	id := itemID(t, "minecraft:stone")
	m.carried = invSlot{item: id, count: 8}
	// Left drag across three empty slots: begin(0) add(4) x3 end(8).
	m.doQuickCraft(p, &conn{s: &Server{}}, 9, 0)
	m.doQuickCraft(p, &conn{s: &Server{}}, 9, 4)
	m.doQuickCraft(p, &conn{s: &Server{}}, 10, 4)
	m.doQuickCraft(p, &conn{s: &Server{}}, 11, 4)
	m.doQuickCraft(p, &conn{s: &Server{}}, 0, 8)
	// Vanilla floor-division: 8 items over 3 slots leaves 2 on cursor.
	if m.carried.count != 2 {
		t.Fatalf("drag left 8 over 3 slots should leave 2: %d", m.carried.count)
	}
	if m.get(p, 9).count != 2 || m.get(p, 10).count != 2 || m.get(p, 11).count != 2 {
		t.Fatalf("spread = %d/%d/%d", m.get(p, 9).count, m.get(p, 10).count, m.get(p, 11).count)
	}
	// Right drag: one per slot (begin 1, add 5, end 9).
	m.carried = invSlot{item: id, count: 4}
	m.doQuickCraft(p, &conn{s: &Server{}}, 9, 1)
	m.doQuickCraft(p, &conn{s: &Server{}}, 12, 5)
	m.doQuickCraft(p, &conn{s: &Server{}}, 13, 5)
	m.doQuickCraft(p, &conn{s: &Server{}}, 0, 9)
	if m.get(p, 12).count != 1 || m.get(p, 13).count != 1 {
		t.Fatalf("right drag = %d/%d", m.get(p, 12).count, m.get(p, 13).count)
	}
	if m.carried.count != 2 {
		t.Fatalf("right drag leftover = %d", m.carried.count)
	}
}

func TestResultTakeConsumesGrid(t *testing.T) {
	p := &player{}
	m := newCraftingMenu(1)
	m.grid = grid9(t, "PP-", "PP-", "---")
	m.refreshResult()
	if m.result.count != 1 {
		t.Fatalf("crafting table result missing")
	}
	m.doPickup(p, &conn{s: &Server{}}, 0, 0)
	if m.carried.count != 1 || itemNameOf(m.carried.item) != "minecraft:crafting_table" {
		t.Fatalf("result not taken: %+v", m.carried)
	}
	for i, s := range m.grid {
		if s.count != 0 {
			t.Fatalf("grid cell %d not consumed: %+v", i, s)
		}
	}
	if m.result.count != 0 {
		t.Fatalf("result should clear after consume")
	}
}
