package java

import "github.com/masgzy/gopherite/protocol"

// Network text components. Since 1.20.3 disconnect reasons (login,
// configuration and play phases) carry an NBT-encoded component document
// instead of a JSON string; sending JSON makes the client fail with
// "Failed to decode packet '...disconnect'".
//
// The wire form is the vanilla NBT tag encoding (tagCodec path in
// ByteBufCodecs): a rootless compound (0x0A, no root name) whose
// TAG_String fields carry an unsigned-short length — NOT the
// VarInt-length "Minecraft string" used by packet fields.

// nbtString writes a TAG_String payload: u16 byte length + UTF-8.
func nbtString(w *protocol.Writer, s string) {
	w.Uint16(uint16(len(s)))
	w.Raw([]byte(s))
}

// WriteTranslateComponent encodes {"translate": key} as a rootless
// network-NBT compound (0x0A root type, no root name).
func WriteTranslateComponent(w *protocol.Writer, key string) {
	w.Byte(0x0A) // TAG_Compound root, network format omits the root name
	w.Byte(0x08) // TAG_String field...
	nbtString(w, "translate")
	nbtString(w, key)
	w.Byte(0x00) // TAG_End
}

// WriteTextComponent encodes {"text": value} as a rootless network-NBT
// compound (configuration/play disconnects, boss bar titles, system chat).
func WriteTextComponent(w *protocol.Writer, text string) {
	w.Byte(0x0A)
	w.Byte(0x08)
	nbtString(w, "text")
	nbtString(w, text)
	w.Byte(0x00)
}

// WriteJsonComponent encodes a component as a VarInt-length string
// holding compact JSON — the login-phase disconnect wire form
// (ClientboundLoginDisconnectPacket uses lenientJson, not the NBT tag
// codec).
func WriteJsonComponent(w *protocol.Writer, json string) {
	w.String(json)
}

// nbtCompoundEntryString writes one TAG_String compound entry.
func nbtCompoundEntryString(w *protocol.Writer, name, value string) {
	w.Byte(0x08)
	nbtString(w, name)
	nbtString(w, value)
}

// WriteAdvancementAnnouncement encodes the vanilla advancement chat
// announcement (AdvancementType.createAnnouncement + Advancement.decorateName):
// {"translate": "chat.type.advancement.<frame>", "with": [
//
//	{"text": player},
//	{"text": "[", "color": c, "extra": [
//	   {"translate": title, "color": c, "hoverEvent": {"action": "show_text",
//	    "value": {"translate": title, "color": c, "extra": [{"text": "\n"},
//	    {"translate": desc}]}}},
//	   {"text": "]"}], "color": c}]}
//
// frame is task/goal/challenge; colors mirror AdvancementType
// (green / dark_purple / green).
func WriteAdvancementAnnouncement(w *protocol.Writer, player, titleKey, descKey, frame string) {
	color := "green"
	switch frame {
	case "challenge":
		color = "dark_purple"
	case "goal":
		color = "green"
	}
	w.Byte(0x0A) // root compound
	nbtCompoundEntryString(w, "translate", "chat.type.advancement."+frame)
	// "with": TAG_List of 2 compounds.
	w.Byte(0x09)
	nbtString(w, "with")
	w.Byte(0x0A) // element type: compound
	w.Uint16(2)
	// arg 0: player display name.
	w.Byte(0x0A)
	nbtCompoundEntryString(w, "text", player)
	w.Byte(0x00)
	// arg 1: bracketed advancement name with hover tooltip.
	w.Byte(0x0A)
	nbtCompoundEntryString(w, "text", "[")
	nbtCompoundEntryString(w, "color", color)
	// "extra": [title+hover, "]"].
	w.Byte(0x09)
	nbtString(w, "extra")
	w.Byte(0x0A) // element type: compound
	w.Uint16(2)
	// title component.
	w.Byte(0x0A)
	nbtCompoundEntryString(w, "translate", titleKey)
	nbtCompoundEntryString(w, "color", color)
	// "hoverEvent": {"action": "show_text", "value": tooltip}.
	w.Byte(0x0A)
	nbtString(w, "hoverEvent")
	nbtCompoundEntryString(w, "action", "show_text")
	// "value": tooltip compound.
	w.Byte(0x0A)
	nbtString(w, "value")
	nbtCompoundEntryString(w, "translate", titleKey)
	nbtCompoundEntryString(w, "color", color)
	// tooltip "extra": [{"text": "\n"}, {"translate": desc}].
	w.Byte(0x09)
	nbtString(w, "extra")
	w.Byte(0x0A)
	w.Uint16(2)
	w.Byte(0x0A)
	nbtCompoundEntryString(w, "text", "\n")
	w.Byte(0x00)
	w.Byte(0x0A)
	nbtCompoundEntryString(w, "translate", descKey)
	w.Byte(0x00)
	w.Byte(0x00) // end tooltip compound ("value")
	w.Byte(0x00) // end hoverEvent compound
	w.Byte(0x00) // end title compound
	// closing bracket sibling.
	w.Byte(0x0A)
	nbtCompoundEntryString(w, "text", "]")
	w.Byte(0x00)
	w.Byte(0x00) // end bracket compound
	w.Byte(0x00) // end root compound
}
