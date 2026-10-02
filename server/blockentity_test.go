package server

import (
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// blockPropsAt returns the property map of the state at a position.
func blockPropsAt(s *Server, x, y, z int) map[string]string {
	return blockPropsOf(int(s.world.getBlock(x, y, z)))
}

// countItemEntities counts live item entities. Caller holds Server.mu.
func countItemEntities(s *Server) int {
	n := 0
	for _, e := range s.entities {
		if _, ok := e.(*itemEntity); ok {
			n++
		}
	}
	return n
}

// TestFurnaceSmeltCycle drives the vanilla furnace block tick: coal
// ignites the furnace, two porkchops cook one by one, the lit block state
// swaps and the fuel burns out.
func TestFurnaceSmeltCycle(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()

	const bx, by, bz = 2, -60, 3
	if !s.world.setBlock(bx, by, bz, int32(defaultStateOf("minecraft:furnace"))) {
		t.Fatal("place furnace failed")
	}
	b := s.ensureBlockEntity(beFurnace, bx, by, bz)
	b.slots[0] = invSlot{item: itemID(t, "minecraft:porkchop"), count: 2}
	b.slots[1] = invSlot{item: itemID(t, "minecraft:coal"), count: 1}
	resetFurnaceInput(b)
	if b.cookingTotalTime != 200 {
		t.Fatalf("cooking total %d, want 200", b.cookingTotalTime)
	}

	// 200 ticks: the first porkchop finishes exactly on the boundary.
	for i := 0; i < 200; i++ {
		s.tickBlockEntities()
	}
	if got := b.slots[2]; got.item != itemID(t, "minecraft:cooked_porkchop") || got.count != 1 {
		t.Fatalf("result slot %v, want 1 cooked_porkchop", got)
	}
	if b.slots[0].count != 1 {
		t.Fatalf("ingredient count %d, want 1 after first cook", b.slots[0].count)
	}
	if b.slots[1].count != 0 {
		t.Fatalf("fuel count %d, coal is 1600 ticks", b.slots[1].count)
	}
	if b.litTimeRemaining <= 0 || b.litTimeRemaining > 1600 {
		t.Fatalf("lit time remaining %d out of range", b.litTimeRemaining)
	}
	if props := blockPropsAt(s, bx, by, bz); props["lit"] != "true" {
		t.Fatalf("block state lit=%q, want true", props["lit"])
	}
	if b.xpBank <= 0 {
		t.Fatal("smelting experience not banked")
	}

	// The furnace keeps burning on banked fuel time: the second porkchop
	// finishes 200 ticks later even though the coal slot is empty.
	for i := 0; i < 200; i++ {
		s.tickBlockEntities()
	}
	if got := b.slots[2]; got.count != 2 {
		t.Fatalf("result count %d, want 2 after second cook", got.count)
	}

	// With no fuel left the burn-down reaches zero and the block goes
	// dark. The leftover ingredient cannot relight (no fuel).
	for i := 0; i < 1400; i++ {
		s.tickBlockEntities()
	}
	if b.litTimeRemaining != 0 {
		t.Fatalf("lit time %d, want exhausted", b.litTimeRemaining)
	}
	if props := blockPropsAt(s, bx, by, bz); props["lit"] != "false" {
		t.Fatalf("block state lit=%q, want false after burnout", props["lit"])
	}
	s.mu.Unlock()
}

// TestFurnaceInputReset verifies the vanilla setItem semantics: replacing
// the ingredient item restarts the cook timer, a same-item top-up does
// not.
func TestFurnaceInputReset(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	defer s.mu.Unlock()

	b := s.ensureBlockEntity(beFurnace, 5, -60, 5)
	b.slots[0] = invSlot{item: itemID(t, "minecraft:porkchop"), count: 3}
	b.slots[1] = invSlot{item: itemID(t, "minecraft:coal"), count: 1}
	resetFurnaceInput(b)
	s.tickBlockEntities()
	if b.cookingTimer == 0 {
		t.Fatal("cooking did not start")
	}
	// Same item: progress preserved.
	resetFurnaceInput(b)
	if b.cookingTimer == 0 {
		t.Fatal("same-item top-up reset the cook timer")
	}
	// Different item: timer restarts with the new duration.
	b.slots[0] = invSlot{item: itemID(t, "minecraft:sand"), count: 1}
	resetFurnaceInput(b)
	if b.cookingTimer != 0 || b.cookingTotalTime != 200 {
		t.Fatalf("input swap: timer=%d total=%d, want 0/200", b.cookingTimer, b.cookingTotalTime)
	}
	// Sand smelts into glass.
	if r, ok := smeltResultOf(itemID(t, "minecraft:sand")); !ok || r.Result != "minecraft:glass" {
		t.Fatalf("sand smelt result %+v", r)
	}
}

// TestFurnaceResultTakeAndXP covers the take-only result slot: a click
// moves the stack to the cursor without consuming anything, and emptying
// the result settles the banked experience into an XP orb.
func TestFurnaceResultTakeAndXP(t *testing.T) {
	s, _, p := joinedBot(t, "Cook")
	s.mu.Lock()
	defer s.mu.Unlock()

	furn := s.ensureBlockEntity(beFurnace, 1, -60, 1)
	furn.slots[2] = invSlot{item: itemID(t, "minecraft:cooked_porkchop"), count: 2}
	furn.xpBank = 4 // whole orbs settle deterministically

	m := newFurnaceMenu(1, furn)
	p.openMenu = m
	m.doClick(p, p.conn, 2, 0, inputPickup) // take result onto the cursor
	if m.carried.item != itemID(t, "minecraft:cooked_porkchop") || m.carried.count != 2 {
		t.Fatalf("cursor %v, want 2 cooked_porkchop", m.carried)
	}
	if furn.slots[2].count != 0 {
		t.Fatal("result slot not emptied")
	}

	// Emptied result: the XP bank settles into one orb.
	if furn.xpBank != 0 {
		t.Fatalf("xp bank %v, want 0 after settle", furn.xpBank)
	}
	var orbValue int32
	for _, e := range s.entities {
		if orb, ok := e.(*xpOrbEntity); ok {
			orbValue += orb.value
		}
	}
	if orbValue != 4 {
		t.Fatalf("xp orb value %d, want 4", orbValue)
	}

	// The result slot rejects placement: clicking it while carrying a
	// different item must leave both the slot and the cursor unchanged.
	m.carried = invSlot{item: itemID(t, "minecraft:porkchop"), count: 1}
	m.doClick(p, p.conn, 2, 0, inputPickup)
	if furn.slots[2].count != 0 || m.carried.count != 1 {
		t.Fatal("placing through the result slot must be rejected")
	}
}

// TestFurnaceQuickMoveRouting mirrors AbstractFurnaceMenu.quickMoveStack:
// smeltables go to the ingredient slot, fuels to the fuel slot, inert
// stacks move between the main and hotbar rows.
func TestFurnaceQuickMoveRouting(t *testing.T) {
	s, _, p := joinedBot(t, "Router")
	s.mu.Lock()
	defer s.mu.Unlock()

	furn := s.ensureBlockEntity(beFurnace, 4, -60, 4)
	m := newFurnaceMenu(1, furn)
	p.openMenu = m
	// Empty the starter kit so routing targets are unambiguous.
	for i := range p.slots {
		p.slots[i] = invSlot{}
	}
	p.slots[9] = invSlot{item: itemID(t, "minecraft:porkchop"), count: 3}
	p.slots[10] = invSlot{item: itemID(t, "minecraft:coal"), count: 2}
	p.slots[11] = invSlot{item: itemID(t, "minecraft:bricks"), count: 5}

	m.doClick(p, p.conn, 3, 0, inputQuickMove) // wire 3 = main inv slot 9
	m.doClick(p, p.conn, 4, 0, inputQuickMove) // wire 4 = coal
	m.doClick(p, p.conn, 5, 0, inputQuickMove) // wire 5 = bricks (inert)
	if got := furn.slots[0]; got.item != itemID(t, "minecraft:porkchop") || got.count != 3 {
		t.Fatalf("ingredient slot %v", got)
	}
	if got := furn.slots[1]; got.item != itemID(t, "minecraft:coal") || got.count != 2 {
		t.Fatalf("fuel slot %v", got)
	}
	// Bricks are neither smeltable nor a fuel (and absent from the
	// starter hotbar): they land in the hotbar row (wire 30..38).
	bricks := int32(0)
	for i := 0; i < 9; i++ {
		if p.slots[i].item == itemID(t, "minecraft:bricks") {
			bricks += p.slots[i].count
		}
	}
	if bricks != 5 {
		t.Fatalf("hotbar bricks total %d, want 5", bricks)
	}
	if p.slots[9].count != 0 || p.slots[10].count != 0 || p.slots[11].count != 0 {
		t.Fatal("sources not drained")
	}
}

// TestChestMenuStoreRetrieveAndBreak stores items through the chest menu,
// reopens it (contents persist in the block entity), then breaks the
// block and checks the spill.
func TestChestMenuStoreRetrieveAndBreak(t *testing.T) {
	s, _, p := joinedBot(t, "Hoarder")
	s.mu.Lock()

	const bx, by, bz = 3, -60, 2
	if !s.world.setBlock(bx, by, bz, int32(defaultStateOf("minecraft:chest"))) {
		t.Fatal("place chest failed")
	}
	b := s.ensureBlockEntity(beChest, bx, by, bz)

	// Open, place 5 stone from the cursor into chest slot 4, close.
	m := newChestMenu(1, b)
	p.openMenu = m
	m.carried = invSlot{item: itemID(t, "minecraft:stone"), count: 5}
	m.doClick(p, p.conn, 4, 0, inputPickup)
	if b.slots[4].count != 5 || m.carried.count != 0 {
		t.Fatalf("store: chest %v cursor %v", b.slots[4], m.carried)
	}
	p.conn.closeMenu(m.id, false)
	if p.openMenu != nil {
		t.Fatal("closeMenu left the menu open")
	}

	// Reopen: a fresh menu over the same block entity keeps the stack.
	m2 := newChestMenu(2, b)
	p.openMenu = m2
	if got := m2.get(p, 4); got.count != 5 {
		t.Fatalf("reopen slot 4: %v", got)
	}
	// Shift-click pulls it back into the inventory.
	m2.doClick(p, p.conn, 4, 0, inputQuickMove)
	if b.slots[4].count != 0 {
		t.Fatalf("quick-move left %v in the chest", b.slots[4])
	}
	if p.slots[9].item != itemID(t, "minecraft:stone") || p.slots[9].count != 5 {
		t.Fatalf("inventory after pull: %v", p.slots[9])
	}
	p.openMenu = nil
	s.mu.Unlock()

	// Break: the chest item drops (contents already pulled out).
	s.mu.Lock()
	before := countItemEntities(s)
	s.mu.Unlock()
	s.breakBlock(p, bx, by, bz)
	s.mu.Lock()
	defer s.mu.Unlock()
	if got := countItemEntities(s) - before; got != 1 {
		t.Fatalf("break dropped %d entities, want 1 (chest item)", got)
	}
	if s.blockEntsAt(bx, by, bz) != nil {
		t.Fatal("block entity survived the break")
	}
}

// drainOne reads a single frame from the bot's stream, returning false
// on timeout. Decompression mirrors next() without the Fatal-on-error
// behaviour so tests can poll.
func drainOne(b *botConn) bool {
	b.nc.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	payload, err := b.fr.Next()
	if err != nil {
		return false
	}
	if b.comp != nil {
		if _, err := b.comp.DecompressFrame(payload, protocol.DefaultMaxPacketLen); err != nil {
			return false
		}
	}
	return true
}

// waitForWire polls a condition while draining server frames so the
// writer pipeline never stalls.
func waitForWire(t *testing.T, b *botConn, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		drainOne(b)
	}
}

// TestChestWireOpenClickClose drives the full wire path: place a chest via
// UseItemOn, open it, click (Container Click) to deposit and close it. It
// guards the M10 dispatch wiring — before this milestone the click packet
// was silently dropped by the play switch.
func TestChestWireOpenClickClose(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Wired")
	p := botPlayer(t, s, "Wired") // botPlayer locks s.mu itself
	chestItem := itemID(t, "minecraft:chest")
	stoneItem := itemID(t, "minecraft:stone")
	s.mu.Lock()
	p.slots[0] = invSlot{item: chestItem, count: 1}
	p.slots[9] = invSlot{item: stoneItem, count: 4}
	s.mu.Unlock()

	// Place the chest on top of the grass at spawn (face UP on (0,-61,0)).
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayUseItemOn)
	w.VarInt(0) // main hand
	java.WriteBlockPos(w, 0, -61, 0)
	w.VarInt(1) // face up
	w.Float(0.5)
	w.Float(1)
	w.Float(0.5)
	w.Bool(false) // not inside block
	w.Bool(false) // world border
	w.VarInt(11)  // sequence
	b.write(w.Bytes())

	var chestBE *blockEntity
	s.mu.Lock()
	chestBE = s.blockEntsAt(0, -60, 0)
	s.mu.Unlock()
	waitForWire(t, b, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		chestBE = s.blockEntsAt(0, -60, 0)
		return chestBE != nil
	}, "chest placement")

	// Re-open through an interact click on the placed chest.
	w.Reset()
	w.VarInt(v776.PacketPlayUseItemOn)
	w.VarInt(0)
	java.WriteBlockPos(w, 0, -60, 0)
	w.VarInt(1)
	w.Float(0.5)
	w.Float(0.5)
	w.Float(0.5)
	w.Bool(false)
	w.Bool(false)
	w.VarInt(12)
	b.write(w.Bytes())

	// The server answers with OpenScreen (generic_9x3) and then the full
	// content sync for the chest window.
	b.nc.SetReadDeadline(time.Now().Add(8 * time.Second))
	var windowID, stateID int32
	deadline := time.Now().Add(8 * time.Second)
	for windowID == 0 {
		if time.Now().After(deadline) {
			t.Fatal("never received OpenScreen")
		}
		id, r := b.next()
		if id != v776.PacketPlayOpenScreen {
			continue // drain placement chatter
		}
		windowID, _ = r.VarInt()
		menuID, _ := r.VarInt()
		if menuID != menuTypeChest9x3 {
			t.Fatalf("menu id %d, want %d", menuID, menuTypeChest9x3)
		}
		if _, err := r.String(256); err != nil {
			t.Fatal(err)
		}
	}
	if windowID <= 0 {
		t.Fatalf("window id %d, want a server-assigned id >= 1", windowID)
	}
	// Drain until the content sync for the chest window.
	for {
		id, r := b.next()
		if id != v776.PacketPlayContainerContent {
			continue
		}
		cid, _ := r.VarInt()
		if cid != windowID {
			continue
		}
		stateID, _ = r.VarInt()
		break
	}
	if stateID == 0 {
		t.Fatal("content sync carried no state id")
	}

	// PICKUP the 4 stones from main-inv wire 27, then deposit them into
	// chest slot 0.
	click := func(slot int32, button byte, input int32) {
		t.Helper()
		cw := protocol.NewWriter()
		cw.VarInt(v776.PacketPlaySBContainerClick)
		cw.VarInt(windowID)
		cw.VarInt(stateID)
		cw.Uint16(uint16(slot))
		cw.Byte(button)
		cw.VarInt(input)
		cw.VarInt(0) // changed slots: none
		cw.Bool(false)
		b.write(cw.Bytes())
	}
	click(27, 0, inputPickup) // pick up the stones
	click(0, 0, inputPickup)  // deposit into the chest

	waitForWire(t, b, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		got := chestBE.slots[0]
		return got.item == stoneItem && got.count == 4
	}, "wire click deposit")

	// Close the window client-side; the server must drop openMenu but
	// keep the block entity.
	cw := protocol.NewWriter()
	cw.VarInt(v776.PacketPlaySBContainerClose)
	cw.VarInt(windowID)
	b.write(cw.Bytes())
	waitForWire(t, b, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return p.openMenu == nil
	}, "menu close")
	s.mu.Lock()
	if got := chestBE.slots[0]; got.count != 4 {
		t.Fatalf("chest contents lost on close: %v", got)
	}
	s.mu.Unlock()
}

// TestBlockEntityNbtRoundTrip serialises both container kinds through the
// Anvil format and verifies every field, including the furnace progress
// shorts and the vanilla-compatible snake_case keys.
func TestBlockEntityNbtRoundTrip(t *testing.T) {
	chest := newBlockEntity(beChest, 7, -60, 9)
	chest.slots[0] = invSlot{item: itemID(t, "minecraft:diamond"), count: 12}
	chest.slots[26] = invSlot{item: itemID(t, "minecraft:oak_log"), count: 3}

	var buf protocol.Writer
	java.WriteNbtFile(&buf, nbtBlockEntity(chest))
	root, err := java.ReadNbtFile(protocol.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if root.Get("id").Str != "minecraft:chest" {
		t.Fatalf("id %q", root.Get("id").Str)
	}
	if got := len(root.Get("Items").List); got != 2 {
		t.Fatalf("items %d, want 2", got)
	}
	back := blockEntityFromNBT(root)
	if back == nil || back.kind != beChest || back.x != 7 || back.y != -60 || back.z != 9 {
		t.Fatalf("decoded chest %+v", back)
	}
	if back.slots[0].item != itemID(t, "minecraft:diamond") || back.slots[0].count != 12 {
		t.Fatalf("slot 0 %v", back.slots[0])
	}
	if back.slots[26].count != 3 {
		t.Fatalf("slot 26 %v", back.slots[26])
	}

	// Furnace: the extra progress fields round-trip as shorts.
	furn := newBlockEntity(beFurnace, -3, -60, 12)
	furn.slots[0] = invSlot{item: itemID(t, "minecraft:porkchop"), count: 8}
	furn.slots[1] = invSlot{item: itemID(t, "minecraft:coal"), count: 2}
	furn.slots[2] = invSlot{item: itemID(t, "minecraft:cooked_porkchop"), count: 1}
	furn.cookingTimer = 33
	furn.cookingTotalTime = 200
	furn.litTimeRemaining = 1400
	furn.litTotalTime = 1600
	buf.Reset()
	java.WriteNbtFile(&buf, nbtBlockEntity(furn))
	froot, err := java.ReadNbtFile(protocol.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if froot.Get("lit_time_remaining").Num != 1400 || froot.Get("cooking_time_spent").Num != 33 {
		t.Fatalf("furnace shorts: lit=%v cook=%v", froot.Get("lit_time_remaining").Num, froot.Get("cooking_time_spent").Num)
	}
	fback := blockEntityFromNBT(froot)
	if fback == nil || fback.kind != beFurnace {
		t.Fatal("furnace not decoded")
	}
	if fback.litTimeRemaining != 1400 || fback.cookingTimer != 33 || fback.litTotalTime != 1600 {
		t.Fatalf("furnace progress lost: %+v", fback)
	}
	if fback.slots[1].item != itemID(t, "minecraft:coal") || fback.slots[1].count != 2 {
		t.Fatalf("fuel slot %v", fback.slots[1])
	}
}

// TestBlockEntityPersistRestart is the acceptance check for container
// persistence: a chest with contents survives a full server restart.
func TestBlockEntityPersistRestart(t *testing.T) {
	dir := t.TempDir()
	s1, err := New(Options{
		ListenAddr:   "127.0.0.1:0",
		LevelName:    dir,
		ViewDistance: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	const bx, by, bz = 10, -60, 10
	s1.world.setBlock(bx, by, bz, int32(defaultStateOf("minecraft:chest")))
	b := s1.ensureBlockEntity(beChest, bx, by, bz)
	b.slots[3] = invSlot{item: itemID(t, "minecraft:golden_apple"), count: 7}
	if err := s1.saveAllWorld(); err != nil {
		t.Fatal(err)
	}

	s2, err := New(Options{
		ListenAddr:   "127.0.0.1:0",
		LevelName:    dir,
		ViewDistance: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	restored := s2.blockEntsAt(bx, by, bz)
	if restored == nil {
		t.Fatal("block entity lost across restart")
	}
	if got := restored.slots[3]; got.item != itemID(t, "minecraft:golden_apple") || got.count != 7 {
		t.Fatalf("restored slot %v", got)
	}
	if got := s2.world.getBlock(bx, by, bz); blockNameOf(int(got)) != "minecraft:chest" {
		t.Fatalf("restored block %s", blockNameOf(int(got)))
	}
}
