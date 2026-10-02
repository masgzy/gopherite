package server

import (
	"log"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// Starter hotbar handed to every player: nine classic superflat-friendly
// block items. Slots are the vanilla hotbar indices 0-8.
var defaultHotbar = []string{
	"minecraft:stone",
	"minecraft:dirt",
	"minecraft:grass_block",
	"minecraft:cobblestone",
	"minecraft:oak_planks",
	"minecraft:oak_log",
	"minecraft:glass",
	"minecraft:sand",
	"minecraft:torch",
}

// sendStarterInventory syncs the hotbar via the 26.2 per-slot inventory
// packet and mirrors it into the server model so pickups can merge.
// Non-block items and unknown names are skipped (empty slot).
func (c *conn) sendStarterInventory() {
	p := c.player
	for slot, item := range defaultHotbar {
		id, ok := itemIDByName[item]
		if !ok {
			continue
		}
		if p != nil {
			p.slots[slot] = invSlot{item: id, count: 64}
		}
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlaySetPlayerInv)
		java.WriteSetPlayerInventory(body, int32(slot), id, 64)
		if err := c.sendPacket(body.Bytes()); err != nil {
			return
		}
	}
}

// giveItem adds a stack to the player inventory: merge into the first
// same-item stack with room, otherwise the first empty slot (0-35).
// Returns the leftover count that did NOT fit; the caller picks up only
// what fits, vanilla-style. Caller holds Server.mu.
func (p *player) giveItem(itemID, count int32) int32 {
	// Merge pass.
	for i := range p.slots {
		if p.slots[i].item == itemID && p.slots[i].count > 0 && p.slots[i].count < itemMaxStack {
			room := itemMaxStack - p.slots[i].count
			take := count
			if take > room {
				take = room
			}
			p.slots[i].count += take
			count -= take
			p.conn.sendSlot(int32(i), p.slots[i])
			if count == 0 {
				return 0
			}
		}
	}
	// Empty-slot pass.
	for i := range p.slots {
		if p.slots[i].count == 0 {
			take := count
			if take > itemMaxStack {
				take = itemMaxStack
			}
			p.slots[i] = invSlot{item: itemID, count: take}
			count -= take
			p.conn.sendSlot(int32(i), p.slots[i])
			if count == 0 {
				return 0
			}
		}
	}
	return count
}

// sendSlot pushes one inventory cell to the client.
func (c *conn) sendSlot(slot int32, s invSlot) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetPlayerInv)
	java.WriteSetPlayerInventory(body, slot, s.item, s.count)
	_ = c.sendPacket(body.Bytes())
}

// handleSetCarriedItem tracks the client's hotbar selection so placement
// resolves the right block.
func (c *conn) handleSetCarriedItem() error {
	slot, err := java.ReadSetCarriedItem(c.rd)
	if err != nil {
		return err
	}
	if p := c.player; p != nil && slot >= 0 && slot < int32(len(defaultHotbar)) {
		p.heldSlot = slot
	}
	return nil
}

// faceOffset maps the vanilla Direction wire order (DOWN, UP, NORTH,
// SOUTH, WEST, EAST) to the adjacent-position step.
var faceOffset = [6][3]int{
	{0, -1, 0}, // down
	{0, 1, 0},  // up
	{0, 0, -1}, // north
	{0, 0, 1},  // south
	{-1, 0, 0}, // west
	{1, 0, 0},  // east
}

// placeBlock resolves the held block item, computes the face-adjacent
// position and fills it when the target is air. Client predictions settle
// via the BlockChangedAck plus the authoritative Block Update.
func (c *conn) placeBlock(u java.ServerboundUseItemOn) {
	p := c.player
	if p == nil || p.heldSlot < 0 || int(p.heldSlot) >= len(defaultHotbar) {
		return
	}
	item := defaultHotbar[p.heldSlot]
	block, ok := itemBlockName[item]
	if !ok {
		return
	}
	state := defaultStateOf(block)
	if state < 0 {
		return
	}
	if u.Face < 0 || int(u.Face) >= len(faceOffset) {
		return
	}
	off := faceOffset[u.Face]
	x, y, z := int(u.X)+off[0], int(u.Y)+off[1], int(u.Z)+off[2]
	if c.s.world.getBlock(x, y, z) != stateAir {
		return
	}
	if !c.s.world.setBlock(x, y, z, int32(state)) {
		return
	}
	c.s.broadcastBlockUpdate(int32(x), int32(y), int32(z), int32(state))
	log.Printf(ui.Success("OK ")+"%s 放置了 %s (%d, %d, %d)", p.name, item, x, y, z)
}
