package server

// Server command surface, registered onto the Brigadier-lite tree in
// brigadier.go. Handlers receive the parsed arguments and run with
// whatever locking they need (they are invoked from the connection's
// dispatch goroutine).

import (
	"fmt"
	"log"
	"strconv"
	"strings"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// argParser ids reused from the protocol layer, named for readability.
const (
	pString    = java.ParserString
	pEntity    = java.ParserEntity
	pInteger   = java.ParserInteger
	pVec3      = java.ParserVec3
	pBlockPos  = java.ParserBlockPos
	pBlockSt   = java.ParserBlockState
	pItemStack = java.ParserItemStack
	pGamemode  = java.ParserGamemode
	pTime      = java.ParserTime
	pBool      = java.ParserBool
)

// registerCommands wires the full tree.
func registerCommands(root *cmdNode) {
	root.add(literalf("tps").setExec(cmdTPS))
	root.add(literalf("tpsbar").setExec(cmdTPSBar))
	root.add(literalf("gc").setExec(cmdGC))
	root.add(literalf("help").setExec(cmdHelp))
	root.add(literalf("seed").setExec(cmdSeed))
	root.add(literalf("list").setExec(cmdList))
	root.add(literalf("kill").setExec(cmdKill))

	root.add(literalf("say").add(
		argf("message", "minecraft:message", java.ParserMessage, "message").setExec(cmdSay)))

	give := literalf("give")
	target := argf("target", "minecraft:entity", pEntity, "target")
	item := argf("item", "minecraft:item_stack", pItemStack, "item")
	item.exec = true // give <target> <item>
	item.run = cmdGive
	item.add(argf("count", "brigadier:integer", pInteger, "count").setExec(cmdGive))
	target.add(item)
	give.add(target)
	root.add(give)

	tp := literalf("tp")
	tp.add(argf("location", "minecraft:vec3", pVec3, "location").setExec(cmdTP))
	tp.add(argf("destination", "minecraft:entity", pEntity, "destination").setExec(cmdTPTo))
	root.add(tp)

	root.add(literalf("gamemode").add(
		argf("mode", "minecraft:gamemode", pGamemode, "mode").setExec(cmdGamemode)))

	root.add(literalf("time").add(literalf("set").add(
		argf("ticks", "minecraft:time", pTime, "ticks").setExec(cmdTimeSet))))

	root.add(literalf("setblock").add(
		argf("pos", "minecraft:block_pos", pBlockPos, "pos").add(
			argf("block", "minecraft:block_state", pBlockSt, "block").setExec(cmdSetBlock))))

	// M13: /effect give <target> <effect> [seconds] [amplifier] [hideParticles]
	//      /effect clear [target]
	effect := literalf("effect")
	effectClear := literalf("clear")
	effectClear.setExec(cmdEffectClear)
	effectClearTarget := argf("target", "minecraft:entity", pEntity, "target")
	effectClearTarget.setExec(cmdEffectClear)
	effectClear.add(effectClearTarget)
	effect.add(effectClear)
	effectGive := literalf("give")
	effectGiveTarget := argf("target", "minecraft:entity", pEntity, "target")
	effArg := argf("effect", "brigadier:string", pString, "effect")
	effArg.exec = true
	effArg.run = cmdEffectGive
	effSec := argf("seconds", "brigadier:integer", pInteger, "seconds").setExec(cmdEffectGive)
	effAmp := argf("amplifier", "brigadier:integer", pInteger, "amplifier").setExec(cmdEffectGive)
	effHide := argf("hideParticles", "brigadier:bool", pBool, "hideParticles").setExec(cmdEffectGive)
	effArg.add(effSec)
	effSec.add(effAmp)
	effAmp.add(effHide)
	effectGiveTarget.add(effArg)
	effectGive.add(effectGiveTarget)
	effect.add(effectGive)
	root.add(effect)

	// M16: 计分板/队伍/世界边界。
	registerM16Commands(root)
}

// setExec marks a node runnable.
func (n *cmdNode) setExec(run func(c *conn, args map[string]string) error) *cmdNode {
	n.exec = true
	n.run = run
	return n
}

// resolveTargets expands the entity argument: "@s" the sender, "@a"
// every player, else the exact player name.
func resolveTargets(s *Server, sender *player, val string) []*player {
	switch val {
	case "@s":
		return []*player{sender}
	case "@a", "@e":
		return s.playerListLocked()
	default:
		for _, p := range s.playerListLocked() {
			if p.name == val {
				return []*player{p}
			}
		}
		return nil
	}
}

func cmdTPS(c *conn, _ map[string]string) error {
	return c.sendTpsChat()
}

func cmdTPSBar(c *conn, _ map[string]string) error {
	return c.toggleTpsbar()
}

// cmdGC answers /gc with the tuner readout (or a hint when disabled).
func cmdGC(c *conn, _ map[string]string) error {
	if c.s.gc == nil {
		return c.sendSystemChat("§7智能 GC 调控未启用（server.properties: gc-tuning=true 开启）")
	}
	return c.sendSystemChat(formatGC(c.s.gc.snapshot()))
}

// formatGC renders the /gc text readout.
func formatGC(s gcSnapshot) string {
	lim := "未设置"
	if s.MemLimitMiB > 0 {
		lim = fmt.Sprintf("%dMiB", s.MemLimitMiB)
	}
	return fmt.Sprintf("§6GC 状态: §fGOGC=%d §7(堆 %.1fMB · 上限 %s)\n§7分配 §f%.1fMB/s §7· GC CPU §f%.1f%% §7· P99 暂停 §f%.2fms §7· 已调控 §f%d §7次",
		s.GOGC, s.HeapMB, lim, s.AllocMBPS, s.GCCPUPct, s.PauseP99MS, s.Adjusts)
}

func cmdHelp(c *conn, _ map[string]string) error {
	return c.sendSystemChat("§6可用命令: §f/tps /tpsbar /gc /help /seed /list /say <消息> /give <玩家> <物品> [数量] /tp <x y z|玩家> /gamemode <模式> /time set <tick> /setblock <x y z> <方块>\n" +
		"§6效果与计分: §f/effect give|clear <玩家> [效果] [秒] [倍率]\n" +
		"§6           /scoreboard objectives add|remove|list|display … /scoreboard players set|add|remove|get|reset|enable …\n" +
		"§6           /team add|remove|join|leave|empty|list|option …\n" +
		"§6           /worldborder set|add|center|damage amount|buffer|get|warning distance|time …")
}

func cmdSeed(c *conn, _ map[string]string) error {
	return c.sendSystemChat(fmt.Sprintf("§7种子: §f%d§7", c.s.world.seed))
}

func cmdKill(c *conn, _ map[string]string) error {
	s := c.s
	s.mu.Lock()
	defer s.mu.Unlock()
	p := c.player
	if p == nil || p.dead {
		return nil
	}
	s.damagePlayerLocked(p, p.health+1, v776.DamageTypeGenericKill, -1, -1)
	return nil
}

func cmdList(c *conn, _ map[string]string) error {
	players := c.s.playerListLocked()
	names := make([]string, len(players))
	for i, p := range players {
		names[i] = p.name
	}
	return c.sendSystemChat(fmt.Sprintf("§7在线 %d 人: §f%s", len(names), strings.Join(names, ", ")))
}

func cmdSay(c *conn, args map[string]string) error {
	msg := fmt.Sprintf("§d[%s]§f %s", c.username, args["message"])
	log.Printf(ui.Info("i ")+"%s 广播: %s", c.username, args["message"])
	return c.s.broadcastSystemChat(msg)
}

func cmdGive(c *conn, args map[string]string) error {
	targets := resolveTargets(c.s, c.player, args["target"])
	if len(targets) == 0 {
		return c.sendSystemChat("§c没有找到玩家: " + args["target"])
	}
	name := stripStateSuffix(args["item"])
	itemID, ok := itemIDByName[name]
	if !ok {
		return c.sendSystemChat("§c未知物品: " + name)
	}
	count := int32(1)
	if v, ok := args["count"]; ok {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil {
			count = int32(n)
		}
	}
	c.s.mu.Lock()
	for _, t := range targets {
		left := t.giveItem(itemID, count)
		if left > 0 {
			c.s.spawnPlayerDrop(t, itemID, left)
		}
		t.conn.sendSystemChat(fmt.Sprintf("§7获得 %s × %d", itemNameOf(itemID), count))
	}
	c.s.mu.Unlock()
	return nil
}

// parseVec3 resolves a vec3 argument against the sender position
// (~ relative offsets).
func parseVec3(p *player, val string) (float64, float64, float64, bool) {
	parts := strings.Fields(val)
	if len(parts) != 3 || p == nil {
		return 0, 0, 0, false
	}
	var out [3]float64
	base := [3]float64{p.x, p.y, p.z}
	for i, s := range parts {
		rel := false
		if strings.HasPrefix(s, "~") {
			rel = true
			s = s[1:]
			if s == "" {
				out[i] = base[i]
				continue
			}
		}
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0, 0, 0, false
		}
		if rel {
			out[i] = base[i] + f
		} else {
			out[i] = f
		}
	}
	return out[0], out[1], out[2], true
}

func cmdTP(c *conn, args map[string]string) error {
	p := c.player
	if p == nil {
		return nil
	}
	x, y, z, ok := parseVec3(p, args["location"])
	if !ok {
		return c.sendSystemChat("§c无效坐标: " + args["location"])
	}
	c.s.mu.Lock()
	teleportPlayer(c.s, p, x, y, z)
	c.s.mu.Unlock()
	return c.sendSystemChat(fmt.Sprintf("§7已传送到 §f%.1f %.1f %.1f", x, y, z))
}

func cmdTPTo(c *conn, args map[string]string) error {
	p := c.player
	targets := resolveTargets(c.s, p, args["destination"])
	if p == nil || len(targets) == 0 || targets[0] == p {
		return c.sendSystemChat("§c没有找到目标玩家")
	}
	t := targets[0]
	c.s.mu.Lock()
	teleportPlayer(c.s, p, t.x, t.y, t.z)
	c.s.mu.Unlock()
	return c.sendSystemChat("§7已传送到 " + t.name)
}

// teleportPlayer moves the player and syncs position + chunks.
// Caller holds Server.mu.
func teleportPlayer(s *Server, p *player, x, y, z float64) {
	p.x, p.y, p.z = x, y, z
	p.cx, p.cz = chunkCoord(x), chunkCoord(z)
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayPlayerPosition)
	java.WritePlayPlayerPosition(body, java.ClientboundPlayerPosition{
		TeleportID: s.nextTeleportID(),
		X:          x,
		Y:          y,
		Z:          z,
		Relatives:  0,
	})
	_ = p.conn.sendPacket(body.Bytes())
	go func() {
		_ = p.conn.syncChunks()
	}()
}

func cmdGamemode(c *conn, args map[string]string) error {
	mode := args["mode"]
	var id int32
	switch mode {
	case "survival", "0":
		id = 0
	case "creative", "1":
		id = 1
	case "adventure", "2":
		id = 2
	case "spectator", "3":
		id = 3
	default:
		return c.sendSystemChat("§c未知游戏模式: " + mode)
	}
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayGameEvent)
	java.WritePlayGameEvent(body, 3, float32(id)) // CHANGE_GAME_MODE
	if err := c.sendPacket(body.Bytes()); err != nil {
		return err
	}
	// Creative gets flight; the other modes lose it.
	fly := id == 1
	flySpeed := 0.06
	if fly {
		flySpeed = 0.1
	}
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlayAbilities)
	java.WritePlayAbilities(c.wr, fly, fly, id == 3, false, 0.05, float32(flySpeed))
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}
	c.s.mu.Lock()
	if p := c.player; p != nil {
		p.gameMode = id
	}
	c.s.mu.Unlock()
	return c.sendSystemChat("§7已将游戏模式设为 §f" + mode)
}

func cmdTimeSet(c *conn, args map[string]string) error {
	ticks, err := strconv.ParseInt(args["ticks"], 10, 64)
	if err != nil {
		return c.sendSystemChat("§c无效时间: " + args["ticks"])
	}
	if ticks < 0 {
		ticks = 0
	}
	c.s.mu.Lock()
	c.s.timeTicks = ticks
	for _, p := range c.s.playerListLocked() {
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlaySetTime)
		java.WritePlaySetTime(body, ticks, []java.ClockState{{
			ClockID:     c.s.world.clockID("overworld"),
			TotalTicks:  ticks,
			PartialTick: 0,
			Rate:        1.0,
		}})
		_ = p.conn.sendPacket(body.Bytes())
	}
	c.s.mu.Unlock()
	return c.sendSystemChat(fmt.Sprintf("§7已将时间设为 §f%d", ticks))
}

func cmdSetBlock(c *conn, args map[string]string) error {
	p := c.player
	x, y, z, ok := parseVec3(p, args["pos"])
	if !ok {
		return c.sendSystemChat("§c无效坐标: " + args["pos"])
	}
	block := stripStateSuffix(args["block"])
	state := defaultStateOf(block)
	if state < 0 {
		return c.sendSystemChat("§c未知方块: " + block)
	}
	c.s.mu.Lock()
	if !c.s.world.setBlock(int(x), int(y), int(z), int32(state)) {
		c.s.mu.Unlock()
		return c.sendSystemChat("§c无法设置方块（区块未加载？）")
	}
	c.s.broadcastBlockUpdate(int32(x), int32(y), int32(z), int32(state))
	c.s.mu.Unlock()
	log.Printf(ui.Success("OK ")+"%s setblock %s (%d, %d, %d)", c.username, block, int(x), int(y), int(z))
	return c.sendSystemChat(fmt.Sprintf("§7已设置 §f%s §7(%d, %d, %d)", block, int(x), int(y), int(z)))
}

// cmdEffectGive applies a status effect: /effect give <target> <effect>
// [seconds] [amplifier] [hideParticles]. Seconds default 30 (vanilla
// 1080000s for infinite is not modelled); amplifier is 0-based.
func cmdEffectGive(c *conn, args map[string]string) error {
	targets := resolveTargets(c.s, c.player, args["target"])
	if len(targets) == 0 {
		return c.sendSystemChat("§c没有找到玩家: " + args["target"])
	}
	name := strings.TrimPrefix(stripStateSuffix(args["effect"]), "minecraft:")
	effID, ok := effectIDByName(name)
	if !ok {
		return c.sendSystemChat("§c未知效果: " + args["effect"])
	}
	seconds := int32(30)
	if v, ok := args["seconds"]; ok {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n > 0 {
			seconds = int32(n)
		}
	}
	amp := int32(0)
	if v, ok := args["amplifier"]; ok {
		if n, err := strconv.ParseInt(v, 10, 32); err == nil && n >= 0 && n < 255 {
			amp = int32(n)
		}
	}
	hide := false
	if v, ok := args["hideParticles"]; ok {
		hide = v == "true" || v == "1"
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	for _, t := range targets {
		if t.dead || t.gameMode != 0 {
			continue
		}
		c.s.applyPlayerEffectLocked(t, effID, amp, seconds*20, hide, !hide, true)
	}
	return nil
}

// cmdEffectClear drops every active effect: /effect clear [target].
func cmdEffectClear(c *conn, args map[string]string) error {
	target := args["target"]
	if target == "" {
		target = "@s"
	}
	targets := resolveTargets(c.s, c.player, target)
	if len(targets) == 0 {
		return c.sendSystemChat("§c没有找到玩家: " + target)
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	for _, t := range targets {
		c.s.clearPlayerEffectsLocked(t)
	}
	return nil
}
