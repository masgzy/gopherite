package server

// Crafting recipe matching over the M6 generated table. Semantics follow
// the 26.2 vanilla classes: ShapedRecipePattern (bounding-box trim +
// horizontal mirror), ShapelessRecipe (ingredient-count gate + multiset
// match) and CraftingInput.ofPositioned (trim).

import (
	"sync"
)

// ingCell is one shaped pattern cell: the item ids it accepts.
// A cell with an empty id set only matches an empty grid cell.
type ingCell struct {
	ids []int32
}

// craftingRecipe is one resolved crafting recipe ready for grid matching.
type craftingRecipe struct {
	name   string
	shaped bool

	// shaped fields: trimmed pattern (w x h), cells row-major, and
	// whether the pattern equals its own mirror (vanilla skips the
	// mirrored test for those).
	w, h   int
	cells  []ingCell
	symRes bool

	// shapeless field: one ingredient set per listed ingredient.
	shapeless []ingCell

	resultID   int32
	resultName string
	count      int32
}

var (
	craftOnce  sync.Once
	craftables []craftingRecipe
)

// craftingRemainder maps items that leave a container behind
// (Item.getCraftingRemainder).
var craftingRemainder = map[string]string{
	"minecraft:milk_bucket":          "minecraft:bucket",
	"minecraft:water_bucket":         "minecraft:bucket",
	"minecraft:lava_bucket":          "minecraft:bucket",
	"minecraft:powder_snow_bucket":   "minecraft:bucket",
	"minecraft:cod_bucket":           "minecraft:bucket",
	"minecraft:salmon_bucket":        "minecraft:bucket",
	"minecraft:tropical_fish_bucket": "minecraft:bucket",
	"minecraft:axolotl_bucket":       "minecraft:bucket",
	"minecraft:tadpole_bucket":       "minecraft:bucket",
	"minecraft:pufferfish_bucket":    "minecraft:bucket",
	"minecraft:honey_bottle":         "minecraft:glass_bottle",
	"minecraft:dragon_breath":        "minecraft:glass_bottle",
}

var remainderByID = map[int32]int32{}

// buildCraftingRecipes resolves the generated recipe table into
// grid-matchable recipes. Runs once, lazily, under Server-free locking.
func buildCraftingRecipes() {
	craftOnce.Do(func() {
		for _, def := range recipeByKindList("crafting") {
			itemID, ok := itemIDByName[def.Result]
			if !ok {
				continue
			}
			r := craftingRecipe{
				name:       def.Name,
				resultName: def.Result,
				resultID:   itemID,
				count:      def.resultCount(),
			}
			switch def.Kind {
			case "minecraft:crafting_shaped":
				r.shaped = true
				rows := def.Pattern
				// Trim empty edges (vanilla ShapedRecipePattern shrink).
				h := len(rows)
				w := 0
				if h > 0 {
					w = len(rows[0])
				}
				top, bottom := 0, h-1
				for top < h && rowEmpty(rows[top]) {
					top++
				}
				for bottom >= top && rowEmpty(rows[bottom]) {
					bottom--
				}
				if top > bottom {
					continue // empty pattern
				}
				rows = rows[top : bottom+1]
				h = len(rows)
				left, right := 0, w-1
				for left < w && colEmpty(rows, left) {
					left++
				}
				for right >= left && colEmpty(rows, right) {
					right--
				}
				if left > right {
					continue
				}
				trimmed := make([]string, h)
				for y, row := range rows {
					trimmed[y] = row[left : right+1]
				}
				w = right - left + 1

				r.w, r.h = w, h
				r.cells = make([]ingCell, w*h)
				bySym := map[byte][]int32{}
				for _, k := range def.Keys {
					bySym[k.Sym] = resolveIngrIDs(k.Ingr)
				}
				nonEmpty := 0
				for y, row := range trimmed {
					for x := 0; x < len(row); x++ {
						ch := row[x]
						if ch == ' ' {
							continue
						}
						ids, ok := bySym[ch]
						if !ok {
							nonEmpty = -1 // unresolvable symbol: drop recipe
							break
						}
						r.cells[x+y*w] = ingCell{ids: ids}
						nonEmpty++
					}
				}
				if nonEmpty <= 0 {
					continue
				}
				// Symmetrical when the pattern equals its own mirror.
				r.symRes = true
				for y := 0; y < h && r.symRes; y++ {
					for x := 0; x < w; x++ {
						if !sameCell(r.cells[x+y*w], r.cells[w-x-1+y*w]) {
							r.symRes = false
							break
						}
					}
				}
			case "minecraft:crafting_shapeless":
				r.shapeless = make([]ingCell, 0, len(def.Ingredients))
				bad := false
				for _, ing := range def.Ingredients {
					ids := resolveIngrIDs(ing)
					if len(ids) == 0 {
						bad = true
						break
					}
					r.shapeless = append(r.shapeless, ingCell{ids: ids})
				}
				if bad {
					continue
				}
			default:
				continue
			}
			craftables = append(craftables, r)
		}
		for item, rem := range craftingRemainder {
			if a, ok := itemIDByName[item]; ok {
				if b, ok2 := itemIDByName[rem]; ok2 {
					remainderByID[a] = b
				}
			}
		}
	})
}

func rowEmpty(row string) bool {
	for _, ch := range row {
		if ch != ' ' {
			return false
		}
	}
	return true
}

func colEmpty(rows []string, x int) bool {
	for _, row := range rows {
		if row[x] != ' ' {
			return false
		}
	}
	return true
}

func sameCell(a, b ingCell) bool {
	if len(a.ids) != len(b.ids) {
		return false
	}
	for i := range a.ids {
		if a.ids[i] != b.ids[i] {
			return false
		}
	}
	return true
}

// recipeByKindList collects the crafting_shaped/shapeless definitions.
func recipeByKindList(prefix string) []*recipeDef {
	buildRecipeIndexes()
	var out []*recipeDef
	for kind, list := range byKind {
		if kind == "minecraft:crafting_shaped" || kind == "minecraft:crafting_shapeless" {
			out = append(out, list...)
		}
		_ = prefix
	}
	return out
}

// resolveIngrIDs flattens one ingredient expression (item / #tag /
// |-alternatives) to its item id set.
func resolveIngrIDs(expr string) []int32 {
	ids := ingItemIDs(expr)
	return ids
}

// gridCell returns the grid stack at (x, y) in the trimmed box.
func gridCell(grid []invSlot, gx, gy, w int) invSlot {
	return grid[gy*3+gx]
}

// matchCraftingResult finds the recipe matching the grid and returns the
// output stack (empty when nothing matches).
func matchCraftingResult(grid []invSlot) invSlot {
	buildCraftingRecipes()
	if len(grid) < 9 {
		return invSlot{}
	}
	// Bounding-box trim (CraftingInput.ofPositioned).
	left, right, top, bottom := 3, -1, 3, -1
	for y := 0; y < 3; y++ {
		for x := 0; x < 3; x++ {
			if grid[y*3+x].count > 0 {
				if x < left {
					left = x
				}
				if x > right {
					right = x
				}
				if y < top {
					top = y
				}
				if y > bottom {
					bottom = y
				}
			}
		}
	}
	if right < left {
		return invSlot{}
	}
	w, h := right-left+1, bottom-top+1
	used := 0
	for _, s := range grid {
		if s.count > 0 {
			used++
		}
	}
	for i := range craftables {
		r := &craftables[i]
		if r.shaped {
			if r.w != w || r.h != h {
				continue
			}
			if matchShaped(grid, r, left, top, false) ||
				(!r.symRes && matchShaped(grid, r, left, top, true)) {
				return invSlot{item: r.resultID, count: r.count}
			}
			continue
		}
		if len(r.shapeless) != used {
			continue
		}
		if matchShapeless(grid, r) {
			return invSlot{item: r.resultID, count: r.count}
		}
	}
	return invSlot{}
}

// matchShaped tests one orientation; xFlip mirrors the recipe pattern.
func matchShaped(grid []invSlot, r *craftingRecipe, left, top int, xFlip bool) bool {
	for y := 0; y < r.h; y++ {
		for x := 0; x < r.w; x++ {
			cell := grid[(top+y)*3+(left+x)]
			want := r.cells[x+y*r.w]
			if xFlip {
				want = r.cells[r.w-x-1+y*r.w]
			}
			if cell.count <= 0 {
				if len(want.ids) != 0 {
					return false
				}
				continue
			}
			if !ingAccepts(want, cell.item) {
				return false
			}
		}
	}
	return true
}

// matchShapeless consumes a copy of the ingredient sets, matching each
// non-empty grid stack against one distinct ingredient.
func matchShapeless(grid []invSlot, r *craftingRecipe) bool {
	remaining := make([]ingCell, len(r.shapeless))
	copy(remaining, r.shapeless)
	for _, s := range grid {
		if s.count <= 0 {
			continue
		}
		found := -1
		for i, ing := range remaining {
			if ingAccepts(ing, s.item) {
				found = i
				break
			}
		}
		if found < 0 {
			return false
		}
		remaining = append(remaining[:found], remaining[found+1:]...)
	}
	return len(remaining) == 0
}

// ingAccepts reports whether a cell/ingredient accepts the item id.
func ingAccepts(c ingCell, item int32) bool {
	if len(c.ids) == 0 {
		return false
	}
	for _, id := range c.ids {
		if id == item {
			return true
		}
	}
	return false
}

// craftConsume applies one craft: decrement every occupied grid cell by
// one and add container remainders (bucket -> empty bucket, ...).
// Caller holds Server.mu.
func (m *menu) craftConsume(p *player) {
	for i, s := range m.grid {
		if s.count <= 0 {
			continue
		}
		s.count--
		if rem, ok := remainderByID[s.item]; ok && s.count == 0 {
			m.grid[i] = invSlot{item: rem, count: 1}
			continue
		}
		m.grid[i] = s
	}
	_ = p
	m.refreshResult()
}
