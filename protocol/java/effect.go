package java

import "github.com/masgzy/gopherite/protocol"

// M13: mob effect sync + living entity shared flags.
//
// 包形状对照 26.2 反编译源（research/decomp_m13）：
//   ClientboundUpdateMobEffectPacket: entity VarInt, effect VarInt
//   (MobEffect.STREAM_CODEC = holderRegistry 直引注册序号), amplifier
//   VarInt, duration VarInt, flags Byte（0x01 ambient / 0x02 粒子可见 /
//   0x04 图标可见 / 0x08 blend）。
//   ClientboundRemoveMobEffectPacket: entity VarInt, effect VarInt。

// Update Mob Effect 的标志位（vanilla FLAG_AMBIENT / FLAG_VISIBLE /
// FLAG_SHOW_ICON / FLAG_BLEND）。
const (
	EffectFlagAmbient   = 0x01
	EffectFlagParticles = 0x02
	EffectFlagIcon      = 0x04
	EffectFlagBlend     = 0x08
)

// WriteUpdateMobEffect encodes one Update Mob Effect packet: add or
// refresh a status effect on an entity.
func WriteUpdateMobEffect(w *protocol.Writer, entityID, effectID, amplifier, duration int32, flags byte) {
	w.VarInt(entityID)
	w.VarInt(effectID)
	w.VarInt(amplifier)
	w.VarInt(duration)
	w.Byte(flags)
}

// WriteRemoveMobEffect encodes one Remove Mob Effect packet.
func WriteRemoveMobEffect(w *protocol.Writer, entityID, effectID int32) {
	w.VarInt(entityID)
	w.VarInt(effectID)
}

// WriteSetEntityDataFlags encodes a Set Entity Data packet carrying the
// living-entity shared flag byte (index 0): 0x20 invisible, 0x40 glowing.
// Used by M13 to mirror invisibility/glowing effects to trackers.
func WriteSetEntityDataFlags(w *protocol.Writer, entityID int32, flags byte) {
	w.VarInt(entityID)
	w.Byte(0)   // shared flag index
	w.VarInt(0) // EntityDataType: byte
	w.Byte(flags)
	w.Byte(0xFF) // end of metadata list
}

// Living entity shared flag bits (vanilla Entity.FLAG_*).
const (
	SharedFlagInvisible = 0x20
	SharedFlagGlowing   = 0x40
)
