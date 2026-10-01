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
