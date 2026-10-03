package server

// M16 command surface: /scoreboard, /team and /worldborder. Wire
// behaviour follows the vanilla ScoreboardCommand / TeamCommand /
// WorldBorderCommand (26.2 decompile); messages are localised Chinese
// like the rest of the command set.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/masgzy/gopherite/protocol/java"
)

// registerM16Commands wires the three roots into the tree.
func registerM16Commands(root *cmdNode) {
	root.add(buildWorldBorderCmd())
	root.add(buildScoreboardCmd())
	root.add(buildTeamCmd())
}

// ---- /worldborder -----------------------------------------------------------

func buildWorldBorderCmd() *cmdNode {
	wb := literalf("worldborder")

	add := literalf("add")
	set := literalf("set")
	for _, lit := range []*cmdNode{add, set} {
		dist := argf("distance", "brigadier:double", java.ParserDouble, "distance")
		dist.exec = true
		dist.run = cmdWorldBorderSet
		t := argf("time", "minecraft:time", pTime, "time")
		t.exec = true
		t.run = cmdWorldBorderSet
		dist.add(t)
		lit.add(dist)
	}
	wb.add(add).add(set)

	center := literalf("center")
	center.add(argf("pos", "minecraft:vec2", java.ParserVec2, "pos").setExec(cmdWorldBorderCenter))
	wb.add(center)

	damage := literalf("damage")
	amount := literalf("amount")
	amount.add(argf("damagePerBlock", "brigadier:float", java.ParserFloat, "value").setExec(cmdWorldBorderDamageAmount))
	buffer := literalf("buffer")
	buffer.add(argf("distance", "brigadier:float", java.ParserFloat, "value").setExec(cmdWorldBorderDamageBuffer))
	damage.add(amount).add(buffer)
	wb.add(damage)

	wb.add(literalf("get").setExec(cmdWorldBorderGet))

	warning := literalf("warning")
	wd := literalf("distance")
	wd.add(argf("distance", "brigadier:integer", pInteger, "value").setExec(cmdWorldBorderWarningDistance))
	wt := literalf("time")
	wt.add(argf("time", "minecraft:time", pTime, "value").setExec(cmdWorldBorderWarningTime))
	warning.add(wd).add(wt)
	wb.add(warning)
	return wb
}

// parseTimeArg parses the vanilla time argument: plain ticks or the
// "30d" / "45s" suffixes.
func parseTimeArg(v string) int64 {
	if v == "" {
		return 0
	}
	switch s := v[len(v)-1]; s {
	case 'd', 's', 't':
		if n, err := strconv.ParseInt(v[:len(v)-1], 10, 64); err == nil {
			switch s {
			case 'd':
				return n * 24000
			case 's':
				return n * 20
			default:
				return n
			}
		}
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func cmdWorldBorderSet(c *conn, args map[string]string) error {
	distance, err := strconv.ParseFloat(args["distance"], 64)
	if err != nil {
		return c.sendSystemChat("§c无效距离: " + args["distance"])
	}
	timeTicks := int64(0)
	if v, ok := args["time"]; ok && v != "" {
		timeTicks = parseTimeArg(v)
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if timeTicks > 0 {
		// Vanilla add: lerpTime() + time on top of a running lerp.
		timeTicks += c.s.border.lerpDuration
	}
	if err := c.s.setBorderSizeLocked(distance, timeTicks); err != nil {
		return c.sendSystemChat("§c" + err.Error())
	}
	formatted := fmt.Sprintf("%.1f", distance)
	if timeTicks > 0 {
		if distance > c.s.border.from {
			c.s.broadcastSystemChatLocked(fmt.Sprintf("§7边界将在 §f%s §7秒内扩大至 §f%s", fmt.Sprintf("%.1f", float64(timeTicks)/20), formatted))
		} else {
			c.s.broadcastSystemChatLocked(fmt.Sprintf("§7边界将在 §f%s §7秒内缩小至 §f%s", fmt.Sprintf("%.1f", float64(timeTicks)/20), formatted))
		}
	} else {
		c.s.broadcastSystemChatLocked("§7边界大小立即设为 §f" + formatted)
	}
	return nil
}

func cmdWorldBorderCenter(c *conn, args map[string]string) error {
	parts := strings.Fields(args["pos"])
	if len(parts) != 2 {
		return c.sendSystemChat("§c无效中心: " + args["pos"])
	}
	x, err1 := strconv.ParseFloat(parts[0], 64)
	z, err2 := strconv.ParseFloat(parts[1], 64)
	if err1 != nil || err2 != nil {
		return c.sendSystemChat("§c无效中心: " + args["pos"])
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := c.s.setBorderCenterLocked(x, z); err != nil {
		return c.sendSystemChat("§c" + err.Error())
	}
	c.s.broadcastSystemChatLocked(fmt.Sprintf("§7边界中心设为 §f%.2f %.2f", x, z))
	return nil
}

func cmdWorldBorderDamageAmount(c *conn, args map[string]string) error {
	v, err := strconv.ParseFloat(args["value"], 64)
	if err != nil || v < 0 {
		return c.sendSystemChat("§c无效伤害: " + args["value"])
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.border.damagePerBlock == v {
		return c.sendSystemChat("§c伤害系数已是该值")
	}
	c.s.border.damagePerBlock = v
	return c.sendSystemChat(fmt.Sprintf("§7每格边界伤害设为 §f%.2f", v))
}

func cmdWorldBorderDamageBuffer(c *conn, args map[string]string) error {
	v, err := strconv.ParseFloat(args["value"], 64)
	if err != nil || v < 0 {
		return c.sendSystemChat("§c无效缓冲: " + args["value"])
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.border.safeZone == v {
		return c.sendSystemChat("§c伤害缓冲已是该值")
	}
	c.s.border.safeZone = v
	return c.sendSystemChat(fmt.Sprintf("§7边界伤害缓冲设为 §f%.2f", v))
}

func cmdWorldBorderGet(c *conn, _ map[string]string) error {
	c.s.mu.Lock()
	size := c.s.border.size
	c.s.mu.Unlock()
	return c.sendSystemChat(fmt.Sprintf("§7边界大小: §f%.0f", size))
}

func cmdWorldBorderWarningDistance(c *conn, args map[string]string) error {
	v, err := strconv.ParseInt(args["value"], 10, 32)
	if err != nil || v < 0 {
		return c.sendSystemChat("§c无效距离: " + args["value"])
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.border.warningBlocks == int32(v) {
		return c.sendSystemChat("§c警告距离已是该值")
	}
	c.s.border.warningBlocks = int32(v)
	c.s.syncWarningLocked()
	return c.sendSystemChat(fmt.Sprintf("§7边界警告距离设为 §f%d §7格", v))
}

func cmdWorldBorderWarningTime(c *conn, args map[string]string) error {
	v := parseTimeArg(args["value"])
	if v < 0 {
		return c.sendSystemChat("§c无效时间: " + args["value"])
	}
	secs := v / 20
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.border.warningTime == int32(secs) {
		return c.sendSystemChat("§c警告时间已是该值")
	}
	c.s.border.warningTime = int32(secs)
	c.s.syncWarningLocked()
	return c.sendSystemChat(fmt.Sprintf("§7边界警告时间设为 §f%d §7秒", secs))
}

// ---- /scoreboard -------------------------------------------------------------

func buildScoreboardCmd() *cmdNode {
	sb := literalf("scoreboard")

	objectives := literalf("objectives")

	objAdd := literalf("add")
	name := argWord("name", "name")
	criteria := argWord("criteria", "criteria")
	criteria.exec = true
	criteria.run = cmdScoreObjAdd
	display := argf("displayName", "brigadier:string", pString, "displayName")
	display.exec = true
	display.run = cmdScoreObjAdd
	criteria.add(display)
	name.add(criteria)
	objAdd.add(name)

	objRemove := literalf("remove")
	objRemove.add(argWord("name", "name").setExec(cmdScoreObjRemove))

	objList := literalf("list")
	objList.exec = true
	objList.run = cmdScoreObjList

	objDisplay := literalf("display")
	for _, lit := range []struct {
		node string
		run  func(c *conn, args map[string]string) error
	}{
		{"list", cmdScoreDisplayList},
		{"sidebar", cmdScoreDisplaySidebar},
		{"below_name", cmdScoreDisplayBelowName},
	} {
		slot := literalf(lit.node)
		slot.exec = true
		slot.run = lit.run
		slot.add(argWord("objective", "objective").setExec(lit.run))
		objDisplay.add(slot)
	}
	objDisplay.add(literalf("reset").setExec(cmdScoreObjDisplayReset))

	objectives.add(objAdd).add(objRemove).add(objList).add(objDisplay)
	sb.add(objectives)

	players := literalf("players")

	playersList := literalf("list")
	playersList.exec = true
	playersList.run = cmdScorePlayersList
	playersList.add(argWord("player", "player").setExec(cmdScorePlayersList))

	get := literalf("get")
	get.add(argWord("player", "player")).add(argWord("objective", "objective").setExec(cmdScorePlayersGet))

	set := literalf("set")
	set.add(argWord("player", "player")).add(argWord("objective", "objective")).add(argf("score", "brigadier:integer", pInteger, "score").setExec(cmdScorePlayersSet))

	add := literalf("add")
	add.add(argWord("player", "player")).add(argWord("objective", "objective")).add(argf("score", "brigadier:integer", pInteger, "score").setExec(cmdScorePlayersAdd))

	remove := literalf("remove")
	remove.add(argWord("player", "player")).add(argWord("objective", "objective")).add(argf("score", "brigadier:integer", pInteger, "score").setExec(cmdScorePlayersRemove))

	reset := literalf("reset")
	// Two sibling chains: reset <player> and reset <player> <objective>;
	// tryParse descends every branch and keeps the deepest executable.
	resetPlain := argWord("player", "player")
	resetPlain.exec = true
	resetPlain.run = cmdScorePlayersReset
	resetWithObj := argWord("player", "player")
	resetWithObj.add(argWord("objective", "objective").setExec(cmdScorePlayersReset))
	reset.add(resetPlain).add(resetWithObj)

	enable := literalf("enable")
	enable.add(argWord("player", "player")).add(argWord("objective", "objective").setExec(cmdScorePlayersEnable))

	players.add(playersList).add(get).add(set).add(add).add(remove).add(reset).add(enable)
	sb.add(players)
	return sb
}

// scoreOwnerTargets expands the owner argument: @s/@a resolve to real
// players, anything else is a literal (fake) holder.
func scoreOwnerTargets(s *Server, sender *player, val string) []string {
	switch val {
	case "@s":
		if sender == nil {
			return nil
		}
		return []string{sender.name}
	case "@a", "@e", "@p":
		players := s.playerListLocked()
		out := make([]string, 0, len(players))
		for _, p := range players {
			out = append(out, p.name)
		}
		return out
	}
	return []string{val}
}

func cmdScoreObjAdd(c *conn, args map[string]string) error {
	name, criteria := args["name"], args["criteria"]
	if !validObjectiveName(name) {
		return c.sendSystemChat("§c目标名只能包含字母、数字、下划线、点、加号和减号")
	}
	if !validCriteria(criteria) {
		return c.sendSystemChat("§c未知判据: " + criteria)
	}
	display := args["displayName"]
	if display == "" {
		display = name
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if _, ok := c.s.scoreboard.objectives[name]; ok {
		return c.sendSystemChat("§c已存在名为 " + name + " 的目标")
	}
	c.s.scoreboard.addObjectiveLocked(name, criteria, display, 0)
	return c.sendSystemChat(fmt.Sprintf("§7创建目标 §f%s §7(判据 §f%s§7)", name, criteria))
}

// validObjectiveName mirrors the vanilla objective name pattern.
func validObjectiveName(name string) bool {
	if name == "" {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '+' || r == '-':
		default:
			return false
		}
	}
	return true
}

func cmdScoreObjRemove(c *conn, args map[string]string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if !c.s.scoreboard.removeObjectiveLocked(args["name"]) {
		return c.sendSystemChat("§c没有名为 " + args["name"] + " 的目标")
	}
	return c.sendSystemChat("§7已删除目标 §f" + args["name"])
}

func cmdScoreObjList(c *conn, _ map[string]string) error {
	c.s.mu.Lock()
	objs := c.s.scoreboard.sortedObjectives()
	c.s.mu.Unlock()
	if len(objs) == 0 {
		return c.sendSystemChat("§7共有 §f0 §7个目标")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "§7共有 §f%d §7个目标:", len(objs))
	for _, o := range objs {
		fmt.Fprintf(&b, "\n§f%s§7 (§f%s§7) %s", o.Name, o.Criteria, o.DisplayName)
	}
	return c.sendSystemChat(b.String())
}

// displaySlotFromNode is gone: each slot literal carries its own handler
// (cmdScoreDisplayList / Sidebar / BelowName) because literals never land
// in the parsed args map.

func cmdScoreDisplayList(c *conn, args map[string]string) error {
	return cmdScoreDisplaySlot(c, java.DisplaySlotList, args["objective"])
}

func cmdScoreDisplaySidebar(c *conn, args map[string]string) error {
	return cmdScoreDisplaySlot(c, java.DisplaySlotSidebar, args["objective"])
}

func cmdScoreDisplayBelowName(c *conn, args map[string]string) error {
	return cmdScoreDisplaySlot(c, java.DisplaySlotBelowName, args["objective"])
}

// cmdScoreDisplaySlot points a slot at an objective, or clears it when
// the objective argument is empty.
func cmdScoreDisplaySlot(c *conn, slot int32, objName string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if objName != "" {
		if _, ok := c.s.scoreboard.objectives[objName]; !ok {
			return c.sendSystemChat("§c没有名为 " + objName + " 的目标")
		}
	}
	c.s.scoreboard.setDisplayLocked(slot, objName)
	if objName == "" {
		return c.sendSystemChat("§7已清空该显示槽位")
	}
	return c.sendSystemChat("§7已将该槽位设为显示 §f" + objName)
}

func cmdScoreObjDisplayReset(c *conn, _ map[string]string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	for i := range c.s.scoreboard.display {
		c.s.scoreboard.setDisplayLocked(int32(i), "")
	}
	return c.sendSystemChat("§7已重置全部显示槽位")
}

func cmdScorePlayersList(c *conn, args map[string]string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if owner, ok := args["player"]; ok {
		m := c.s.scoreboard.scores[owner]
		if len(m) == 0 {
			return c.sendSystemChat("§7" + owner + " §7没有任何分数")
		}
		var b strings.Builder
		fmt.Fprintf(&b, "§7%s §7共有 §f%d §7个分数:", owner, len(m))
		for _, objName := range sortedKeys(m) {
			fmt.Fprintf(&b, "\n§f%s§7: §f%d", objName, m[objName])
		}
		return c.sendSystemChat(b.String())
	}
	owners := c.s.scoreboard.sortedOwnersAll()
	return c.sendSystemChat(fmt.Sprintf("§7共有 §f%d §7个被追踪的对象", len(owners)))
}

// sortedKeys orders a string→int map (deterministic readouts).
func sortedKeys(m map[string]int32) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Insertion-order-agnostic sort for stable output.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func cmdScorePlayersGet(c *conn, args map[string]string) error {
	owner, objName := args["player"], args["objective"]
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	obj, ok := c.s.scoreboard.objectives[objName]
	if !ok {
		return c.sendSystemChat("§c没有名为 " + objName + " 的目标")
	}
	m, ok := c.s.scoreboard.scores[owner]
	if !ok {
		return c.sendSystemChat("§7" + owner + " §7没有 " + objName + " 的分数")
	}
	v, ok := m[objName]
	if !ok {
		return c.sendSystemChat("§7" + owner + " §7没有 " + objName + " 的分数")
	}
	_ = obj
	return c.sendSystemChat(fmt.Sprintf("§7%s §7的 §f%s §7为 §f%d", owner, objName, v))
}

// scoreWriteCheck validates the objective exists and is writable.
func scoreWriteCheck(sb *Scoreboard, objName string) error {
	obj, ok := sb.objectives[objName]
	if !ok {
		return fmt.Errorf("没有名为 %s 的目标", objName)
	}
	if obj.ReadOnly {
		return fmt.Errorf("目标 %s 使用只读判据 %s", objName, obj.Criteria)
	}
	return nil
}

func cmdScorePlayersSet(c *conn, args map[string]string) error {
	score, _ := strconv.ParseInt(args["score"], 10, 32)
	objName := args["objective"]
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := scoreWriteCheck(c.s.scoreboard, objName); err != nil {
		return c.sendSystemChat("§c" + err.Error())
	}
	for _, owner := range scoreOwnerTargets(c.s, c.player, args["player"]) {
		c.s.scoreboard.setScoreLocked(owner, objName, int32(score))
	}
	return c.sendSystemChat(fmt.Sprintf("§7将 §f%s §7的 §f%s §7设为 §f%d", args["player"], objName, score))
}

func cmdScorePlayersAdd(c *conn, args map[string]string) error {
	score, _ := strconv.ParseInt(args["score"], 10, 32)
	objName := args["objective"]
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := scoreWriteCheck(c.s.scoreboard, objName); err != nil {
		return c.sendSystemChat("§c" + err.Error())
	}
	for _, owner := range scoreOwnerTargets(c.s, c.player, args["player"]) {
		c.s.scoreboard.addScoreLocked(owner, objName, int32(score))
	}
	return c.sendSystemChat(fmt.Sprintf("§7将 §f%s §7的 §f%s §7增加 §f%d", args["player"], objName, score))
}

func cmdScorePlayersRemove(c *conn, args map[string]string) error {
	score, _ := strconv.ParseInt(args["score"], 10, 32)
	objName := args["objective"]
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if err := scoreWriteCheck(c.s.scoreboard, objName); err != nil {
		return c.sendSystemChat("§c" + err.Error())
	}
	for _, owner := range scoreOwnerTargets(c.s, c.player, args["player"]) {
		c.s.scoreboard.addScoreLocked(owner, objName, -int32(score))
	}
	return c.sendSystemChat(fmt.Sprintf("§7将 §f%s §7的 §f%s §7减少 §f%d", args["player"], objName, score))
}

func cmdScorePlayersReset(c *conn, args map[string]string) error {
	owner, objName := args["player"], args["objective"]
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	n := 0
	for _, o := range scoreOwnerTargets(c.s, c.player, owner) {
		if c.s.scoreboard.resetScoreLocked(o, objName) {
			n++
		}
	}
	if n == 0 {
		return c.sendSystemChat("§7没有可重置的分数")
	}
	return c.sendSystemChat("§7已重置分数")
}

func cmdScorePlayersEnable(c *conn, args map[string]string) error {
	// trigger semantics: enabling is only meaningful for the trigger
	// criteria; M16 grants the score once (set to 0) so /trigger works
	// client-side.
	objName := args["objective"]
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	obj, ok := c.s.scoreboard.objectives[objName]
	if !ok {
		return c.sendSystemChat("§c没有名为 " + objName + " 的目标")
	}
	if obj.Criteria != "trigger" {
		return c.sendSystemChat("§c只能启用 trigger 判据的目标")
	}
	for _, owner := range scoreOwnerTargets(c.s, c.player, args["player"]) {
		if _, ok := c.s.scoreboard.scores[owner][objName]; !ok {
			c.s.scoreboard.setScoreLocked(owner, objName, 0)
		}
	}
	return c.sendSystemChat("§7已启用 " + args["player"] + " 的 " + objName)
}

// ---- /team -------------------------------------------------------------------

func buildTeamCmd() *cmdNode {
	team := literalf("team")

	add := literalf("add")
	add.add(argWord("name", "name").setExec(cmdTeamAdd))
	addName := argWord("name", "name")
	display := argf("displayName", "brigadier:string", pString, "displayName")
	display.exec = true
	display.run = cmdTeamAdd
	addName.add(display)
	add.add(addName)

	remove := literalf("remove")
	remove.add(argWord("name", "name").setExec(cmdTeamRemove))

	list := literalf("list")
	list.exec = true
	list.run = cmdTeamList
	list.add(argWord("name", "name").setExec(cmdTeamList))

	join := literalf("join")
	joinTeam := argWord("name", "name")
	joinTeam.exec = true
	joinTeam.run = cmdTeamJoin
	members := argf("members", "brigadier:string", pString, "members")
	members.exec = true
	members.run = cmdTeamJoin
	joinTeam.add(members)
	join.add(joinTeam)

	leave := literalf("leave")
	leave.add(argf("members", "brigadier:string", pString, "members").setExec(cmdTeamLeave))

	empty := literalf("empty")
	empty.add(argWord("name", "name").setExec(cmdTeamEmpty))

	option := literalf("option")
	optTeam := argWord("name", "name")
	opt := argWord("option", "option")
	opt.exec = true
	opt.run = cmdTeamOption
	value := argf("value", "brigadier:string", pString, "value")
	value.exec = true
	value.run = cmdTeamOption
	opt.add(value)
	optTeam.add(opt)
	option.add(optTeam)

	team.add(add).add(remove).add(list).add(join).add(leave).add(empty).add(option)
	return team
}

func cmdTeamAdd(c *conn, args map[string]string) error {
	name := args["name"]
	if !validObjectiveName(name) {
		return c.sendSystemChat("§c队伍名只能包含字母、数字、下划线、点、加号和减号")
	}
	display := args["displayName"]
	if display == "" {
		display = name
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if _, err := c.s.scoreboard.addTeamLocked(name, display); err != nil {
		return c.sendSystemChat("§c" + err.Error())
	}
	return c.sendSystemChat("§7已创建队伍 §f" + name)
}

func cmdTeamRemove(c *conn, args map[string]string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if !c.s.scoreboard.removeTeamLocked(args["name"]) {
		return c.sendSystemChat("§c没有名为 " + args["name"] + " 的队伍")
	}
	return c.sendSystemChat("§7已删除队伍 §f" + args["name"])
}

func cmdTeamList(c *conn, args map[string]string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	sb := c.s.scoreboard
	if name, ok := args["name"]; ok {
		t, ok := sb.teams[name]
		if !ok {
			return c.sendSystemChat("§c没有名为 " + name + " 的队伍")
		}
		if len(t.Members) == 0 {
			return c.sendSystemChat("§7队伍 §f" + name + " §7没有成员")
		}
		return c.sendSystemChat(fmt.Sprintf("§7队伍 §f%s §7成员 (§f%d§7): §f%s", name, len(t.Members), strings.Join(t.Members, ", ")))
	}
	teams := sb.sortedTeams()
	if len(teams) == 0 {
		return c.sendSystemChat("§7共有 §f0 §7支队伍")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "§7共有 §f%d §7支队伍:", len(teams))
	for _, t := range teams {
		fmt.Fprintf(&b, "\n§f%s §7(§f%d §7名成员)", t.Name, len(t.Members))
	}
	return c.sendSystemChat(b.String())
}

func cmdTeamJoin(c *conn, args map[string]string) error {
	teamName := args["name"]
	rawMembers := args["members"]
	if rawMembers == "" {
		rawMembers = "@s"
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	t, ok := c.s.scoreboard.teams[teamName]
	if !ok {
		return c.sendSystemChat("§c没有名为 " + teamName + " 的队伍")
	}
	n := 0
	for _, raw := range strings.Fields(rawMembers) {
		for _, m := range scoreOwnerTargets(c.s, c.player, raw) {
			if c.s.scoreboard.joinTeamLocked(t, m) {
				n++
			}
		}
	}
	return c.sendSystemChat(fmt.Sprintf("§7已将 §f%d §7名成员加入 §f%s", n, teamName))
}

func cmdTeamLeave(c *conn, args map[string]string) error {
	raw := args["members"]
	if raw == "" {
		raw = "@s"
	}
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	n := 0
	for _, raw := range strings.Fields(raw) {
		for _, m := range scoreOwnerTargets(c.s, c.player, raw) {
			if c.s.scoreboard.leaveTeamLocked(m) {
				n++
			}
		}
	}
	return c.sendSystemChat(fmt.Sprintf("§7已将 §f%d §7名成员移出队伍", n))
}

func cmdTeamEmpty(c *conn, args map[string]string) error {
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	t, ok := c.s.scoreboard.teams[args["name"]]
	if !ok {
		return c.sendSystemChat("§c没有名为 " + args["name"] + " 的队伍")
	}
	n := len(t.Members)
	for _, m := range append([]string(nil), t.Members...) {
		c.s.scoreboard.leaveTeamLocked(m)
	}
	return c.sendSystemChat(fmt.Sprintf("§7已从 §f%s §7移出 §f%d §7名成员", t.Name, n))
}

func cmdTeamOption(c *conn, args map[string]string) error {
	teamName, option, value := args["name"], args["option"], args["value"]
	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	t, ok := c.s.scoreboard.teams[teamName]
	if !ok {
		return c.sendSystemChat("§c没有名为 " + teamName + " 的队伍")
	}
	switch option {
	case "color":
		color := teamColorByName(value)
		if color < 0 {
			return c.sendSystemChat("§c未知颜色: " + value)
		}
		t.Color, t.ColorSet = color, true
	case "prefix":
		t.Prefix = value
	case "suffix":
		t.Suffix = value
	case "friendlyFire":
		if value != "true" && value != "false" {
			return c.sendSystemChat("§c友伤开关只接受 true/false")
		}
		t.FriendlyFire = value == "true"
	case "seeFriendlyInvisibles":
		if value != "true" && value != "false" {
			return c.sendSystemChat("§c可见开关只接受 true/false")
		}
		t.SeeInvis = value == "true"
	case "nametagVisibility":
		id, ok := visibilityIDStrict(value)
		if !ok {
			return c.sendSystemChat("§c未知可见性: " + value)
		}
		t.NameTagVis = id
	case "collisionRule":
		id, ok := collisionIDStrict(value)
		if !ok {
			return c.sendSystemChat("§c未知碰撞规则: " + value)
		}
		t.Collision = id
	case "deathMessageVisibility":
		id, ok := visibilityIDStrict(value)
		if !ok {
			return c.sendSystemChat("§c未知可见性: " + value)
		}
		t.DeathVis = id
	default:
		return c.sendSystemChat("§c未知选项: " + option)
	}
	c.s.scoreboard.changeTeamLocked(t)
	return c.sendSystemChat(fmt.Sprintf("§7队伍 §f%s §7的 §f%s §7已更新", teamName, option))
}
