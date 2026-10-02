package java

import (
	"github.com/masgzy/gopherite/protocol"
)

// Entity packet encoders for the M5 entity system. All ids and field
// orders follow the 26.2 (protocol 776) packets report; see the add_entity
// and set_entity_data entries there.

// WriteAddEntity encodes minecraft:add_entity. Velocity components are in
// units of 1/8000 block per tick; data is the per-type auxiliary int.
func WriteAddEntity(w *protocol.Writer, entityID int32, uuid [16]byte, typeID int32,
	x, y, z float64, pitch, yaw, headYaw byte, data int32, vx, vy, vz int16) {
	w.VarInt(entityID)
	w.UUID(uuid)
	w.VarInt(typeID)
	w.Double(x)
	w.Double(y)
	w.Double(z)
	w.Byte(pitch)
	w.Byte(yaw)
	w.Byte(headYaw)
	w.VarInt(data)
	w.Int16(vx)
	w.Int16(vy)
	w.Int16(vz)
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

// WriteTeleportEntity encodes minecraft:teleport_entity (full position,
// used when a move would overflow the 1/4096 delta shorts).
func WriteTeleportEntity(w *protocol.Writer, entityID int32, x, y, z float64,
	vx, vy, vz float64, yaw, pitch byte, onGround bool) {
	w.VarInt(entityID)
	w.Double(x)
	w.Double(y)
	w.Double(z)
	w.Double(vx)
	w.Double(vy)
	w.Double(vz)
	w.Byte(yaw)
	w.Byte(pitch)
	w.Bool(onGround)
}

// WriteSetEntityDataItem encodes minecraft:set_entity_data carrying the
// single ItemEntity payload: metadata index 8, ItemStack type, the stack,
// then the 0xFF terminator. The item id uses the baked holder form
// (registry index + 1), matching the inventory slot encoding.
func WriteSetEntityDataItem(w *protocol.Writer, entityID int32, itemID, count int32) {
	w.VarInt(entityID)
	w.Byte(8)   // ItemEntity.DATA_ITEM_STACK
	w.VarInt(7) // EntityDataType: item_stack
	if count <= 0 {
		w.VarInt(0) // empty stack
	} else {
		w.VarInt(count)
		w.VarInt(itemID + 1)
		w.VarInt(0) // component additions
		w.VarInt(0) // component removals
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
