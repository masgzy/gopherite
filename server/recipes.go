package server

// Runtime view over the generated vanilla recipe table (recipes_gen.go).
//
// M6 only ships the data pipeline plus lookup helpers; crafting grid
// matching and the recipe book arrive with M7. Ingredient expressions
// use the generator's spelling: "minecraft:item", "#minecraft:tag" or
// '|'-joined alternatives in item/tag spelling.

import (
	"sort"
	"strings"
	"sync"
)

// symBinding binds one shaped-pattern symbol to an ingredient expr.
type symBinding struct {
	Sym  byte
	Ingr string
}

// slot is one named ingredient slot ("ingredient", "base", "template",
// "addition", "input", "material", ...) of a non-shaped recipe.
type slot struct {
	Role string
	Ingr string
}

// recipeDef mirrors one vanilla datapack recipe file.
type recipeDef struct {
	Name     string   // "minecraft:crafting_table"
	Kind     string   // serializer, e.g. "minecraft:crafting_shaped"
	Group    string   // recipe-book group ("" when absent)
	Category string   // recipe-book category ("" when absent)
	Pattern  []string // crafting_shaped rows
	Keys     []symBinding
	// Ingredients holds the crafting_shapeless ingredient list.
	Ingredients []string
	// Slots holds every named ingredient slot of the remaining kinds.
	Slots      []slot
	Result     string // "" for recipes without a fixed result (smithing_trim, repairitem)
	Count      int32  // 0 means 1
	Components string // raw result component JSON when the vanilla file has some
	Exp        float32
	CookTime   int32 // ticks; 0 when the vanilla file omits it
}

func (r *recipeDef) resultCount() int32 {
	if r.Count == 0 {
		return 1
	}
	return r.Count
}

// ingredientExprs lists the ingredient exprs of a recipe in a stable
// order: shaped key bindings (sorted by symbol), then shapeless, then
// named slots.
func (r *recipeDef) ingredientExprs() []string {
	var out []string
	for _, k := range r.Keys {
		out = append(out, k.Ingr)
	}
	out = append(out, r.Ingredients...)
	for _, s := range r.Slots {
		out = append(out, s.Ingr)
	}
	return out
}

var (
	recipeOnce  sync.Once
	byName      map[string]*recipeDef
	byResult    map[string][]*recipeDef
	byKind      map[string][]*recipeDef
	tagItemName map[string][]string
)

func buildRecipeIndexes() {
	recipeOnce.Do(func() {
		byName = make(map[string]*recipeDef, len(recipeDefs))
		byResult = make(map[string][]*recipeDef)
		byKind = make(map[string][]*recipeDef)
		for i := range recipeDefs {
			r := &recipeDefs[i]
			byName[r.Name] = r
			if r.Result != "" {
				byResult[r.Result] = append(byResult[r.Result], r)
			}
			byKind[r.Kind] = append(byKind[r.Kind], r)
		}
		tagItemName = make(map[string][]string, len(itemTags))
		for tag, items := range itemTags {
			names := append([]string(nil), items...)
			sort.Strings(names)
			tagItemName[tag] = names
		}
	})
}

// recipeByName returns the recipe with the given identifier, or nil.
func recipeByName(name string) *recipeDef {
	buildRecipeIndexes()
	return byName[name]
}

// recipesForResult lists every recipe producing the given item.
func recipesForResult(item string) []*recipeDef {
	buildRecipeIndexes()
	return byResult[item]
}

// recipesOfKind lists every recipe of the given serializer kind.
func recipesOfKind(kind string) []*recipeDef {
	buildRecipeIndexes()
	return byKind[kind]
}

// tagMembers returns the sorted item identifiers of a resolved tag.
func tagMembers(tag string) []string {
	buildRecipeIndexes()
	return tagItemName[tag]
}

// ingItemIDs resolves one ingredient expression to the item registry ids
// it accepts. Unknown items or tags yield nil (callers treat that as
// "matches nothing"); the test suite asserts the generated table never
// hits that path.
func ingItemIDs(expr string) []int32 {
	var out []int32
	for _, alt := range strings.Split(expr, "|") {
		if strings.HasPrefix(alt, "#") {
			for _, name := range tagMembers(alt[1:]) {
				if id, ok := itemIDByName[name]; ok {
					out = append(out, id)
				}
			}
			continue
		}
		if id, ok := itemIDByName[alt]; ok {
			out = append(out, id)
		}
	}
	return out
}
