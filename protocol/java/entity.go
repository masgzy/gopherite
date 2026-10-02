package java

import (
	"github.com/masgzy/gopherite/protocol"
)

// Entity packet encoders for the M5 entity system. All ids and field
// orders follow the 26.2 (protocol 776) packets report; see the add_entity
// and set_entity_data entries there.

// WriteAddEntity encodes minecraft:add_entity in the 26.2 field order:
// id, uuid, type, position, low-precision velocity, pitch/yaw/head-yaw
// angle bytes, then the per-type auxiliary int. Velocity is in blocks per
// tick and goes through the LpVec3 codec (a zero vector is one byte).
func WriteAddEntity(w *protocol.Writer, entityID int32, uuid [16]byte, typeID int32,
	x, y, z float64, vx, vy, vz float64, pitch, yaw, headYaw byte, data int32) {
	w.VarInt(entityID)
	w.UUID(uuid)
	w.VarInt(typeID)
	w.Double(x)
	w.Double(y)
	w.Double(z)
	WriteLpVec3(w, vx, vy, vz)
	w.Byte(pitch)
	w.Byte(yaw)
	w.Byte(headYaw)
	w.VarInt(data)
}

// WriteRemoveEntities encodes minecraft:remove_entities.
func WriteRemoveEntities(w *protocol.Writer, ids []int32) {
	w.VarInt(int32(len(ids)))
	for _, id := range ids {
		w.VarInt(id)
	}
}

// WriteMoveEntityPos encodes minecraft:move_entity_pos; deltas are in
// units of 1/4096 block and must fit in an int16 (|delta| < 8 blocks).
func WriteMoveEntityPos(w *protocol.Writer, entityID int32, dx, dy, dz int16, onGround bool) {
	w.VarInt(entityID)
	w.Int16(dx)
	w.Int16(dy)
	w.Int16(dz)
	w.Bool(onGround)
}

// WriteTeleportEntity encodes minecraft:teleport_entity in the 26.2 form:
// id, PositionMoveRotation (position, low-precision velocity, yaw/pitch
// floats), the relative-axes bitmask (fixed int32) and the ground flag.
func WriteTeleportEntity(w *protocol.Writer, entityID int32, x, y, z float64,
	vx, vy, vz float64, yaw, pitch float32, relatives int32, onGround bool) {
	w.VarInt(entityID)
	w.Double(x)
	w.Double(y)
	w.Double(z)
	WriteLpVec3(w, vx, vy, vz)
	w.Float(yaw)
	w.Float(pitch)
	w.Int32(relatives)
	w.Bool(onGround)
}

// WriteEntityPositionSync encodes minecraft:entity_position_sync: like
// teleport_entity without the relative mask. Used to re-sync an entity
// whose accumulated move deltas would overflow.
func WriteEntityPositionSync(w *protocol.Writer, entityID int32, x, y, z float64,
	vx, vy, vz float64, yaw, pitch float32, onGround bool) {
	w.VarInt(entityID)
	w.Double(x)
	w.Double(y)
	w.Double(z)
	WriteLpVec3(w, vx, vy, vz)
	w.Float(yaw)
	w.Float(pitch)
	w.Bool(onGround)
}

// WriteMoveEntityPosRot encodes minecraft:move_entity_pos_rot: the same
// delta shorts as move_entity_pos plus absolute yaw/pitch angle bytes.
func WriteMoveEntityPosRot(w *protocol.Writer, entityID int32, dx, dy, dz int16,
	yaw, pitch byte, onGround bool) {
	w.VarInt(entityID)
	w.Int16(dx)
	w.Int16(dy)
	w.Int16(dz)
	w.Byte(yaw)
	w.Byte(pitch)
	w.Bool(onGround)
}

// WriteRotateHead encodes minecraft:rotate_head (head yaw is independent
// of body yaw for mobs).
func WriteRotateHead(w *protocol.Writer, entityID int32, headYaw byte) {
	w.VarInt(entityID)
	w.Byte(headYaw)
}

// WriteSetEntityMotion encodes minecraft:set_entity_motion with the
// low-precision velocity codec.
func WriteSetEntityMotion(w *protocol.Writer, entityID int32, vx, vy, vz float64) {
	w.VarInt(entityID)
	WriteLpVec3(w, vx, vy, vz)
}

// AttributeSnapshot is one tracked attribute for update_attributes; the
// 26.2 wire form carries the registry id, the base value and the modifier
// list (always empty in gopherite).
type AttributeSnapshot struct {
	ID   int32
	Base float64
}

// WriteUpdateAttributes encodes minecraft:update_attributes.
func WriteUpdateAttributes(w *protocol.Writer, entityID int32, attrs []AttributeSnapshot) {
	w.VarInt(entityID)
	w.VarInt(int32(len(attrs)))
	for _, a := range attrs {
		w.VarInt(a.ID)
		w.Double(a.Base)
		w.VarInt(0) // modifiers
	}
}

// WriteSetEntityDataItem encodes minecraft:set_entity_data carrying the
// single ItemEntity payload: metadata index 8, ItemStack type, the stack,
// then the 0xFF terminator. The item id uses the baked holder form
// (registry index + 1), matching the inventory slot encoding. Potion
// forwards the M13 potion_contents component (0 = none).
func WriteSetEntityDataItem(w *protocol.Writer, entityID int32, itemID, count, potion int32) {
	w.VarInt(entityID)
	w.Byte(8)   // ItemEntity.DATA_ITEM_STACK
	w.VarInt(7) // EntityDataType: item_stack
	if count <= 0 {
		w.VarInt(0) // empty stack
	} else {
		w.VarInt(count)
		w.VarInt(itemID + 1)
		writeComponentPatch(w, potion)
	}
	w.Byte(0xFF) // end of metadata list
}

// WriteTakeItemEntity encodes minecraft:take_item_entity: the item being
// collected, the collector, and the stack size taken.
func WriteTakeItemEntity(w *protocol.Writer, itemEntityID, collectorID, amount int32) {
	w.VarInt(itemEntityID)
	w.VarInt(collectorID)
	w.VarInt(amount)
}
