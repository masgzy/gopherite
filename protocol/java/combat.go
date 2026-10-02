package java

import (
	"github.com/masgzy/gopherite/protocol"
)

// M8 combat packet encoders/decoders: damage feedback, hurt animation,
// entity events (death/fall-over), arm swing animation and the 26.2
// serverbound attack/interact/use packets.

// WriteDamageEvent encodes minecraft:damage_event. Entity ids are sent in
// the shifted optional form (0 = absent, otherwise id+1); sourcePosition
// is optional and unused by gopherite.
func WriteDamageEvent(w *protocol.Writer, entityID, damageTypeID, causeID, directID int32) {
	w.VarInt(entityID)
	w.VarInt(damageTypeID)
	if causeID < 0 {
		w.VarInt(0)
	} else {
		w.VarInt(causeID + 1)
	}
	if directID < 0 {
		w.VarInt(0)
	} else {
		w.VarInt(directID + 1)
	}
	w.Bool(false) // optional source position: absent
}

// WriteHurtAnimation encodes minecraft:hurt_animation (the tilt direction
// the victim's model flashes towards).
func WriteHurtAnimation(w *protocol.Writer, entityID int32, yaw float32) {
	w.VarInt(entityID)
	w.Float(yaw)
}

// WriteEntityEvent encodes minecraft:entity_event. The entity id is a
// FIXED int32 in every protocol version — not a VarInt. Event 3 is the
// living-entity death animation.
func WriteEntityEvent(w *protocol.Writer, entityID int32, event byte) {
	w.Int32(entityID)
	w.Byte(event)
}

// Clientbound animate actions (26.2 ClientboundAnimatePacket constants).
const (
	AnimateSwingMainHand = 0
	AnimateWakeUp        = 2
	AnimateSwingOffHand  = 3
	AnimateCriticalHit   = 4
	AnimateMagicCritical = 5
)

// WriteAnimate encodes minecraft:animate (entity action animation).
func WriteAnimate(w *protocol.Writer, entityID int32, action byte) {
	w.VarInt(entityID)
	w.Byte(action)
}

// --- serverbound combat packets -------------------------------------------

// ReadAttack decodes the 26.2 dedicated attack packet: a single VarInt
// entity id (attack is no longer an Interact variant).
func ReadAttack(r *protocol.Reader) (int32, error) {
	return r.VarInt()
}

// ReadInteract decodes the reworked 26.2 interact packet: entity id, hand,
// a low-precision hit location and the secondary-action flag. Gopherite
// consumes the location purely to keep the stream in sync.
func ReadInteract(r *protocol.Reader) (entityID, hand int32, usingSecondaryAction bool, err error) {
	if entityID, err = r.VarInt(); err != nil {
		return
	}
	if hand, err = r.VarInt(); err != nil {
		return
	}
	if _, _, _, err = ReadLpVec3(r); err != nil {
		return
	}
	usingSecondaryAction, err = r.Bool()
	return
}

// ReadClientCommand decodes the client command packet (VarInt enum):
// 0 = perform respawn, 1 = request stats, 2 = request gamerule values.
func ReadClientCommand(r *protocol.Reader) (int32, error) {
	return r.VarInt()
}

// Serverbound player command actions (26.2 enum order).
const (
	PlayerCommandStopSleeping    = 0
	PlayerCommandStartSprinting  = 1
	PlayerCommandStopSprinting   = 2
	PlayerCommandStartRidingJump = 3
	PlayerCommandStopRidingJump  = 4
	PlayerCommandOpenInventory   = 5
	PlayerCommandStartFallFlying = 6
)

// ReadPlayerCommand decodes the player command packet: entity id, action
// enum and a data int — all consumed so the stream stays aligned.
func ReadPlayerCommand(r *protocol.Reader) (entityID, action, data int32, err error) {
	if entityID, err = r.VarInt(); err != nil {
		return
	}
	if action, err = r.VarInt(); err != nil {
		return
	}
	data, err = r.VarInt()
	return
}

// ReadUseItem decodes the use-item packet (right click in air): hand,
// sequence, then the look angles.
func ReadUseItem(r *protocol.Reader) (hand, sequence int32, yaw, pitch float32, err error) {
	if hand, err = r.VarInt(); err != nil {
		return
	}
	if sequence, err = r.VarInt(); err != nil {
		return
	}
	if yaw, err = r.Float(); err != nil {
		return
	}
	pitch, err = r.Float()
	return
}
