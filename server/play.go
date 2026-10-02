package server

import (
	"fmt"
	"log"
	"time"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// handlePlay runs the play phase: spawn sequence, movement, keep-alive.
func (c *conn) handlePlay() error {
	id, err := c.rd.VarInt()
	if err != nil {
		return err
	}
	switch id {
	case v776.PacketPlayConfigAcknowledged:
		return java.ReadPlayConfigAcknowledged(c.rd)
	case v776.PacketPlayPlayerLoaded:
		// Client finished loading the world; start streaming chunks.
		if c.player != nil && !c.player.chunksSent {
			c.player.chunksSent = true
			return c.sendSpawnChunks()
		}
		return nil
	case v776.PacketPlaySBKeepAlive:
		_, err := java.ReadPlayKeepAlive(c.rd)
		return err
	case v776.PacketPlayMovePos, v776.PacketPlayMovePosRot, v776.PacketPlayMoveRot, v776.PacketPlayMoveStatus:
		return c.handleMove(id)
	case v776.PacketPlayAcceptTeleport:
		_, err := java.ReadPlayAcceptTeleport(c.rd)
		return err
	case v776.PacketPlaySBChunkBatchDone:
		// Chunk batch received: the client's throughput report; M2
		// ignores the pacing hint and streams at full speed.
		_, err := java.ReadPlayChunkBatchDone(c.rd)
		return err
	case v776.PacketPlayPlayerAction:
		return c.handlePlayerAction()
	case v776.PacketPlayUseItemOn:
		return c.handleUseItemOn()
	case v776.PacketPlaySBChatCommand:
		cmd, err := java.ReadChatCommand(c.rd)
		if err != nil {
			return err
		}
		return c.handleCommand(cmd)
	case v776.PacketPlaySBCarriedItem:
		return c.handleSetCarriedItem()
	case v776.PacketPlaySwing:
		// Arm swing: no entity animation broadcast yet (M3+).
		return nil
	case v776.PacketPlayClientTickEnd:
		return java.ReadPlayClientTickEnd(c.rd)
	case v776.PacketPlaySBPong:
		_, err := java.ReadPlayPong(c.rd)
		return err
	}
	// Unknown serverbound packets are skipped, mirroring vanilla: the
	// client may send packet types this milestone does not implement yet
	// (e.g. client settings, brand plugin message, chat).
	return nil
}

// startPlay performs the initial spawn packet sequence, mirroring
// PlayerList.placeNewPlayer for the M3 packet subset.
func (c *conn) startPlay() error {
	log.Printf(ui.Success("OK ")+"%s 进入游戏", c.username)
	radius := int32(c.s.opts.ViewDistance)
	if c.clientViewDistance > 0 && int32(c.clientViewDistance) < radius {
		radius = int32(c.clientViewDistance)
	}
	p := &player{
		conn:     c,
		name:     c.username,
		id:       c.s.allocEntityID(),
		x:        0.5,
		y:        -60,
		z:        0.5,
		yaw:      0,
		pitch:    0,
		radius:   radius,
		seen:     make(map[[2]int32]bool),
		seenEnt:  make(map[int32]bool),
		onGround: true,
		barUUID:  newBarUUID(),

		invMenu: newInventoryMenu(),
	}
	c.player = p
	c.s.addPlayer(p)

	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayLogin)
	java.WritePlayLogin(c.wr, java.ClientboundLogin{
		EntityID:   p.id,
		Hardcore:   false,
		Levels:     []string{"minecraft:overworld"},
		MaxPlayers: int32(c.s.opts.MaxPlayers),
		// Declare exactly the radius syncChunks will deliver: the client's
		// loading screen waits for the declared square, so a larger number
		// than the actual send leaves the player staring at the terrain
		// screen until movement forces a re-sync.
		ChunkRadius:      radius,
		SimulationRadius: int32(c.s.opts.ViewDistance),
		ReducedDebug:     false,
		ShowDeathScreen:  true,
		LimitedCrafting:  false,
		Spawn: java.CommonSpawnInfo{
			DimensionType:    "minecraft:overworld",
			DimensionTypeID:  1, // overworld = registry entry 0, holder form id+1
			Dimension:        "minecraft:overworld",
			Seed:             c.s.world.seed,
			GameType:         0,    // survival
			PreviousGameType: 0xFF, // none (GameType.getNullableId uses 255? see below)
			IsFlat:           true,
			PortalCooldown:   0,
			SeaLevel:         63,
		},
		OnlineMode:         c.s.opts.OnlineMode,
		EnforcesSecureChat: false,
	})
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Difficulty (normal, unlocked).
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayDifficulty)
	java.WritePlayDifficulty(c.wr, 2, false)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Command tree (Declare Commands) before the rest of the play UI.
	c.sendDeclareCommands()

	// Abilities.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayAbilities)
	java.WritePlayAbilities(c.wr, false, false, false, false, 0.05, 0.1)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Held slot.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayHeldSlot)
	java.WritePlayHeldSlot(c.wr, 0)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Starter hotbar (per-slot inventory sync).
	c.sendStarterInventory()

	// Full inventory-menu state (vanilla sendAllDataToRemote on initMenu).
	p.invMenu.sendAll(c)

	// Tab list initialisation with this player only.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayPlayerInfo)
	java.WritePlayPlayerInfo(c.wr, []java.PlayerInfoEntry{{
		UUID:     c.profileID,
		Name:     c.username,
		GameMode: 0,
		Listed:   true,
		Latency:  0,
	}})
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Teleport to spawn (absolute).
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayPlayerPosition)
	java.WritePlayPlayerPosition(c.wr, java.ClientboundPlayerPosition{
		TeleportID: 1,
		X:          p.x,
		Y:          p.y,
		Z:          p.z,
		Relatives:  0,
	})
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Chunk cache centre + radius.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayCacheCenter)
	java.WritePlayCacheCenter(c.wr, 0, 0)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayCacheRadius)
	java.WritePlayCacheRadius(c.wr, radius)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Default spawn position: packed block pos (0, -60, 0).
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlaySpawnPosition)
	java.WritePlaySpawnPosition(c.wr, "minecraft:overworld", java.PackBlockPos(0, -60, 0), 0, 0)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// World clock sync: overworld clock at tick 0, rate 1.0.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlaySetTime)
	java.WritePlaySetTime(c.wr, 0, []java.ClockState{{
		ClockID:     c.s.world.clockID("overworld"),
		TotalTicks:  0,
		PartialTick: 0,
		Rate:        1.0,
	}})
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// LEVEL_CHUNKS_LOAD_START game event.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayGameEvent)
	java.WritePlayGameEvent(c.wr, 13, 0)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Launch the keep-alive ticker.
	go c.keepAliveLoop()
	return nil
}

// sendSpawnChunks streams the initial chunk batch once the client reports
// itself loaded.
func (c *conn) sendSpawnChunks() error {
	log.Printf("正在为 %s 准备出生区域（半径 %s）", c.username, ui.Number(fmt.Sprint(c.player.radius)))
	return c.syncChunks()
}

// handleMove applies a movement packet to the player model. M3 trusts the
// client fully (no anti-cheat); crossing chunk borders triggers the
// incremental chunk sync (new sends + unload notifications).
func (c *conn) handleMove(packetID int32) error {
	move, err := java.ReadPlayMove(c.rd, packetID)
	if err != nil {
		return err
	}
	p := c.player
	if p == nil {
		return nil
	}
	moved := false
	if move.HasPos {
		p.x, p.y, p.z = move.X, move.Y, move.Z
		if ncx := chunkCoord(p.x); ncx != p.cx {
			p.cx = ncx
			moved = true
		}
		if ncz := chunkCoord(p.z); ncz != p.cz {
			p.cz = ncz
			moved = true
		}
	}
	// Every move variant (including status-only) carries the ground flag;
	// mining speed depends on it.
	p.onGround = move.OnGround
	if move.HasRot {
		p.yaw, p.pitch = move.Yaw, move.Pitch
	}
	if moved && p.chunksSent {
		// Only re-sync when the chunk actually changed.
		return c.syncChunks()
	}
	return nil
}

// keepAliveLoop sends a keep-alive every 15 seconds (vanilla
// KEEPALIVE_PERIOD) and stops when the connection leaves play state.
func (c *conn) keepAliveLoop() {
	interval := c.s.opts.KeepAliveInterval
	if interval <= 0 {
		interval = 15 * time.Second
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	// Own scratch writer: the dispatch goroutine uses c.wr concurrently.
	w := protocol.NewWriter()
	for range t.C {
		if c.st != statePlay {
			return
		}
		nonce, err := protocol.RandomLong()
		if err != nil {
			return
		}
		w.Reset()
		w.VarInt(v776.PacketPlayCBKeepAlive)
		java.WritePlayKeepAlive(w, nonce)
		if err := c.sendPacket(w.Bytes()); err != nil {
			return
		}
	}
}

// chunkCoord converts a world coordinate to a chunk coordinate.
func chunkCoord(v float64) int32 {
	return int32(v) >> 4
}
