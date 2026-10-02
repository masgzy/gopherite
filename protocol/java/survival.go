package java

import (
	"github.com/masgzy/gopherite/protocol"
)

// M8 survival packet encoders: health/food sync, experience, the death
// screen packet and the respawn handshake.

// WriteSetHealth encodes minecraft:set_health: health float, food VarInt,
// saturation float.
func WriteSetHealth(w *protocol.Writer, health, saturation float32, food int32) {
	w.Float(health)
	w.VarInt(food)
	w.Float(saturation)
}

// WriteSetExperience encodes minecraft:set_experience: progress float,
// then level and total as VarInts (level BEFORE total in 26.2).
func WriteSetExperience(w *protocol.Writer, progress float32, level, total int32) {
	w.Float(progress)
	w.VarInt(level)
	w.VarInt(total)
}

// WritePlayerCombatKill encodes minecraft:player_combat_kill — the death
// screen packet carrying the victim entity id and the death message.
func WritePlayerCombatKill(w *protocol.Writer, playerID int32, message string) {
	w.VarInt(playerID)
	WriteTextComponent(w, message)
}

// WriteRespawn encodes minecraft:respawn: the common spawn info block
// (identical to the login packet's) plus the dataToKeep bitmask.
func WriteRespawn(w *protocol.Writer, info CommonSpawnInfo, dataToKeep byte) {
	WriteCommonSpawnInfo(w, info)
	w.Byte(dataToKeep)
}
