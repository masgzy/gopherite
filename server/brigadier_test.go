package server

// Brigadier-lite dispatcher tests: parse resolution, argument
// validation, greedy strings and suggestion ranges.

import (
	"testing"
)

func TestTryParseLiterals(t *testing.T) {
	root := getCommandRoot()
	if pc := tryParse(root, "tps"); pc == nil {
		t.Fatal("tps should parse")
	}
	if pc := tryParse(root, "tpsbar"); pc == nil {
		t.Fatal("tpsbar should parse")
	}
	if pc := tryParse(root, "tpsx"); pc != nil {
		t.Fatal("tpsx must not parse")
	}
	if pc := tryParse(root, "tps extra"); pc != nil {
		t.Fatal("tps with trailing token must not parse")
	}
}

func TestTryParseArgs(t *testing.T) {
	root := getCommandRoot()
	pc := tryParse(root, "give Steve minecraft:stone 5")
	if pc == nil {
		t.Fatal("give with count should parse")
	}
	if pc.args["target"] != "Steve" || pc.args["item"] != "minecraft:stone" || pc.args["count"] != "5" {
		t.Fatalf("args = %+v", pc.args)
	}
	pc = tryParse(root, "give Steve stone")
	if pc == nil || pc.args["item"] != "stone" {
		t.Fatalf("short form item should parse: %+v", pc)
	}
	pc = tryParse(root, "give Steve notanitem 1")
	if pc != nil {
		t.Fatal("unknown item must fail validation")
	}
	pc = tryParse(root, "gamemode creative")
	if pc == nil || pc.args["mode"] != "creative" {
		t.Fatalf("gamemode: %+v", pc)
	}
	pc = tryParse(root, "gamemode spectator")
	if pc == nil {
		t.Fatal("spectator should parse")
	}
	pc = tryParse(root, "gamemode hard")
	if pc != nil {
		t.Fatal("invalid gamemode must fail")
	}
}

func TestTryParseGreedy(t *testing.T) {
	root := getCommandRoot()
	pc := tryParse(root, "say hello world with spaces")
	if pc == nil {
		t.Fatal("say should parse")
	}
	if pc.args["message"] != "hello world with spaces" {
		t.Fatalf("greedy message = %q", pc.args["message"])
	}
	pc = tryParse(root, `say "quoted string" end`)
	if pc == nil || pc.args["message"] != "quoted string end" {
		t.Fatalf("quote handling: %q", pc.args["message"])
	}
}

func TestTryParseVecAndPos(t *testing.T) {
	root := getCommandRoot()
	pc := tryParse(root, "tp 100.5 -60 ~5")
	if pc == nil {
		t.Fatal("tp relative vec should parse")
	}
	pc = tryParse(root, "tp 100")
	if pc == nil || pc.args["destination"] != "100" {
		// one token resolves as the entity (destination) branch
		t.Fatalf("tp 100 should resolve via entity branch: %+v", pc)
	}
	pc = tryParse(root, "setblock 0 -61 0 minecraft:stone")
	if pc == nil || pc.args["block"] != "minecraft:stone" {
		t.Fatalf("setblock: %+v", pc)
	}
	pc = tryParse(root, "setblock 0 -61 0 minecraft:nope")
	if pc != nil {
		t.Fatal("unknown block must fail")
	}
}

func TestSuggestions(t *testing.T) {
	// The client sends the text after the slash; empty means "just typed /".
	start, length, matches := suggestionsFor("")
	if start != 0 || length != 0 {
		t.Fatalf("empty command: start=%d length=%d", start, length)
	}
	if len(matches) == 0 {
		t.Fatal("no literal suggestions at root")
	}

	start, length, matches = suggestionsFor("g")
	if start != 0 || length != 1 {
		t.Fatalf("prefix range: start=%d length=%d", start, length)
	}
	found := map[string]bool{}
	for _, m := range matches {
		found[m] = true
	}
	if !found["give"] {
		t.Fatalf("prefix g should suggest give: %v", matches)
	}

	_, _, matches = suggestionsFor("gamemode ")
	if len(matches) != 4 {
		t.Fatalf("gamemode values: %v", matches)
	}
	start, _, matches = suggestionsFor("give Steve ston")
	if len(matches) == 0 {
		t.Fatal("item suggestions missing")
	}
	found = map[string]bool{}
	for _, m := range matches {
		found[m] = true
	}
	if !found["stone"] && !found["minecraft:stone"] {
		t.Fatalf("stone not suggested: %v", matches)
	}
	_ = start
}

func TestSuggestionPacketShape(t *testing.T) {
	start, length, matches := suggestionsFor("g")
	if len(matches) == 0 {
		t.Fatal("need matches")
	}
	_ = start
	_ = length
}
