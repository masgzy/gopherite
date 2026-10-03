package java

import "github.com/masgzy/gopherite/protocol"

// M16 world border packets, verified against the 26.2 decompiled
// ClientboundInitializeBorderPacket / ClientboundSetBorder*Packet
// STREAM_CODECs. All fields are plain doubles / VarLongs / VarInts —
// no optionals, no components.

// WritePlayInitializeBorder emits 0x2B INITIALIZE_BORDER, the full-state
// snapshot sent on join (PlayerList.sendLevelInfo order: center x/z,
// current size, lerp target, lerp duration in ticks, absolute max size,
// warning blocks, warning time).
func WritePlayInitializeBorder(w *protocol.Writer, centerX, centerZ, oldSize, newSize float64, lerpTime int64, absoluteMaxSize int32, warningBlocks, warningTime int32) {
	w.Double(centerX)
	w.Double(centerZ)
	w.Double(oldSize)
	w.Double(newSize)
	w.VarLong(lerpTime)
	w.VarInt(absoluteMaxSize)
	w.VarInt(warningBlocks)
	w.VarInt(warningTime)
}

// WritePlaySetBorderCenter emits 0x58 SET_BORDER_CENTER.
func WritePlaySetBorderCenter(w *protocol.Writer, centerX, centerZ float64) {
	w.Double(centerX)
	w.Double(centerZ)
}

// WritePlaySetBorderSize emits 0x5A SET_BORDER_SIZE (immediate resize;
// the vanilla packet carries getLerpTarget, i.e. the final size).
func WritePlaySetBorderSize(w *protocol.Writer, size float64) {
	w.Double(size)
}

// WritePlaySetBorderLerpSize emits 0x59 SET_BORDER_LERP_SIZE: the client
// interpolates from oldSize to newSize over lerpTime ticks on its own.
func WritePlaySetBorderLerpSize(w *protocol.Writer, oldSize, newSize float64, lerpTime int64) {
	w.Double(oldSize)
	w.Double(newSize)
	w.VarLong(lerpTime)
}

// WritePlaySetBorderWarningDelay emits 0x5B SET_BORDER_WARNING_DELAY
// (warning time in seconds shown when the border is moving towards the
// player).
func WritePlaySetBorderWarningDelay(w *protocol.Writer, warningTime int32) {
	w.VarInt(warningTime)
}

// WritePlaySetBorderWarningDistance emits 0x5C
// SET_BORDER_WARNING_DISTANCE (red screen distance in blocks).
func WritePlaySetBorderWarningDistance(w *protocol.Writer, warningBlocks int32) {
	w.VarInt(warningBlocks)
}
