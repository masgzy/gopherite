package server

import (
	"log"
	"math"
	"strings"
	"sync"

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
	return p.giveItemPotion(itemID, count, 0)
}

// giveItemPotion is the component-aware variant: stacks merge only when
// both the item AND the potion_contents match (M14; tipped arrows and
// potions never mix with plain stacks, mirroring vanilla component
// equality). Caller holds Server.mu.
func (p *player) giveItemPotion(itemID, count, potion int32) int32 {
	// Merge pass.
	for i := range p.slots {
		s := p.slots[i]
		if s.item == itemID && s.potion == potion && s.count > 0 && s.count < itemMaxStack {
			room := itemMaxStack - s.count
			take := count
			if take > room {
				take = room
			}
			s.count += take
			count -= take
			p.slots[i] = s
			p.conn.sendSlot(int32(i), s)
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
			s := invSlot{item: itemID, count: take, potion: potion}
			p.slots[i] = s
			count -= take
			p.conn.sendSlot(int32(i), s)
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
	java.WriteSetPlayerInventoryPotion(body, slot, s.item, s.count, s.potion)
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
	// A hotbar swap interrupts an in-progress eat.
	c.cancelEating()
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

// facingFromYaw maps the player look yaw to the horizontal facing a
// placed container block gets: vanilla blocks face the player, i.e. the
// opposite of the look direction (yaw 0 = looking south).
func facingFromYaw(yaw float32) string {
	seg := int(math.Floor(float64(yaw)/90 + 0.5))
	switch ((seg % 4) + 4) % 4 {
	case 1:
		return "east" // looking west
	case 2:
		return "south" // looking north
	case 3:
		return "west" // looking east
	default:
		return "north" // looking south
	}
}

// itemNamesByID is the reverse of itemIDByName, built lazily once.
var (
	nameOnce      sync.Once
	itemNamesByID map[int32]string
)

func itemNameOf(id int32) string {
	nameOnce.Do(func() {
		itemNamesByID = make(map[int32]string, len(itemIDByName))
		for name, i := range itemIDByName {
			itemNamesByID[i] = name
		}
	})
	return itemNamesByID[id]
}

// placeBlock resolves the held block item, computes the face-adjacent
// position and fills it when the target is air. The held stack is
// consumed (survival); right-clicking a crafting table opens its menu
// instead unless the player sneaks. Client predictions settle via the
// BlockChangedAck plus the authoritative Block Update.
func (c *conn) placeBlock(u java.ServerboundUseItemOn) {
	p := c.player
	if p == nil || p.heldSlot < 0 || p.heldSlot >= int32(len(p.slots)) {
		return
	}
	// Interact blocks first: a crafting table opens its 3x3 grid menu,
	// chests and furnaces open their block-entity menus and a lever
	// flips (unless the player sneaks to place against it).
	if !p.sneaking {
		state := c.s.world.getBlock(int(u.X), int(u.Y), int(u.Z))
		name := blockNameOf(int(state))
		switch name {
		case "minecraft:crafting_table":
			c.s.mu.Lock()
			c.openCrafting()
			c.s.mu.Unlock()
			return
		case "minecraft:chest":
			c.s.mu.Lock()
			b := c.s.ensureBlockEntity(beChest, int(u.X), int(u.Y), int(u.Z))
			c.openChest(b)
			c.s.mu.Unlock()
			return
		case "minecraft:furnace":
			c.s.mu.Lock()
			b := c.s.ensureBlockEntity(beFurnace, int(u.X), int(u.Y), int(u.Z))
			c.openFurnace(b)
			c.s.mu.Unlock()
			return
		case "minecraft:brewing_stand":
			c.s.mu.Lock()
			b := c.s.ensureBlockEntity(beBrewing, int(u.X), int(u.Y), int(u.Z))
			c.openBrewing(b)
			c.s.mu.Unlock()
			return
		case "minecraft:lever":
			c.s.mu.Lock()
			c.s.toggleLever(int(u.X), int(u.Y), int(u.Z))
			c.s.mu.Unlock()
			return
		case "minecraft:tnt":
			// M12: 打火石点燃 TNT（不潜行）；其他物品落入下方放置流程。
			if p.heldSlot >= 0 && p.heldSlot < int32(len(p.slots)) &&
				itemNameOf(p.slots[p.heldSlot].item) == "minecraft:flint_and_steel" {
				c.s.mu.Lock()
				c.s.igniteTNTLocked(int(u.X), int(u.Y), int(u.Z), p.id, 0)
				c.s.mu.Unlock()
				return
			}
		}
		if strings.HasSuffix(name, "_bed") {
			// M12: 床交互（不潜行时）——睡觉或提示。
			c.s.mu.Lock()
			c.interactBed(int(u.X), int(u.Y), int(u.Z))
			c.s.mu.Unlock()
			return
		}
	}
	held := p.slots[p.heldSlot]
	if held.count <= 0 {
		return
	}
	block, ok := itemBlockName[itemNameOf(held.item)]
	if !ok {
		return
	}
	// M12: 床放置走两格路径（foot 在目标格，head 在玩家朝向方向）。
	if strings.HasSuffix(block, "_bed") {
		c.placeBedAndConsume(block, u, held)
		return
	}
	state := defaultStateOf(block)
	if state < 0 {
		return
	}
	// Containers face the player; vanilla property sets must match
	// exactly for the state lookup.
	isContainer := block == "minecraft:chest" || block == "minecraft:furnace" || block == "minecraft:brewing_stand"
	if isContainer {
		props := blockPropsOf(state)
		props["facing"] = facingFromYaw(p.yaw)
		if block == "minecraft:chest" {
			props["type"] = "single"
		}
		if sid := stateIDOf(block, props); sid > 0 {
			state = int(sid)
		}
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
	if isContainer {
		c.s.mu.Lock()
		switch block {
		case "minecraft:chest":
			c.s.ensureBlockEntity(beChest, x, y, z)
		case "minecraft:brewing_stand":
			c.s.ensureBlockEntity(beBrewing, x, y, z)
		default:
			c.s.ensureBlockEntity(beFurnace, x, y, z)
		}
		c.s.mu.Unlock()
	}
	held.count--
	p.slots[p.heldSlot] = held
	c.sendSlot(p.heldSlot, held)
	c.s.broadcastBlockUpdate(int32(x), int32(y), int32(z), int32(state))
	c.s.mu.Lock()
	c.s.redstoneUpdate(x, y, z)
	// M17: placed_block 触发。
	c.s.advEventPlace(p, block)
	c.s.mu.Unlock()
	log.Printf(ui.Success("OK ")+"%s 放置了 %s (%d, %d, %d)", p.name, block, x, y, z)
}

// spawnPlayerDrop drops item entities at the player (cursor drops,
// container overflow, inventory throws). Caller holds Server.mu.
func (s *Server) spawnPlayerDrop(p *player, itemID, count int32) {
	s.spawnPlayerDropPotion(p, itemID, count, 0)
}

// spawnPlayerDropPotion is the M14 component-aware drop: potion > 0
// spawns a stack carrying its potion_contents (tipped arrows and thrown
// potions never lose their component mid-air). Caller holds Server.mu.
func (s *Server) spawnPlayerDropPotion(p *player, itemID, count, potion int32) {
	if itemID <= 0 || count <= 0 {
		return
	}
	for count > 0 {
		take := count
		if take > itemMaxStack {
			take = itemMaxStack
		}
		var e *itemEntity
		if potion > 0 {
			e = newItemEntityWithPotion(s.allocEntityID(), p.x, p.y+0.5, p.z, itemID, take, potion)
		} else {
			e = newItemEntity(s.allocEntityID(), p.x, p.y+0.5, p.z, itemID, take)
		}
		s.spawnEntity(e)
		count -= take
	}
}
