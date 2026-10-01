package java

import "github.com/masgzy/gopherite/protocol"

// Network text components. Since 1.20.3 disconnect reasons (login,
// configuration and play phases) carry an NBT-encoded component document
// instead of a JSON string; sending JSON makes the client fail with
// "Failed to decode packet '...disconnect'".

// WriteTranslateComponent encodes {"translate": key} as a rootless
// network-NBT compound (0x0A root type, no root name).
func WriteTranslateComponent(w *protocol.Writer, key string) {
	w.Byte(0x0A) // TAG_Compound root, network format omits the root name
	w.Byte(0x08) // TAG_String field...
	w.String("translate")
	w.String(key)
	w.Byte(0x00) // TAG_End
}

// WriteTextComponent encodes {"text": value} as a rootless network-NBT
// compound.
func WriteTextComponent(w *protocol.Writer, text string) {
	w.Byte(0x0A)
	w.Byte(0x08)
	w.String("text")
	w.String(text)
	w.Byte(0x00)
}
