package server

// Container menus (M7). Every player owns a persistent inventory menu
// (container id 0, vanilla InventoryMenu); right-clicking a crafting
// table opens a transient CraftingMenu with a fresh window id. M10 adds
// chest (3x9) and furnace menus backed by block entities.
//
// Wire slot layouts mirror the 26.2 vanilla menu classes:
//
//      InventoryMenu (46 slots): 0 result, 1-4 2x2 grid, 5-8 armor
//        (inv 39..36 = head/chest/legs/feet), 9-35 main (inv 9..35),
//        36-44 hotbar (inv 0..8), 45 offhand (inv 40).
//      CraftingMenu (46 slots): 0 result, 1-9 3x3 grid, 10-36 main
//        (inv 9..35), 37-45 hotbar (inv 0..8).
//      ChestMenu 3 rows (63 slots): 0-26 chest grid, 27-53 main
//        (inv 9..35), 54-62 hotbar (inv 0..8).
//      FurnaceMenu (39 slots): 0 ingredient, 1 fuel, 2 result, 3-29 main
//        (inv 9..35), 30-38 hotbar (inv 0..8).

import (
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// menuKind discriminates the implemented menus.
type menuKind int

const (
	menuInventory menuKind = iota
	menuCrafting
	menuChest
	menuFurnace
	menuBrewing
)

// menuTypeIDs are the vanilla menu registry ids (MenuType register order,
// 26.2 MenuType.java：crafter_3x3 在 7，furnace = 14）。
const (
	menuTypeChest9x3     = 2
	menuTypeBrewingStand = 11
	menuTypeFurnace      = 14
)

// menu is the server-side state of one open container.
type menu struct {
	kind    menuKind
	id      int32
	stateID int32
	carried invSlot // cursor stack

	// be backs chest/furnace menus: the block entity owns the wire
	// slots before the player inventory range.
	be *blockEntity

	// grid holds the crafting cells (4 or 9); result the computed
	// output of slot 0.
	grid   []invSlot
	result invSlot

	// quick-craft (drag) bookkeeping, mirroring AbstractContainerMenu.
	dragStatus int
	dragType   int
	dragSlots  []int
}

func newInventoryMenu() *menu {
	return &menu{kind: menuInventory, id: 0, grid: make([]invSlot, 4)}
}

func newCraftingMenu(id int32) *menu {
	return &menu{kind: menuCrafting, id: id, grid: make([]invSlot, 9)}
}

func newChestMenu(id int32, b *blockEntity) *menu {
	return &menu{kind: menuChest, id: id, be: b}
}

func newFurnaceMenu(id int32, b *blockEntity) *menu {
	return &menu{kind: menuFurnace, id: id, be: b}
}

func newBrewingMenu(id int32, b *blockEntity) *menu {
	return &menu{kind: menuBrewing, id: id, be: b}
}

func (m *menu) slotCount() int {
	switch m.kind {
	case menuChest:
		return 63
	case menuFurnace:
		return 39
	case menuBrewing:
		return 41 // 5 brewing slots + 27 main + 9 hotbar
	default:
		return 46
	}
}

// beSlots resolves the block-entity-backed slot count of the menu (0 for
// crafting menus). Everything before this boundary lives in the block.
func (m *menu) beSlotCount() int {
	switch m.kind {
	case menuChest:
		return 27
	case menuFurnace:
		return 3
	case menuBrewing:
		return 5
	default:
		return 0
	}
}

// get resolves one wire slot to its current stack.
func (m *menu) get(p *player, i int) invSlot {
	if n := m.beSlotCount(); n > 0 {
		switch {
		case m.kind == menuChest && i >= 0 && i < n:
			return m.be.slots[i]
		case m.kind == menuFurnace && i >= 0 && i < n:
			return m.be.slots[i]
		case m.kind == menuBrewing && i >= 0 && i < n:
			return m.be.slots[i]
		case i >= n && i < n+27: // main inventory (inv 9..35)
			return p.slots[i-n+9]
		case i >= n+27 && i < m.slotCount(): // hotbar (inv 0..8)
			return p.slots[i-n-27]
		default:
			return invSlot{}
		}
	}
	switch {
	case i == 0:
		return m.result
	case i < 0 || i >= m.slotCount():
		return invSlot{}
	}
	if m.kind == menuInventory {
		switch {
		case i < 5: // 1-4 grid
			return m.grid[i-1]
		case i < 9: // 5-8 armor: wire 5 = head (inv 39) .. wire 8 = feet
			return p.armor[i-5]
		case i < 36: // 9-35 main
			return p.slots[i]
		case i < 45: // 36-44 hotbar
			return p.slots[i-36]
		default: // 45 offhand
			return p.offhand
		}
	}
	switch {
	case i < 10: // 1-9 grid
		return m.grid[i-1]
	case i < 37: // 10-36 main
		return p.slots[i-1]
	default: // 37-45 hotbar
		return p.slots[i-37]
	}
}

// set stores one wire slot; unknown cells are ignored.
func (m *menu) set(p *player, i int, s invSlot) {
	if n := m.beSlotCount(); n > 0 {
		switch {
		case i >= 0 && i < n:
			m.be.slots[i] = s
		case i >= n && i < n+27:
			p.slots[i-n+9] = s
		case i >= n+27 && i < m.slotCount():
			p.slots[i-n-27] = s
		}
		return
	}
	switch {
	case i == 0:
		m.result = s
		return
	case i < 0 || i >= m.slotCount():
		return
	}
	if m.kind == menuInventory {
		switch {
		case i < 5:
			m.grid[i-1] = s
		case i < 9:
			p.armor[i-5] = s
		case i < 36:
			p.slots[i] = s
		case i < 45:
			p.slots[i-36] = s
		default:
			p.offhand = s
		}
		return
	}
	switch {
	case i < 10:
		m.grid[i-1] = s
	case i < 37:
		p.slots[i-1] = s
	default:
		p.slots[i-37] = s
	}
}

// isResultSlot reports whether wire slot i is a take-only output: the
// crafting results (0) and the furnace result (2). Chests have none.
func (m *menu) isResultSlot(i int) bool {
	switch m.kind {
	case menuFurnace:
		return i == 2
	case menuChest, menuBrewing:
		return false
	default:
		return i == 0
	}
}

// wireStack converts an invSlot into the protocol stack shape.
func wireStack(s invSlot) java.ItemStack {
	return java.ItemStack{ID: s.item, Count: s.count, Potion: s.potion}
}

func wireSlot(s java.ItemStack) invSlot {
	if s.Count <= 0 || s.ID <= 0 {
		return invSlot{}
	}
	return invSlot{item: s.ID, count: s.Count, potion: s.Potion}
}

// refreshResult recomputes slot 0 from the grid after any change.
func (m *menu) refreshResult() {
	m.result = matchCraftingResult(m.grid)
}

// bump advances the window state id (vanilla increments per change).
func (m *menu) bump() int32 {
	m.stateID++
	return m.stateID
}

// sendAll pushes the full window state: Container Set Content followed
// by Set Cursor Item (vanilla sendInitialData/broadcastChanges shape).
func (m *menu) sendAll(c *conn) {
	state := m.bump()
	slots := make([]java.ItemStack, m.slotCount())
	for i := range slots {
		slots[i] = wireStack(m.get(c.player, i))
	}
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayContainerContent)
	java.WriteContainerSetContent(body, m.id, state, slots, wireStack(m.carried))
	_ = c.sendPacket(body.Bytes())
	c.sendCursorItem(m.carried)
}

// sendSlotUpdate pushes one changed cell.
func (m *menu) sendSlotUpdate(c *conn, i int) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayContainerSetSlot)
	java.WriteContainerSetSlot(body, m.id, m.bump(), int32(i), wireStack(m.get(c.player, i)))
	_ = c.sendPacket(body.Bytes())
}

// sendCursorItem pushes the carried stack.
func (c *conn) sendCursorItem(s invSlot) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetCursorItem)
	java.WriteSetCursorItem(body, wireStack(s))
	_ = c.sendPacket(body.Bytes())
}

// openCrafting opens a fresh CraftingMenu for the player and syncs it.
// Caller holds Server.mu.
func (c *conn) openCrafting() {
	p := c.player
	m := newCraftingMenu(p.nextWindowID)
	p.nextWindowID++
	p.openMenu = m
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayOpenScreen)
	java.WriteOpenScreen(body, m.id, 12, "Crafting") // minecraft:crafting menu
	_ = c.sendPacket(body.Bytes())
	m.refreshResult()
	m.sendAll(c)
}

// openChest opens the 3x9 menu of the chest block entity. Caller holds
// Server.mu.
func (c *conn) openChest(b *blockEntity) {
	p := c.player
	m := newChestMenu(p.nextWindowID, b)
	p.nextWindowID++
	p.openMenu = m
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayOpenScreen)
	java.WriteOpenScreen(body, m.id, menuTypeChest9x3, "Chest") // minecraft:generic_9x3
	_ = c.sendPacket(body.Bytes())
	m.sendAll(c)
}

// openFurnace opens the furnace menu and seeds the client's progress
// widgets with the four data slots (vanilla sendAllDataToRemote before
// the initial content). Caller holds Server.mu.
func (c *conn) openFurnace(b *blockEntity) {
	p := c.player
	m := newFurnaceMenu(p.nextWindowID, b)
	p.nextWindowID++
	p.openMenu = m
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayOpenScreen)
	java.WriteOpenScreen(body, m.id, menuTypeFurnace, "Furnace") // minecraft:furnace
	_ = c.sendPacket(body.Bytes())
	lit := b.litTimeRemaining
	for i, v := range [4]int{lit, b.litTotalTime, b.cookingTimer, b.cookingTotalTime} {
		b.lastData[i] = v
		dbody := protocol.NewWriter()
		dbody.VarInt(v776.PacketPlayContainerSetData)
		java.WriteContainerSetData(dbody, m.id, int16(i), int16(v))
		_ = c.sendPacket(dbody.Bytes())
	}
	m.sendAll(c)
}

// closeMenu returns crafting-grid + carried contents to the inventory
// (dropping the overflow, vanilla clearContainer), notifies the client
// and falls back to the inventory menu. Block-entity slots stay in the
// chest/furnace. Caller holds Server.mu.
func (c *conn) closeMenu(windowID int32, notify bool) {
	p := c.player
	m := p.openMenu
	if m == nil || (windowID != 0 && m.id != windowID) {
		return
	}
	for i, s := range m.grid {
		if s.count > 0 {
			if left := p.giveItem(s.item, s.count); left > 0 {
				c.s.spawnPlayerDrop(p, s.item, left)
			}
			m.grid[i] = invSlot{}
		}
	}
	if m.carried.count > 0 {
		if left := p.giveItem(m.carried.item, m.carried.count); left > 0 {
			c.s.spawnPlayerDrop(p, m.carried.item, left)
		}
		m.carried = invSlot{}
	}
	p.openMenu = nil
	if notify {
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlayCBContainerClose)
		java.WriteContainerClose(body, m.id)
		_ = c.sendPacket(body.Bytes())
	}
	// Refresh the (always open) inventory menu to its final state.
	p.invMenu.sendAll(c)
}

// maxStackOf returns the vanilla max stack size for one item; M13 potions
// never stack (1), glass bottles stack in 16s, everything else 64.
func maxStackOf(item int32) int32 {
	switch bareItemName(item) {
	case "potion", "splash_potion", "lingering_potion":
		return 1
	case "glass_bottle":
		return 16
	}
	return itemMaxStack
}

// openBrewing opens the brewing stand menu and seeds the two data slots
// (0 brewTime, 1 fuel). Caller holds Server.mu.
func (c *conn) openBrewing(b *blockEntity) {
	p := c.player
	m := newBrewingMenu(p.nextWindowID, b)
	p.nextWindowID++
	p.openMenu = m
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayOpenScreen)
	java.WriteOpenScreen(body, m.id, menuTypeBrewingStand, "Brewing Stand") // minecraft:brewing_stand
	_ = c.sendPacket(body.Bytes())
	for i, v := range [2]int{int(b.brewTime), int(b.fuel)} {
		b.lastData[i] = v
		dbody := protocol.NewWriter()
		dbody.VarInt(v776.PacketPlayContainerSetData)
		java.WriteContainerSetData(dbody, m.id, int16(i), int16(v))
		_ = c.sendPacket(dbody.Bytes())
	}
	m.sendAll(c)
}
