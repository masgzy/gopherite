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
	case v776.PacketPlaySBAttack:
		targetID, err := java.ReadAttack(c.rd)
		if err != nil {
			return err
		}
		return c.handleAttack(targetID)
	case v776.PacketPlaySBInteract:
		// Right-click on an entity: consumed to keep the stream aligned;
		// entity interactions (breeding, riding) arrive with containers.
		_, _, _, err := java.ReadInteract(c.rd)
		return err
	case v776.PacketPlaySBClientCommand:
		return c.handleClientCommand()
	case v776.PacketPlaySBPlayerCommand:
		_, action, _, err := java.ReadPlayerCommand(c.rd)
		if err != nil {
			return err
		}
		if p := c.player; p != nil {
			c.s.mu.Lock()
			switch action {
			case java.PlayerCommandStartSprinting:
				p.sprinting = true
			case java.PlayerCommandStopSprinting:
				p.sprinting = false
			}
			c.s.mu.Unlock()
		}
		return nil
	case v776.PacketPlayUseItem:
		hand, _, _, _, err := java.ReadUseItem(c.rd)
		if err != nil {
			return err
		}
		// M11: a bow starts the draw; everything else keeps the M8
		// eating path.
		c.useItemStart(hand)
		return nil
	case v776.PacketPlayUseItemOn:
		return c.handleUseItemOn()
	case v776.PacketPlaySBChatCommand:
		cmd, err := java.ReadChatCommand(c.rd)
		if err != nil {
			return err
		}
		return c.handleCommand(cmd)
	case v776.PacketPlaySBChatCommandSigned:
		// M16: 在线客户端总是发 signed 变体；签名不校验（离线无聊天
		// 会话），仅按 26.2 字段序消费后走同一分发。
		cmd, err := java.ReadChatCommandSigned(c.rd)
		if err != nil {
			return err
		}
		return c.handleCommand(cmd)
	case v776.PacketPlaySBCommandSuggestion:
		// M16: Tab 补全请求 → suggestionsFor 组包回复。
		return c.handleCommandSuggestion()
	case v776.PacketPlaySBCarriedItem:
		return c.handleSetCarriedItem()
	case v776.PacketPlaySBContainerClick:
		// M10: the M7 click state machine was never wired into the
		// dispatch switch — container clicks were silently dropped.
		return c.handleContainerClick()
	case v776.PacketPlaySBContainerClose:
		return c.handleContainerClose()
	case v776.PacketPlaySwing:
		// M11: mirror the arm swing to every client tracking us.
		if p := c.player; p != nil {
			c.s.mu.Lock()
			c.s.broadcastSwing(p)
			c.s.mu.Unlock()
		}
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
		y:        float64(c.s.surfaceY(0, 0)),
		z:        0.5,
		yaw:      0,
		pitch:    0,
		radius:   radius,
		seen:     make(map[[2]int32]bool),
		seenEnt:  make(map[int32]bool),
		onGround: true,
		barUUID:  newBarUUID(),

		invMenu: newInventoryMenu(),

		// Window ids start at 1: container 0 is the always-open
		// inventory menu (fixes the M7 zero-value collision that
		// handed transient menus id 0).
		nextWindowID: 1,

		// Survival baseline: full vitals on first spawn.
		health:     maxHealth,
		food:       maxFood,
		saturation: startingSaturation,
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

	// Survival baseline (vanilla placeNewPlayer): attributes, vitals, XP.
	if err := c.sendAttributes(p.id, maxHealth); err != nil {
		return err
	}
	p.sendHealth()
	p.sendExperience()

	// M13: 重放已活跃的状态效果（重进服后客户端 HUD 恢复）。
	c.s.mu.Lock()
	for id, inst := range p.effects {
		c.s.syncPlayerEffectLocked(p, id, inst)
	}
	c.s.mu.Unlock()

	// Starter hotbar (per-slot inventory sync).
	c.sendStarterInventory()

	// M12: sync the current weather so the newcomer sees the same sky.
	c.sendWeather()

	// Full inventory-menu state (vanilla sendAllDataToRemote on initMenu).
	p.invMenu.sendAll(c)

	// Tab list: the newcomer receives everyone; everyone else receives
	// the newcomer (M8.5 multiplayer visibility).
	entry := java.PlayerInfoEntry{
		UUID:     c.profileID,
		Name:     c.username,
		GameMode: 0,
		Listed:   true,
		Latency:  0,
	}
	c.s.mu.Lock()
	entries := []java.PlayerInfoEntry{entry}
	for _, q := range c.s.players {
		if q != p {
			entries = append(entries, java.PlayerInfoEntry{
				UUID:     q.conn.profileID,
				Name:     q.name,
				GameMode: q.gameMode,
				Listed:   true,
				Latency:  0,
			})
		}
	}
	var otherConns []*conn
	for q := range c.s.players {
		if q != c {
			otherConns = append(otherConns, q)
		}
	}
	c.s.mu.Unlock()
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayPlayerInfo)
	java.WritePlayPlayerInfo(c.wr, entries)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}
	if len(otherConns) > 0 {
		addBody := protocol.NewWriter()
		addBody.VarInt(v776.PacketPlayPlayerInfo)
		java.WritePlayPlayerInfo(addBody, []java.PlayerInfoEntry{entry})
		for _, q := range otherConns {
			_ = q.sendPacket(addBody.Bytes())
		}
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

	// World clock sync: overworld clock at the live server time, rate 1.0.
	c.s.mu.Lock()
	gameTime := c.s.timeTicks
	c.s.mu.Unlock()
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlaySetTime)
	java.WritePlaySetTime(c.wr, gameTime, []java.ClockState{{
		ClockID:     c.s.world.clockID("overworld"),
		TotalTicks:  gameTime,
		PartialTick: 0,
		Rate:        1.0,
	}})
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// M16: 计分板快照与世界边界全量包（vanilla placeNewPlayer →
	// updateEntireScoreboard / sendLevelInfo 的 INITIALIZE_BORDER）。
	c.s.mu.Lock()
	c.s.scoreboard.sendFullScoreboard(c)
	c.s.sendBorderInit(c)
	c.s.mu.Unlock()

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
// incremental chunk sync (new sends + unload notifications). The state
// mutation runs under s.mu: the ticker (entity sync, survival) and other
// connections' goroutines read these fields concurrently.
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
	c.s.mu.Lock()
	prevX, prevY, prevZ := p.x, p.y, p.z
	if move.HasPos {
		// M16: 边界钳制——界内玩家不能走出边界（vanilla 通过碰撞形状
		// 拦截，这里钳制目标坐标）；界外玩家仍可自由走回。
		move.X, move.Z = c.s.clampMoveLocked(p, move.X, move.Z)
		p.x, p.y, p.z = move.X, move.Y, move.Z
		if ncx := chunkCoord(p.x); ncx != p.cx {
			p.cx = ncx
			moved = true
		}
		if ncz := chunkCoord(p.z); ncz != p.cz {
			p.cz = ncz
			moved = true
		}
		// M8 survival: landing damage + movement exhaustion. Fall distance
		// accumulates for every gamemode but only survival pays.
		c.s.moveFallDamageLocked(p, prevY, move.Y, move.OnGround)
		p.moveExhaustion(move.X-prevX, move.Z-prevZ)
	}
	// Every move variant (including status-only) carries the ground flag;
	// mining speed depends on it.
	p.onGround = move.OnGround
	if move.HasRot {
		p.yaw, p.pitch = move.Yaw, move.Pitch
	}
	c.s.mu.Unlock()
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
