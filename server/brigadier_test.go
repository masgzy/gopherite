package server

// Brigadier-lite dispatcher tests: parse resolution, argument
// validation, greedy strings and suggestion ranges.

import (
	"testing"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
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

// TestDeclareCommandsVanillaDecode replays the 26.2 vanilla client's
// ClientboundCommandsPacket decode path field by field. Any payload
// misalignment (a wrong parser property length, say) surfaces as a
// children count exceeding the remaining bytes, dangling references or
// trailing garbage — the exact crash real clients hit before the time
// parser payload was fixed ("VarIntArray with size 115 is bigger than
// allowed 26").
func TestDeclareCommandsVanillaDecode(t *testing.T) {
	nodes, rootIdx := flatten(getCommandRoot())
	w := protocol.NewWriter()
	java.WriteDeclareCommands(w, nodes, rootIdx)

	r := protocol.NewReader(nil)
	r.Reset(w.Bytes())
	count, err := r.VarInt()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != int32(len(nodes)) {
		t.Fatalf("node count %d, want %d", count, len(nodes))
	}

	// 26.2 command_argument_type registry ids and their property size
	// behaviour, per the decompiled ArgumentTypeInfos serializers.
	singleton := map[int32]bool{
		java.ParserBool: true, java.ParserBlockPos: true, java.ParserVec3: true,
		java.ParserBlockState: true, java.ParserItemStack: true,
		java.ParserMessage: true, java.ParserGamemode: true, java.ParserUUID: true,
	}

	for i := int32(0); i < count; i++ {
		flags, err := r.Byte()
		if err != nil {
			t.Fatalf("node %d flags: %v", i, err)
		}
		nChildren, err := r.VarInt()
		if err != nil {
			t.Fatalf("node %d children count: %v", i, err)
		}
		if int(nChildren) > r.Remaining() {
			t.Fatalf("node %d children %d exceeds remaining %d bytes (misaligned)", i, nChildren, r.Remaining())
		}
		kids := make([]int32, nChildren)
		for j := int32(0); j < nChildren; j++ {
			c, err := r.VarInt()
			if err != nil {
				t.Fatalf("node %d child %d: %v", i, j, err)
			}
			if c < 0 || c >= count {
				t.Fatalf("node %d child %d out of range", i, c)
			}
			kids[j] = c
		}
		if flags&java.NodeFlagRedirect != 0 {
			rd, err := r.VarInt()
			if err != nil {
				t.Fatalf("node %d redirect: %v", i, err)
			}
			if rd < 0 || rd >= count {
				t.Fatalf("node %d redirect %d out of range", i, rd)
			}
		}
		switch flags & 3 {
		case 0: // root: nothing
		case 1: // literal
			if _, err := r.String(32767); err != nil {
				t.Fatalf("node %d literal name: %v", i, err)
			}
		case 2: // argument
			if _, err := r.String(32767); err != nil {
				t.Fatalf("node %d argument name: %v", i, err)
			}
			id, err := r.VarInt()
			if err != nil {
				t.Fatalf("node %d parser id: %v", i, err)
			}
			switch {
			case singleton[id]:
				// zero payload
			case id == java.ParserString:
				if _, err := r.VarInt(); err != nil {
					t.Fatalf("node %d string props: %v", i, err)
				}
			case id == java.ParserInteger:
				b, _ := r.Byte()
				if b&1 != 0 {
					if _, err := r.Int32(); err != nil {
						t.Fatalf("node %d integer min: %v", i, err)
					}
				}
				if b&2 != 0 {
					if _, err := r.Int32(); err != nil {
						t.Fatalf("node %d integer max: %v", i, err)
					}
				}
			case id == java.ParserEntity:
				if _, err := r.Byte(); err != nil {
					t.Fatalf("node %d entity props: %v", i, err)
				}
			case id == java.ParserFloat, id == java.ParserDouble:
				// flag byte 0 = unbounded（M16 worldborder 数值参数）。
				if _, err := r.Byte(); err != nil {
					t.Fatalf("node %d float props: %v", i, err)
				}
			case id == java.ParserVec2:
				// singleton，无属性负载（M16 worldborder center）。
			case id == java.ParserTime:
				// 26.2: a raw int32 minimum, no flag byte.
				if _, err := r.Int32(); err != nil {
					t.Fatalf("node %d time props: %v", i, err)
				}
			default:
				t.Fatalf("node %d uses unknown parser id %d", i, id)
			}
			if flags&java.NodeFlagSuggest != 0 {
				if _, err := r.String(32767); err != nil {
					t.Fatalf("node %d suggestion id: %v", i, err)
				}
			}
		default:
			t.Fatalf("node %d unknown type flags %d", i, flags&3)
		}
	}
	// 客户端强转规则（26.2 ClientboundCommandsPacket.getRoot）：全树
	// 必须恰有一个类型 0 节点，且 entries[rootIdx] 就是它。旧实现把
	// 零值根 cmdNode 打成 argument（0x02），本测试因 byte 对齐而全绿，
	// 真实客户端却在 getRoot 处 ClassCastException——此断言即回归防线。
	rootSeen := -1
	for i, n := range nodes {
		typ := n.Flags & 3
		if typ == 0 {
			if rootSeen != -1 {
				t.Fatalf("node %d is a second root node", i)
			}
			rootSeen = i
			if n.Flags&^(java.NodeFlagRedirect|java.NodeFlagExecutable) != 0 {
				t.Fatalf("root node %d carries unexpected flags 0x%x", i, n.Flags)
			}
		}
	}
	if rootSeen == -1 {
		t.Fatal("no root node in the tree")
	}
	if int32(rootSeen) != rootIdx {
		t.Fatalf("root node at index %d, but root index is %d", rootSeen, rootIdx)
	}

	root, err := r.VarInt()
	if err != nil {
		t.Fatalf("root index: %v", err)
	}
	if root != rootIdx {
		t.Fatalf("root index %d, want %d", root, rootIdx)
	}
	if left := r.Remaining(); left != 0 {
		t.Fatalf("%d trailing bytes after the tree (misalignment)", left)
	}
}
