package server

import "github.com/masgzy/gopherite/protocol/java"

// Container click handling — a faithful port of the 26.2
// AbstractContainerMenu.doClick state machine for the two implemented
// menus. The server is authoritative: after every click the whole window
// is re-synced (Container Set Content), so the client's prediction
// hashes are parsed and discarded.

// ContainerInput wire ids (26.2 ContainerInput enum).
const (
	inputPickup     = 0
	inputQuickMove  = 1
	inputSwap       = 2
	inputClone      = 3
	inputThrow      = 4
	inputQuickCraft = 5
	inputPickupAll  = 6
)

// handleContainerClick applies one click and re-syncs the window.
func (c *conn) handleContainerClick() error {
	click, err := java.ReadContainerClick(c.rd)
	if err != nil {
		return err
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	p := c.player
	if p == nil {
		return nil
	}
	var m *menu
	if click.ContainerID == 0 {
		m = p.invMenu
	} else if p.openMenu != nil && p.openMenu.id == click.ContainerID {
		m = p.openMenu
	} else {
		return nil
	}
	m.doClick(p, c, int(click.Slot), int(click.Button), int(click.Input))
	m.sendAll(c)
	return nil
}

// doClick mutates the menu per the vanilla click rules. It only touches
// the model; syncing happens in handleContainerClick.
func (m *menu) doClick(p *player, c *conn, slot, button, input int) {
	switch input {
	case inputQuickCraft:
		m.doQuickCraft(p, c, slot, button)
	case inputPickup:
		m.doPickup(p, c, slot, button)
	case inputQuickMove:
		m.doQuickMove(p, c, slot)
	case inputSwap:
		if button >= 0 && button < 9 || button == 40 {
			m.doSwap(p, slot, button)
		}
	case inputThrow:
		// Drop from slot with empty cursor: button 0 = one, 1 = stack.
		if m.carried.count == 0 && slot >= 0 {
			s := m.get(p, slot)
			if s.count > 0 {
				amount := int32(1)
				if button == 1 {
					amount = s.count
				}
				take := amount
				if take > s.count {
					take = s.count
				}
				s.count -= take
				m.set(p, slot, s)
				m.afterSlotChange(p, slot)
				c.s.spawnPlayerDrop(p, s.item, take)
			}
		}
	case inputPickupAll:
		m.doPickupAll(p, slot)
	}
}

// afterSlotChange recomputes the crafting result when a grid cell moved.
func (m *menu) afterSlotChange(p *player, slot int) {
	gridTouched := slot == 0
	if m.kind == menuInventory {
		gridTouched = slot >= 1 && slot <= 4
	} else {
		gridTouched = slot >= 1 && slot <= 9
	}
	if gridTouched {
		m.refreshResult()
	}
}

// doPickup implements ContainerInput.PICKUP: place, take, merge, swap,
// result-slot take and drop-through-cursor (-999).
func (m *menu) doPickup(p *player, c *conn, slot, button int) {
	primary := button == 0
	if slot == -999 {
		// Click outside the window: drop carried (all / one).
		if m.carried.count > 0 {
			if primary {
				c.s.spawnPlayerDrop(p, m.carried.item, m.carried.count)
				m.carried = invSlot{}
			} else {
				c.s.spawnPlayerDrop(p, m.carried.item, 1)
				m.carried.count--
			}
		}
		return
	}
	if slot < 0 || slot >= m.slotCount() {
		return
	}
	clicked := m.get(p, slot)
	carried := m.carried

	// Result slot: crafting take (vanilla ResultSlot behaviour).
	if m.isResultSlot(slot) {
		if clicked.count <= 0 || carried.count > 0 && carried.item != clicked.item {
			return
		}
		if carried.count > 0 && !stackFits(carried, clicked) {
			return
		}
		take := clicked
		if carried.count > 0 && carried.count+take.count > itemMaxStack {
			return
		}
		m.craftConsume(p)
		if carried.count == 0 {
			m.carried = take
		} else {
			m.carried.count += take.count
		}
		m.afterSlotChange(p, slot)
		return
	}

	switch {
	case clicked.count == 0:
		if carried.count > 0 {
			amount := carried.count
			if !primary {
				amount = 1
			}
			m.carried = insertStack(m, p, slot, carried, amount)
		}
	case carried.count == 0:
		amount := clicked.count
		if !primary {
			amount = (clicked.count + 1) / 2
		}
		taken := invSlot{item: clicked.item, count: amount}
		clicked.count -= amount
		m.set(p, slot, clicked)
		m.carried = taken
		m.afterSlotChange(p, slot)
	case clicked.item == carried.item:
		if primary {
			// Merge carried into the slot.
			room := itemMaxStack - clicked.count
			move := carried.count
			if move > room {
				move = room
			}
			clicked.count += move
			carried.count -= move
			m.set(p, slot, clicked)
			m.carried = carried
		} else {
			// Move one from carried to slot (vanilla places one even at
			// full? vanilla inserts one; room is required).
			if clicked.count < itemMaxStack {
				clicked.count++
				carried.count--
				m.set(p, slot, clicked)
				m.carried = carried
			}
		}
		m.afterSlotChange(p, slot)
	default:
		// Different items: swap when the slot accepts the carried stack.
		if carried.count <= itemMaxStack {
			m.set(p, slot, carried)
			m.carried = clicked
			m.afterSlotChange(p, slot)
		}
	}
}

// insertStack places up to amount of s into slot i and returns the
// remaining carried stack.
func insertStack(m *menu, p *player, i int, s invSlot, amount int32) invSlot {
	target := m.get(p, i)
	if target.count == 0 {
		if amount > s.count {
			amount = s.count
		}
		m.set(p, i, invSlot{item: s.item, count: amount})
		s.count -= amount
		return s
	}
	if target.item != s.item {
		return s
	}
	room := itemMaxStack - target.count
	if amount > room {
		amount = room
	}
	target.count += amount
	s.count -= amount
	m.set(p, i, target)
	return s
}

// stackFits reports whether the result stack can merge into carried.
func stackFits(carried, result invSlot) bool {
	return carried.count+result.count <= itemMaxStack
}

// doQuickMove implements ContainerInput.QUICK_MOVE (shift-click) with
// vanilla routing per menu; a result-slot click crafts repeatedly while
// the output keeps moving (vanilla's while-loop).
func (m *menu) doQuickMove(p *player, c *conn, slot int) {
	if slot < 0 || slot >= m.slotCount() {
		return
	}
	if m.isResultSlot(slot) {
		// Craft into the inventory while a result exists and fits.
		for it := 0; it < 64; it++ {
			res := m.result
			if res.count == 0 {
				break
			}
			left := p.giveItem(res.item, res.count)
			if left == res.count {
				break // inventory full
			}
			m.craftConsume(p)
			if left > 0 {
				c.s.spawnPlayerDrop(p, res.item, left)
				break
			}
		}
		m.afterSlotChange(p, slot)
		return
	}
	s := m.get(p, slot)
	if s.count == 0 {
		return
	}
	moved := m.quickMove(p, slot, s)
	if moved.count == 0 {
		m.set(p, slot, invSlot{})
	} else {
		m.set(p, slot, moved)
	}
	m.afterSlotChange(p, slot)
}

// quickMove routes one stack per the vanilla quickMoveStack tables and
// returns the part that stayed in the source slot.
func (m *menu) quickMove(p *player, slot int, s invSlot) invSlot {
	// Ranges in wire-slot space: main and hotbar per menu.
	var mainLo, mainHi, hotLo, hotHi int // [lo, hi)
	if m.kind == menuInventory {
		mainLo, mainHi, hotLo, hotHi = 9, 36, 36, 45
	} else {
		mainLo, mainHi, hotLo, hotHi = 10, 37, 37, 46
	}
	switch {
	case slot == 0: // handled by caller
		return s
	case slot >= 1 && slot < (func() int {
		if m.kind == menuInventory {
			return 9
		}
		return 10
	})():
		// Grid cell -> inventory.
		return m.moveRange(p, s, mainLo, mainHi, false)
	case slot >= mainLo && slot < mainHi:
		return m.moveRange(p, s, hotLo, hotHi, false)
	case slot >= hotLo && slot < hotHi:
		return m.moveRange(p, s, mainLo, mainHi, false)
	default:
		return m.moveRange(p, s, mainLo, mainHi, false)
	}
}

// moveRange inserts s into the wire-slot range [lo, hi); hotbar
// preference ("true" in vanilla's moveItemStackTo final param) is used
// for result takes.
func (m *menu) moveRange(p *player, s invSlot, lo, hi int, reverse bool) invSlot {
	// Merge pass then empty pass, like Inventory.add / moveItemStackTo.
	for i := lo; i < hi; i++ {
		t := m.get(p, i)
		if t.count > 0 && t.item == s.item && t.count < itemMaxStack {
			room := itemMaxStack - t.count
			move := s.count
			if move > room {
				move = room
			}
			t.count += move
			s.count -= move
			m.set(p, i, t)
			if s.count == 0 {
				return s
			}
		}
	}
	for i := lo; i < hi; i++ {
		t := m.get(p, i)
		if t.count == 0 {
			place := s.count
			if place > itemMaxStack {
				place = itemMaxStack
			}
			m.set(p, i, invSlot{item: s.item, count: place})
			s.count -= place
			if s.count == 0 {
				return s
			}
		}
	}
	return s
}

// doSwap implements ContainerInput.SWAP: button 0-8 swaps the slot with
// the hotbar stack of that index, 40 swaps with the offhand.
func (m *menu) doSwap(p *player, slot, button int) {
	if slot < 0 || slot >= m.slotCount() || m.isResultSlot(slot) {
		return
	}
	target := m.get(p, slot)
	var stored invSlot
	if button == 40 {
		stored = p.offhand
	} else {
		stored = p.slots[button]
	}
	if stored.count == 0 && target.count == 0 {
		return
	}
	if button == 40 {
		p.offhand = target
	} else {
		p.slots[button] = target
	}
	m.set(p, slot, stored)
	m.afterSlotChange(p, slot)
}

// doPickupAll implements ContainerInput.PICKUP_ALL (double click):
// gather every stack of the carried item into the cursor.
func (m *menu) doPickupAll(p *player, slot int) {
	carried := m.carried
	if carried.count == 0 || carried.count >= itemMaxStack {
		return
	}
	for i := 0; i < m.slotCount() && carried.count < itemMaxStack; i++ {
		if m.isResultSlot(i) || i == slot {
			continue
		}
		t := m.get(p, i)
		if t.count > 0 && t.item == carried.item {
			move := t.count
			if move > itemMaxStack-carried.count {
				move = itemMaxStack - carried.count
			}
			t.count -= move
			carried.count += move
			m.set(p, i, t)
		}
	}
	m.carried = carried
}

// doQuickCraft implements ContainerInput.QUICK_CRAFT (drag painting).
// Button encoding: header = button>>2&3 (0 begin, 1 add, 2 end),
// type = button&3 (0 left, 1 right, 2 middle/clone — ignored in
// survival).
func (m *menu) doQuickCraft(p *player, c *conn, slot, button int) {
	header := (button >> 2) & 3
	dragType := button & 3
	expected := m.dragStatus
	m.dragStatus = header
	if (expected != 1 || m.dragStatus != 2) && expected != m.dragStatus {
		m.resetDrag()
		return
	}
	if m.carried.count == 0 {
		m.resetDrag()
		return
	}
	switch m.dragStatus {
	case 0:
		if dragType == 0 || dragType == 1 {
			m.dragType = dragType
			m.dragStatus = 1
			m.dragSlots = nil
		} else {
			m.resetDrag()
		}
	case 1:
		if slot >= 0 && slot < m.slotCount() && !m.isResultSlot(slot) {
			if m.dragType == 1 || m.carried.count > int32(len(m.dragSlots)) {
				m.dragSlots = append(m.dragSlots, slot)
			}
		}
	case 2:
		if len(m.dragSlots) > 0 {
			if len(m.dragSlots) == 1 {
				one := m.dragSlots[0]
				typ := m.dragType
				m.resetDrag()
				m.doPickup(p, c, one, typ)
				return
			}
			left := m.carried.count
			for _, i := range m.dragSlots {
				if left == 0 {
					break
				}
				t := m.get(p, i)
				per := int32(1)
				if m.dragType == 0 {
					per = m.carried.count / int32(len(m.dragSlots))
				}
				if t.count == 0 {
					if per > left {
						per = left
					}
					m.set(p, i, invSlot{item: m.carried.item, count: per})
					left -= per
				} else if t.item == m.carried.item {
					room := itemMaxStack - t.count
					per2 := per
					if per2 > room {
						per2 = room
					}
					if per2 > left {
						per2 = left
					}
					t.count += per2
					m.set(p, i, t)
					left -= per2
				}
			}
			m.carried.count = left
		}
		m.resetDrag()
	}
}

func (m *menu) dragType2() int {
	if m.dragType == 1 {
		return 1
	}
	return 0
}

func (m *menu) resetDrag() {
	m.dragStatus = 0
	m.dragType = 0
	m.dragSlots = nil
}
