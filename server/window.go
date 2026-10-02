package server

// Container menus (M7). Every player owns a persistent inventory menu
// (container id 0, vanilla InventoryMenu); right-clicking a crafting
// table opens a transient CraftingMenu with a fresh window id.
//
// Wire slot layouts mirror the 26.2 vanilla menu classes:
//
//	InventoryMenu (46 slots): 0 result, 1-4 2x2 grid, 5-8 armor
//	  (inv 39..36 = head/chest/legs/feet), 9-35 main (inv 9..35),
//	  36-44 hotbar (inv 0..8), 45 offhand (inv 40).
//	CraftingMenu (46 slots): 0 result, 1-9 3x3 grid, 10-36 main
//	  (inv 9..35), 37-45 hotbar (inv 0..8).

import (
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// menuKind discriminates the two menus implemented so far.
type menuKind int

const (
	menuInventory menuKind = iota
	menuCrafting
)

// menu is the server-side state of one open container.
type menu struct {
	kind    menuKind
	id      int32
	stateID int32
	carried invSlot // cursor stack

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

func (m *menu) slotCount() int { return 46 }

// get resolves one wire slot to its current stack.
func (m *menu) get(p *player, i int) invSlot {
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

// isResultSlot reports whether wire slot i is a crafting output.
func (m *menu) isResultSlot(i int) bool { return i == 0 }

// wireStack converts an invSlot into the protocol stack shape.
func wireStack(s invSlot) java.ItemStack {
	return java.ItemStack{ID: s.item, Count: s.count}
}

func wireSlot(s java.ItemStack) invSlot {
	if s.Count <= 0 || s.ID <= 0 {
		return invSlot{}
	}
	return invSlot{item: s.ID, count: s.Count}
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

// closeMenu returns grid + carried contents to the inventory (dropping
// the overflow, vanilla clearContainer), notifies the client and falls
// back to the inventory menu. Caller holds Server.mu.
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
