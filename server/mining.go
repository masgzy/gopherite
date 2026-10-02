package server

import (
	"log"
	"strings"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// miningState tracks one in-progress block break per player. The pointer
// is guarded by Server.mu (read/written from the connection goroutine
// and the ticker); the pointed-to struct is only touched while it is
// still the player's current mining state.
type miningState struct {
	x, y, z int
	state   int32   // state id at dig start
	damage  float64 // accumulated 0..1
	stage   int8    // last broadcast destruction stage
}

// blockHardness is the M3 hardness entry for a block.
type blockHardness struct {
	hardness            float64
	requiresCorrectTool bool
}

// hardnessByBlock covers the blocks a superflat player can reach;
// unknown blocks default to one second of hand mining.
var hardnessByBlock = map[string]blockHardness{
	"minecraft:dirt":             {0.5, false},
	"minecraft:grass_block":      {0.6, false},
	"minecraft:stone":            {1.5, true},
	"minecraft:bedrock":          {-1, false},
	"minecraft:cobblestone":      {2.0, true},
	"minecraft:oak_planks":       {2.0, false},
	"minecraft:oak_log":          {2.0, false},
	"minecraft:sand":             {0.5, false},
	"minecraft:gravel":           {0.6, false},
	"minecraft:glass":            {0.3, false},
	"minecraft:oak_leaves":       {0.2, false},
	"minecraft:deepslate":        {3.0, true},
	"minecraft:netherrack":       {0.4, true},
	"minecraft:end_stone":        {3.0, true},
	"minecraft:obsidian":         {50.0, true},
	"minecraft:crafting_table":   {2.5, false},
	"minecraft:furnace":          {3.5, true},
	"minecraft:chest":            {2.5, false},
	"minecraft:torch":            {0.0, false},
	"minecraft:cobblestone_slab": {2.0, true},
}

// hardnessOf resolves the hardness entry for a state id.
func hardnessOf(state int32) blockHardness {
	name := blockNameOf(int(state))
	if h, ok := hardnessByBlock[name]; ok {
		return h
	}
	return blockHardness{hardness: 1.0, requiresCorrectTool: false}
}

// handlePlayerAction processes dig start/abort/stop.
func (c *conn) handlePlayerAction() error {
	a, err := java.ReadPlayerAction(c.rd)
	if err != nil {
		return err
	}
	p := c.player
	if p == nil {
		return nil
	}
	switch a.Action {
	case java.ActionStartDestroy:
		state := c.s.world.getBlock(int(a.X), int(a.Y), int(a.Z))
		if hardnessOf(state).hardness < 0 {
			// Unbreakable: ack so the client clears its prediction.
			c.ackSequence(a.Sequence)
			return nil
		}
		c.s.mu.Lock()
		p.mining = &miningState{x: int(a.X), y: int(a.Y), z: int(a.Z), state: state}
		c.s.mu.Unlock()
		c.ackSequence(a.Sequence)
		// Advance one tick immediately so instant-break blocks (hardness
		// 0) break without waiting for the ticker.
		return c.s.advanceMining(p)
	case java.ActionAbortDestroy, java.ActionStopDestroy:
		c.s.mu.Lock()
		if m := p.mining; m != nil && m.x == int(a.X) && m.y == int(a.Y) && m.z == int(a.Z) {
			switch a.Action {
			case java.ActionAbortDestroy:
				p.mining = nil
			case java.ActionStopDestroy:
				// Vanilla stops the overlay on client-reported completion;
				// the ticker finishes the block if damage already crossed.
				if m.damage < 1.0 {
					m.damage += miningPerTick(1.0, p.onGround, hardnessOf(m.state))
				}
			}
		}
		c.s.mu.Unlock()
		c.ackSequence(a.Sequence)
	case java.ActionDropAllItem, java.ActionDropItem:
		// Q key: drop from the selected hotbar slot (all / one).
		c.s.mu.Lock()
		p2 := c.player
		if p2 != nil && p2.heldSlot >= 0 && p2.heldSlot < int32(len(p2.slots)) {
			held := p2.slots[p2.heldSlot]
			if held.count > 0 {
				drop := held.count
				if a.Action == java.ActionDropItem {
					drop = 1
				}
				held.count -= drop
				p2.slots[p2.heldSlot] = held
				c.s.spawnPlayerDrop(p2, held.item, drop)
				c.sendSlot(p2.heldSlot, held)
			}
		}
		c.s.mu.Unlock()
	case java.ActionReleaseUseItem:
		// Right-click released: fire a drawn bow (M11) and abort an
		// in-progress eat.
		c.releaseBow()
		c.cancelEating()
	case java.ActionSwapItemWithOffhand:
		// Off-hand swap aborts without firing.
		c.cancelEating()
	}
	return nil
}

// handleUseItemOn acknowledges the sequence and places the held block
// item against the clicked face. The starter hotbar is infinite (no
// decrement yet); client predictions settle via the ack + block update.
func (c *conn) handleUseItemOn() error {
	u, err := java.ReadUseItemOn(c.rd)
	if err != nil {
		return err
	}
	c.ackSequence(u.Sequence)
	c.placeBlock(u)
	return nil
}

// ackSequence replies with the block-changed acknowledgement.
func (c *conn) ackSequence(sequence int32) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayBlockChangedAck)
	java.WriteBlockChangedAck(body, sequence)
	_ = c.sendPacket(body.Bytes())
}

// miningPerTick is the vanilla bare-hand damage increment:
// speed / hardness / (can_harvest ? 30 : 100), speed floored off-ground.
func miningPerTick(speed float64, onGround bool, h blockHardness) float64 {
	if !onGround {
		speed *= 0.2
	}
	den := h.hardness
	if h.requiresCorrectTool {
		den *= 100
	} else {
		den *= 30
	}
	if den <= 0 {
		return 1
	}
	return speed / den
}

// advanceMining pushes one tick of progress for a player's dig. Called
// from the ticker and right after a start action.
func (s *Server) advanceMining(p *player) error {
	s.mu.Lock()
	m := p.mining
	if m == nil {
		s.mu.Unlock()
		return nil
	}
	h := hardnessOf(m.state)
	per := miningPerTick(1.0, p.onGround, h)
	m.damage += per
	stage := int8(m.damage * 10)
	if stage > 9 {
		stage = 9
	}
	progressed := stage != m.stage
	if progressed {
		m.stage = stage
	}
	px, py, pz := int32(m.x), int32(m.y), int32(m.z)
	finished := m.damage >= 1.0
	if finished {
		p.mining = nil
	}
	s.mu.Unlock()

	if progressed {
		s.broadcastBlockDestruction(p, px, py, pz, stage)
	}
	if finished {
		s.breakBlock(p, int(px), int(py), int(pz))
	}
	return nil
}

// breakBlock replaces a block with air, broadcasts the change to every
// player that can see the chunk and spawns the block's drop as an item
// entity (M5). Container block entities spill their contents first (M10).
func (s *Server) breakBlock(breaker *player, x, y, z int) {
	dropped := s.world.getBlock(x, y, z)
	s.mu.Lock()
	if b := s.blockEntsAt(x, y, z); b != nil {
		s.removeBlockEntity(b)
	}
	s.mu.Unlock()
	if !s.world.setBlock(x, y, z, stateAir) {
		return
	}
	s.broadcastBlockUpdate(int32(x), int32(y), int32(z), stateAir)
	// M12: 床破坏联动——另一半静默清除，掉落只走主格一次。
	if name := blockNameOf(int(dropped)); strings.HasSuffix(name, "_bed") {
		s.mu.Lock()
		s.breakBed(x, y, z, dropped)
		s.mu.Unlock()
	}
	s.spawnBlockDrop(x, y, z, dropped)
	if isComponent(blockNameOf(int(dropped))) {
		s.redstoneUpdate(x, y, z)
	}
	log.Printf(ui.Success("OK ")+"%s 挖掉了 (%d, %d, %d)", breaker.name, x, y, z)
}

// blockDrops maps blocks to their vanilla drop item: stone drops
// cobblestone, grass drops dirt, everything else drops itself when an
// item form exists.
func blockDrops(name string) (string, bool) {
	switch name {
	case "minecraft:stone":
		return "minecraft:cobblestone", true
	case "minecraft:grass_block":
		return "minecraft:dirt", true
	case "minecraft:redstone_wire":
		return "minecraft:redstone", true
	default:
		return name, true
	}
}

// spawnBlockDrop turns a broken block into an item entity at the block
// center. Unknown item forms (rare superflat) skip the drop.
func (s *Server) spawnBlockDrop(x, y, z int, state int32) {
	name := blockNameOf(int(state))
	if name == "" {
		return
	}
	drop, ok := blockDrops(name)
	itemID, ok2 := itemIDByName[drop]
	if !ok2 {
		// fall back to the block's own item id when a mapping exists
		if id, has := itemIDByName[name]; has {
			itemID = id
		} else {
			return
		}
	}
	_ = ok
	e := newItemEntity(s.allocEntityID(), float64(x)+0.5, float64(y)+0.4, float64(z)+0.5, itemID, 1)
	s.spawnEntity(e)
}

// broadcastBlockUpdate sends a Block Update to every player whose
// streamed chunks include the position. The payload is encoded once.
func (s *Server) broadcastBlockUpdate(x, y, z int32, state int32) {
	key := [2]int32{int32(x >> 4), int32(z >> 4)}
	s.mu.Lock()
	var targets []*conn
	for _, p := range s.players {
		if p.seen[key] {
			targets = append(targets, p.conn)
		}
	}
	s.mu.Unlock()

	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayBlockUpdate)
	java.WriteBlockUpdate(body, x, y, z, state)
	for _, c := range targets {
		_ = c.sendPacket(body.Bytes())
	}
}

// broadcastBlockUpdateLocked is the variant for paths that already hold
// Server.mu (redstone recomputation batches).
func (s *Server) broadcastBlockUpdateLocked(x, y, z int32, state int32) {
	key := [2]int32{int32(x >> 4), int32(z >> 4)}
	var targets []*conn
	for _, p := range s.players {
		if p.seen[key] {
			targets = append(targets, p.conn)
		}
	}
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayBlockUpdate)
	java.WriteBlockUpdate(body, x, y, z, state)
	for _, c := range targets {
		_ = c.sendPacket(body.Bytes())
	}
}

// broadcastBlockDestruction sends mining progress to everyone except the
// digger (vanilla clients render their own overlay locally).
func (s *Server) broadcastBlockDestruction(digger *player, x, y, z int32, stage int8) {
	s.mu.Lock()
	var targets []*conn
	for _, p := range s.players {
		if p != digger && p.seen[[2]int32{int32(x >> 4), int32(z >> 4)}] {
			targets = append(targets, p.conn)
		}
	}
	s.mu.Unlock()

	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayBlockDestruction)
	java.WriteBlockDestruction(body, digger.id, x, y, z, stage)
	for _, c := range targets {
		_ = c.sendPacket(body.Bytes())
	}
}
