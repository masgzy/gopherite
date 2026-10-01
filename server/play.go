package server

import (
	"log"
	"time"

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
// PlayerList.placeNewPlayer for the M2 packet subset.
func (c *conn) startPlay() error {
	log.Printf("%s entered play", c.username)
	p := &player{
		conn:  c,
		name:  c.username,
		id:    1, // single-player milestone: entity id 1
		x:     0.5,
		y:     -60,
		z:     0.5,
		yaw:   0,
		pitch: 0,
	}
	c.player = p

	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayLogin)
	java.WritePlayLogin(c.wr, java.ClientboundLogin{
		EntityID:         p.id,
		Hardcore:         false,
		Levels:           []string{"minecraft:overworld"},
		MaxPlayers:       int32(c.s.opts.MaxPlayers),
		ChunkRadius:      int32(c.s.opts.ViewDistance),
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
	java.WritePlayCacheRadius(c.wr, int32(c.s.opts.ViewDistance))
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Default spawn position: packed block pos (0, -60, 0).
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlaySpawnPosition)
	java.WritePlaySpawnPosition(c.wr, "minecraft:overworld", packBlockPos(0, -60, 0), 0, 0)
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
	p := c.player
	radius := int32(c.s.opts.ViewDistance)
	cx, cz := chunkCoord(p.x), chunkCoord(p.z)
	log.Printf("preparing spawn area for %s (radius %d)", c.username, radius)

	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayChunkBatchStart)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	count := 0
	for dx := -radius; dx <= radius; dx++ {
		for dz := -radius; dz <= radius; dz++ {
			data, err := c.s.world.chunkData(cx+dx, cz+dz)
			if err != nil {
				return err
			}
			c.wr.Reset()
			c.wr.VarInt(v776.PacketPlayLevelChunk)
			c.wr.FixedBytes(data)
			if err := c.sendPacket(c.wr.Bytes()); err != nil {
				return err
			}
			count++
		}
	}

	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayCBChunkBatchDone)
	c.wr.VarInt(int32(count))
	return c.sendPacket(c.wr.Bytes())
}

// handleMove applies a movement packet to the player model. M2 trusts the
// client fully (no anti-cheat); chunk borders trigger new chunk streaming.
func (c *conn) handleMove(packetID int32) error {
	move, err := java.ReadPlayMove(c.rd, packetID)
	if err != nil {
		return err
	}
	p := c.player
	if p == nil {
		return nil
	}
	if move.HasPos {
		p.x, p.y, p.z = move.X, move.Y, move.Z
		if ncx := chunkCoord(p.x); ncx != p.cx {
			if p.chunksSent {
				// stream the entered chunk column lazily in M2
				if err := c.sendChunkColumn(ncx, p.cz); err != nil {
					return err
				}
			}
			p.cx = ncx
		}
		if ncz := chunkCoord(p.z); ncz != p.cz {
			if p.chunksSent {
				if err := c.sendChunkColumn(p.cx, ncz); err != nil {
					return err
				}
			}
			p.cz = ncz
		}
	}
	if move.HasRot {
		p.yaw, p.pitch = move.Yaw, move.Pitch
	}
	return nil
}

// sendChunkColumn streams the chunk column for a chunk the player entered.
func (c *conn) sendChunkColumn(cx, cz int32) error {
	data, err := c.s.world.chunkData(cx, cz)
	if err != nil {
		return err
	}
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayLevelChunk)
	c.wr.FixedBytes(data)
	return c.sendPacket(c.wr.Bytes())
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

// packBlockPos packs a block position into the vanilla long form.
func packBlockPos(x, y, z int64) int64 {
	return ((x & 0x3FFFFFF) << 38) | ((z & 0x3FFFFFF) << 12) | (y & 0xFFF)
}

// chunkCoord converts a world coordinate to a chunk coordinate.
func chunkCoord(v float64) int32 {
	return int32(v) >> 4
}
