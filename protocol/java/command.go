package java

// Declare Commands (0x10) and command suggestions (0x0F) for 26.2.
// The 26.2 argument nodes carry the parser as a VarInt into the
// minecraft:command_argument_type registry (was: identifier string).
// Parser property payloads per type mirror ArgumentTypeInfos.

import "github.com/masgzy/gopherite/protocol"

// CommandNodeData is one node of the flattened command tree.
type CommandNodeData struct {
	Flags    byte // bit0 literal / bit1 argument / bit2 executable / bit4 suggestion
	Children []int32
	Redirect int32 // -1 = none
	Name     string
	ParserID int32
	Props    []byte // parser-specific property payload
	Suggest  string // suggestion provider id ("" = none)
}

// Declare Commands flags.
const (
	NodeFlagLiteral    = 0x01
	NodeFlagArgument   = 0x02
	NodeFlagExecutable = 0x04
	NodeFlagRedirect   = 0x08
	NodeFlagSuggest    = 0x10
)

// Parser registry ids (minecraft:command_argument_type, 26.2).
const (
	ParserBool       = 0  // brigadier:bool
	ParserFloat      = 1  // brigadier:float
	ParserDouble     = 2  // brigadier:double
	ParserInteger    = 3  // brigadier:integer
	ParserString     = 5  // brigadier:string
	ParserEntity     = 6  // minecraft:entity
	ParserBlockPos   = 8  // minecraft:block_pos
	ParserVec3       = 10 // minecraft:vec3
	ParserVec2       = 11 // minecraft:vec2
	ParserBlockState = 12 // minecraft:block_state
	ParserItemStack  = 14 // minecraft:item_stack
	ParserMessage    = 20 // minecraft:message
	ParserGamemode   = 42 // minecraft:gamemode
	ParserTime       = 43 // minecraft:time
	ParserUUID       = 56 // minecraft:uuid
)

// WriteDeclareCommands encodes the flattened node list + root index.
func WriteDeclareCommands(w *protocol.Writer, nodes []CommandNodeData, root int32) {
	w.VarInt(int32(len(nodes)))
	for i := range nodes {
		n := &nodes[i]
		w.Byte(n.Flags)
		w.VarInt(int32(len(n.Children)))
		for _, ch := range n.Children {
			w.VarInt(ch)
		}
		if n.Flags&NodeFlagRedirect != 0 {
			w.VarInt(n.Redirect)
		}
		switch n.Flags & 3 {
		case NodeFlagLiteral:
			w.String(n.Name)
		case NodeFlagArgument:
			w.String(n.Name)
			w.VarInt(n.ParserID)
			w.FixedBytes(n.Props)
			if n.Flags&NodeFlagSuggest != 0 {
				w.String(n.Suggest)
			}
		}
	}
	w.VarInt(root)
}

// WriteCommandSuggestions answers a tab-complete request: transaction id,
// replacement range (start, length) and matches with optional tooltips.
func WriteCommandSuggestions(w *protocol.Writer, id, start, length int32, matches []string) {
	w.VarInt(id)
	w.VarInt(start)
	w.VarInt(length)
	w.VarInt(int32(len(matches)))
	for _, m := range matches {
		w.String(m)
		w.Bool(false) // no tooltip
	}
}

// ReadCommandSuggestion decodes a tab-complete request: transaction id
// plus the command text being typed (no cursor offset on the wire —
// the client sends the full text; assume the cursor is at the end).
func ReadCommandSuggestion(r *protocol.Reader) (int32, string, error) {
	id, err := r.VarInt()
	if err != nil {
		return 0, "", err
	}
	text, err := r.String(256)
	return id, text, err
}

// Parser property helpers (ArgumentTypeInfo payloads).
//
// Ground truth is the decompiled 26.2 serializers:
//
//      bool/float/double/integer/long  flag byte + optional bounds
//      string                          VarInt enum (word/phrase/greedy)
//      entity                          flag byte (bit0 single, bit1 players)
//      time                            raw int32 minimum (no flag byte!)
//      singleton (message/block_pos/vec3/block_state/item_stack/gamemode/
//                uuid/game_profile/...)        empty payload

// StringProps encodes brigadier:string properties: 0 single word,
// 1 quotable phrase, 2 greedy phrase (a VarInt; values 0-2 are one byte).
func StringProps(mode int32) []byte {
	return []byte{byte(mode)}
}

// EntityProps encodes minecraft:entity properties: bit0 single only,
// bit1 players only.
func EntityProps(single, playersOnly bool) []byte {
	var b byte
	if single {
		b |= 1
	}
	if playersOnly {
		b |= 2
	}
	return []byte{b}
}

// IntegerProps encodes brigadier:integer bounds (flags 0 = unbounded).
func IntegerProps() []byte { return []byte{0} }

// FloatProps/DoubleProps encode brigadier:float/double properties:
// flags byte with the optional min/max bits unset (any value).
func FloatProps() []byte  { return []byte{0} }
func DoubleProps() []byte { return []byte{0} }

// TimeProps encodes minecraft:time properties: a raw big-endian int32
// minimum tick count (0 = any). The 26.2 TimeArgument.Info writes the
// int directly with no flag byte — emitting a flag byte here shifted
// every later node by 3 bytes and crashed real clients with
// "VarIntArray with size 115 is bigger than allowed 26" while decoding
// the command tree.
func TimeProps() []byte { return []byte{0, 0, 0, 0} }
