package server

// Integrity checks over the generated recipe table (recipes_gen.go):
// every ingredient expression must resolve against the item registry,
// every result must be a known item, and a handful of hand-picked
// vanilla recipes must round-trip with their expected fields.

import (
	"testing"
)

func TestRecipeTableIntegrity(t *testing.T) {
	if len(recipeDefs) != 1585 {
		t.Fatalf("recipe count = %d, want 1585 (26.2 vanilla)", len(recipeDefs))
	}
	names := map[string]bool{}
	for i := range recipeDefs {
		r := &recipeDefs[i]
		if r.Name == "" || r.Kind == "" {
			t.Fatalf("recipe[%d] missing name/kind", i)
		}
		if names[r.Name] {
			t.Fatalf("duplicate recipe name %s", r.Name)
		}
		names[r.Name] = true
		if r.Result != "" {
			if _, ok := itemIDByName[r.Result]; !ok {
				t.Fatalf("%s: unknown result item %s", r.Name, r.Result)
			}
		}
		for _, expr := range r.ingredientExprs() {
			for _, alt := range splitAlternatives(expr) {
				ids := ingItemIDs(alt)
				if len(ids) == 0 {
					t.Fatalf("%s: ingredient %q resolves to no items", r.Name, alt)
				}
			}
		}
	}
}

func splitAlternatives(expr string) []string {
	out := []string{expr}
	// tags and single items pass through unchanged; '|'-joined
	// alternatives are validated one by one
	if len(expr) > 0 && expr[0] != '#' {
		out = nil
		start := 0
		for i := 0; i <= len(expr); i++ {
			if i == len(expr) || expr[i] == '|' {
				out = append(out, expr[start:i])
				start = i + 1
			}
		}
	}
	return out
}

func TestTagResolution(t *testing.T) {
	planks := tagMembers("minecraft:planks")
	if len(planks) != 12 {
		t.Fatalf("planks tag has %d members, want 12", len(planks))
	}
	ids := ingItemIDs("#minecraft:planks")
	if len(ids) != 12 {
		t.Fatalf("ingItemIDs(#minecraft:planks) = %d ids, want 12", len(ids))
	}
	if got := ingItemIDs("minecraft:stone"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("ingItemIDs(minecraft:stone) = %v, want [1]", got)
	}
	// alternatives: oak or birch logs
	ids = ingItemIDs("minecraft:oak_log|minecraft:birch_log")
	if len(ids) != 2 {
		t.Fatalf("alternatives resolved to %d ids, want 2", len(ids))
	}
}

func TestKnownRecipes(t *testing.T) {
	// 2x2 planks -> crafting_table
	r := recipeByName("minecraft:crafting_table")
	if r == nil {
		t.Fatal("crafting_table recipe missing")
	}
	if r.Kind != "minecraft:crafting_shaped" ||
		len(r.Pattern) != 2 || r.Pattern[0] != "##" || r.Pattern[1] != "##" {
		t.Fatalf("crafting_table shape wrong: %+v", r)
	}
	if len(r.Keys) != 1 || r.Keys[0].Sym != '#' ||
		r.Keys[0].Ingr != "#minecraft:planks" {
		t.Fatalf("crafting_table key wrong: %+v", r.Keys)
	}
	if r.Result != "minecraft:crafting_table" || r.resultCount() != 1 {
		t.Fatalf("crafting_table result wrong: %+v", r)
	}

	// one acacia_log tag -> 4 planks
	r = recipeByName("minecraft:acacia_planks")
	if r == nil || r.Kind != "minecraft:crafting_shapeless" ||
		len(r.Ingredients) != 1 ||
		r.Ingredients[0] != "#minecraft:acacia_logs" ||
		r.Result != "minecraft:acacia_planks" || r.Count != 4 {
		t.Fatalf("acacia_planks recipe wrong: %+v", r)
	}

	// smelting: raw_iron -> iron_ingot, 0.7 xp
	r = recipeByName("minecraft:iron_ingot_from_smelting_raw_iron")
	if r == nil || r.Kind != "minecraft:smelting" ||
		len(r.Slots) != 1 || r.Slots[0].Role != "ingredient" ||
		r.Slots[0].Ingr != "minecraft:raw_iron" ||
		r.Result != "minecraft:iron_ingot" || r.Exp != 0.7 {
		t.Fatalf("raw iron smelting wrong: %+v", r)
	}

	// shaped: 2x2 stone -> 4 stone bricks
	r = recipeByName("minecraft:stone_bricks")
	if r == nil || r.Kind != "minecraft:crafting_shaped" ||
		r.Result != "minecraft:stone_bricks" || r.resultCount() != 4 {
		t.Fatalf("stone bricks shaped recipe wrong: %+v", r)
	}

	// stonecutting variant: 1 stone -> 1 stone brick slab? no —
	// stone -> 2 stone brick slabs via the *_stonecutting recipe
	r = recipeByName("minecraft:stone_brick_slab_from_stone_stonecutting")
	if r == nil || r.Kind != "minecraft:stonecutting" ||
		len(r.Slots) != 1 || r.Slots[0].Role != "ingredient" ||
		r.Slots[0].Ingr != "minecraft:stone" ||
		r.Result != "minecraft:stone_brick_slab" {
		t.Fatalf("stone brick slab stonecutting wrong: %+v", r)
	}
}

func TestRecipeIndexes(t *testing.T) {
	if got := len(recipesOfKind("minecraft:smelting")); got != 73 {
		t.Fatalf("smelting recipes = %d, want 73", got)
	}
	if got := len(recipesOfKind("minecraft:stonecutting")); got != 319 {
		t.Fatalf("stonecutting recipes = %d, want 319", got)
	}
	found := false
	for _, r := range recipesForResult("minecraft:crafting_table") {
		if r.Name == "minecraft:crafting_table" {
			found = true
		}
	}
	if !found {
		t.Fatal("recipesForResult(crafting_table) missed the shaped recipe")
	}
	if recipeByName("minecraft:no_such_recipe") != nil {
		t.Fatal("unknown recipe should return nil")
	}
}
