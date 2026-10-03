package server

// M16 scoreboard tests: model rules, tracking lifecycle, packet wire
// forms, join sync, the death-count hook and the scoreboard.dat
// persistence round trip.

import (
	"testing"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

func TestScoreboardObjectiveModel(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	sb := s.scoreboard

	if !validCriteria("dummy") || !validCriteria("health") || !validCriteria("teamkill.red") || validCriteria("nope") {
		t.Fatal("判据校验错误")
	}
	if !criteriaReadOnly("health") || criteriaReadOnly("dummy") {
		t.Fatal("只读判据分类错误")
	}

	sb.addObjectiveLocked("kills", "totalKillCount", "击杀", 0)
	if _, ok := sb.objectives["kills"]; !ok {
		t.Fatal("目标未创建")
	}
	if sb.removeObjectiveLocked("kills") != true || sb.removeObjectiveLocked("kills") {
		t.Fatal("删除幂等性错误")
	}
}

func TestScoreboardScores(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	sb := s.scoreboard

	sb.addObjectiveLocked("score", "dummy", "分数", 0)
	sb.setScoreLocked("Alice", "score", 10)
	sb.addScoreLocked("Alice", "score", 5)
	if sb.scores["Alice"]["score"] != 15 {
		t.Fatalf("加减分错误: %d", sb.scores["Alice"]["score"])
	}
	if !sb.resetScoreLocked("Alice", "score") {
		t.Fatal("单目标重置应成功")
	}
	if sb.resetScoreLocked("Alice", "score") {
		t.Fatal("重复重置应返回 false")
	}
	sb.setScoreLocked("Alice", "score", 1)
	if !sb.resetScoreLocked("Alice", "") {
		t.Fatal("全目标重置应成功")
	}
	if _, ok := sb.scores["Alice"]; ok {
		t.Fatal("重置后 owner 应被清理")
	}
}

func TestScoreboardTeams(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	sb := s.scoreboard

	if _, err := sb.addTeamLocked("red", "红队"); err != nil {
		t.Fatalf("建队失败: %v", err)
	}
	if _, err := sb.addTeamLocked("red", "重复"); err == nil {
		t.Fatal("重复建队应报错")
	}
	if !sb.joinTeamLocked(sb.teams["red"], "Alice") {
		t.Fatal("加入失败")
	}
	if sb.joinTeamLocked(sb.teams["red"], "Alice") {
		t.Fatal("重复加入应返回 false")
	}
	if sb.teamOfLocked("Alice") != sb.teams["red"] {
		t.Fatal("成员归属查询错误")
	}
	if !sb.leaveTeamLocked("Alice") || sb.leaveTeamLocked("Alice") {
		t.Fatal("离队幂等性错误")
	}
	tt := sb.teams["red"]
	tt.FriendlyFire = true
	tt.Color, tt.ColorSet = java.TeamColorRed, true
	sb.changeTeamLocked(tt)
	if teamOptions(tt) != java.TeamOptionFriendlyFire {
		t.Fatalf("选项位掩码错误: %d", teamOptions(tt))
	}
	sb.removeTeamLocked("red")
	if len(sb.teams) != 0 {
		t.Fatal("删除后不应有队伍")
	}
}

func TestScoreboardDisplayTracking(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	sb := s.scoreboard

	sb.addObjectiveLocked("obj", "dummy", "Obj", 0)
	if sb.tracked["obj"] {
		t.Fatal("未显示的目标不应被追踪")
	}
	sb.setDisplayLocked(java.DisplaySlotSidebar, "obj")
	if !sb.tracked["obj"] {
		t.Fatal("显示后应被追踪")
	}
	sb.setDisplayLocked(java.DisplaySlotSidebar, "")
	if sb.tracked["obj"] {
		t.Fatal("清槽且无引用后应停止追踪")
	}
	sb.setDisplayLocked(java.DisplaySlotBelowName, "obj")
	if !sb.tracked["obj"] {
		t.Fatal("重新显示应重新追踪")
	}
}

func TestScoreboardTeamPacketEncoding(t *testing.T) {
	// method 0 (create): name + method + 参数块 + 成员列表。
	w := protocol.NewWriter()
	java.WritePlaySetPlayerTeam(w, "red", java.ScoreTeamAdd, &java.TeamParams{
		DisplayName: "红队", NameTagVis: java.TeamVisAlways,
		Collision: java.TeamCollisionAlways, ColorPresent: true, Color: java.TeamColorRed,
		Options: java.TeamOptionFriendlyFire,
	}, []string{"Alice", "Bob"})

	r := protocol.NewReader(w.Bytes())
	if name, _ := r.String(64); name != "red" {
		t.Fatal("队名解码错误")
	}
	if m, _ := r.Byte(); m != 0 {
		t.Fatalf("method 应为 0，得到 %d", m)
	}
	// 参数块：3 个组件 + visibility + collision + optional color + options。
	for i := 0; i < 3; i++ {
		if root, _ := r.Byte(); root != 0x0A {
			t.Fatalf("组件 %d 根类型错误: 0x%x", i, root)
		}
		skipNbtPayload(t, r, 0x0A)
	}
	if v, _ := r.VarInt(); v != java.TeamVisAlways {
		t.Fatalf("visibility 错误: %d", v)
	}
	if v, _ := r.VarInt(); v != java.TeamCollisionAlways {
		t.Fatalf("collision 错误: %d", v)
	}
	if ok, _ := r.Bool(); !ok {
		t.Fatal("color 应存在")
	}
	if v, _ := r.VarInt(); v != java.TeamColorRed {
		t.Fatalf("color 错误: %d", v)
	}
	if o, _ := r.Byte(); o != java.TeamOptionFriendlyFire {
		t.Fatalf("options 错误: %d", o)
	}
	if n, _ := r.VarInt(); n != 2 {
		t.Fatalf("成员数错误: %d", n)
	}
	if a, _ := r.String(64); a != "Alice" {
		t.Fatal("成员 Alice 解码错误")
	}
	if b2, _ := r.String(64); b2 != "Bob" {
		t.Fatal("成员 Bob 解码错误")
	}

	// method 3 (join): 单成员、无参数块。
	w = protocol.NewWriter()
	java.WritePlaySetPlayerTeam(w, "red", java.ScoreTeamJoin, nil, []string{"Carol"})
	r = protocol.NewReader(w.Bytes())
	_, _ = r.String(64)
	if m, _ := r.Byte(); m != 3 {
		t.Fatalf("join method 应为 3: %d", m)
	}
	if n, _ := r.VarInt(); n != 1 {
		t.Fatalf("join 成员数错误: %d", n)
	}
}

func TestScoreboardObjectivePacketEncoding(t *testing.T) {
	w := protocol.NewWriter()
	java.WritePlaySetObjective(w, "kills", java.ScoreObjectiveAdd, "击杀数", 1)
	r := protocol.NewReader(w.Bytes())
	if name, _ := r.String(64); name != "kills" {
		t.Fatal("objective 名解码错误")
	}
	if m, _ := r.Byte(); m != java.ScoreObjectiveAdd {
		t.Fatalf("method 错误: %d", m)
	}
	if root, _ := r.Byte(); root != 0x0A {
		t.Fatalf("display 组件根错误: 0x%x", root)
	}
	skipNbtPayload(t, r, 0x0A)
	if rt, _ := r.VarInt(); rt != 1 {
		t.Fatalf("renderType 错误: %d", rt)
	}
	if ok, _ := r.Bool(); ok {
		t.Fatal("numberFormat 应缺席")
	}
	if r.Remaining() != 0 {
		t.Fatalf("包尾多余 %d 字节", r.Remaining())
	}

	// method 1 (remove)：仅 name + method。
	w = protocol.NewWriter()
	java.WritePlaySetObjective(w, "kills", java.ScoreObjectiveRemove, "", 0)
	// body = VarInt 长度前缀(1) + "kills"(5) + method(1)；包号由调用方写。
	if got := len(w.Bytes()); got != 1+len("kills")+1 {
		t.Fatalf("remove 包长度错误: %d", got)
	}
}

func TestScoreboardJoinSync(t *testing.T) {
	s := startTestServer(t)
	joinBotToPlay(t, s, "Seeder")
	// 两个 bot 都在场后做变更：join 快照本身被 joinBotToPlay 的 drain
	// 吞掉，这里验证的是变更广播到达每一个客户端。
	b2 := joinBotToPlay(t, s, "Joiner")

	s.mu.Lock()
	s.scoreboard.addObjectiveLocked("kills", "totalKillCount", "击杀", 0)
	s.scoreboard.setScoreLocked("Seeder", "kills", 3)
	s.scoreboard.setDisplayLocked(java.DisplaySlotSidebar, "kills")
	s.scoreboard.addTeamLocked("red", "红队")
	s.scoreboard.joinTeamLocked(s.scoreboard.teams["red"], "Seeder")
	s.mu.Unlock()

	sawObjective, sawDisplay, sawScore, sawTeam := false, false, false, false
	for i := 0; i < 200; i++ {
		id, rr := b2.next()
		switch id {
		case v776.PacketPlaySetObjective:
			name, _ := rr.String(64)
			method, _ := rr.Byte()
			if name == "kills" && method == java.ScoreObjectiveAdd {
				sawObjective = true
			}
		case v776.PacketPlaySetDisplayObjective:
			slot, _ := rr.VarInt()
			name, _ := rr.String(64)
			if slot == java.DisplaySlotSidebar && name == "kills" {
				sawDisplay = true
			}
		case v776.PacketPlaySetScore:
			owner, _ := rr.String(64)
			obj, _ := rr.String(64)
			score, _ := rr.VarInt()
			if owner == "Seeder" && obj == "kills" && score == 3 {
				sawScore = true
			}
		case v776.PacketPlaySetPlayerTeam:
			name, _ := rr.String(64)
			method, _ := rr.Byte()
			if name == "red" && method == java.ScoreTeamAdd {
				sawTeam = true
			}
		case v776.PacketPlayLevelChunk, v776.PacketPlayChunkBatchStart,
			v776.PacketPlayCBChunkBatchDone, v776.PacketPlayAddEntity,
			v776.PacketPlaySetEntityData, v776.PacketPlaySetEntityMotion,
			v776.PacketPlayMoveEntityPos, v776.PacketPlayMoveEntityPosRot,
			v776.PacketPlayRotateHead, v776.PacketPlayCBKeepAlive,
			v776.PacketPlayEntityEvent, v776.PacketPlaySound,
			v776.PacketPlaySystemChat, v776.PacketPlayPlayerInfo,
			v776.PacketPlayForgetChunk, v776.PacketPlayLevelParticles,
			v776.PacketPlayInitializeBorder:
			continue
		case v776.PacketPlayDisconnect:
			t.Fatal("joiner 被断开")
		}
		if sawObjective && sawDisplay && sawScore && sawTeam {
			return
		}
	}
	t.Fatalf("join 同步缺失: obj=%v disp=%v score=%v team=%v", sawObjective, sawDisplay, sawScore, sawTeam)
}

func TestScoreboardDeathHook(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	s.scoreboard.addObjectiveLocked("deaths", "deathCount", "死亡", 0)
	s.scoreboard.addObjectiveLocked("pk", "playerKillCount", "被玩家杀", 0)
	s.scoreboard.addObjectiveLocked("tk", "totalKillCount", "总击杀", 0)
	s.mu.Unlock()

	joinBotToPlay(t, s, "Victim")
	var p *player
	for _, q := range s.players {
		if q.name == "Victim" {
			p = q
		}
	}
	if p == nil {
		t.Fatal("Victim 未入服")
	}

	// 摔死（无凶手）→ deathCount+1、totalKillCount+1。
	s.mu.Lock()
	s.damagePlayerLocked(p, 1000, v776.DamageTypeFall, -1, -1)
	sb := s.scoreboard
	s.mu.Unlock()
	if sb.scores["Victim"]["deaths"] != 1 || sb.scores["Victim"]["tk"] != 1 {
		t.Fatalf("死亡钩子错误: %+v", sb.scores["Victim"])
	}

	// 玩家攻击致死 → playerKillCount+1（cause 指向另一名玩家）。
	joinBotToPlay(t, s, "Killer")
	joinBotToPlay(t, s, "Victim2")
	var killer, victim2 *player
	for _, q := range s.players {
		switch q.name {
		case "Killer":
			killer = q
		case "Victim2":
			victim2 = q
		}
	}
	if killer == nil || victim2 == nil {
		t.Fatal("bot 未入服")
	}
	s.mu.Lock()
	s.damagePlayerLocked(victim2, 1000, v776.DamageTypePlayerAtk, killer.id, killer.id)
	sb = s.scoreboard
	s.mu.Unlock()
	if sb.scores["Victim2"]["pk"] != 1 {
		t.Fatalf("击杀归属错误: %+v", sb.scores["Victim2"])
	}
}

func TestScoreboardPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := startTestServer(t)

	s.mu.Lock()
	sb := s.scoreboard
	sb.addObjectiveLocked("kills", "totalKillCount", "击杀数", 1)
	sb.addObjectiveLocked("coins", "dummy", "金币", 0)
	sb.setScoreLocked("Alice", "coins", 42)
	sb.setDisplayLocked(java.DisplaySlotSidebar, "coins")
	sb.addTeamLocked("blue", "蓝队")
	sb.joinTeamLocked(sb.teams["blue"], "Alice")
	tt := sb.teams["blue"]
	tt.Prefix = "[蓝]"
	tt.Color, tt.ColorSet = java.TeamColorBlue, true
	sb.changeTeamLocked(tt)
	if err := sb.saveLocked(dir); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	// 清空后回放。
	s.scoreboard = newScoreboard(s)
	s.scoreboard.loadScoreboard(dir)
	sb = s.scoreboard
	s.mu.Unlock()

	if len(sb.objectives) != 2 {
		t.Fatalf("目标数错误: %d", len(sb.objectives))
	}
	if o := sb.objectives["kills"]; o == nil || o.RenderType != 1 || o.Criteria != "totalKillCount" || o.DisplayName != "击杀数" {
		t.Fatalf("kills 回放错误: %+v", o)
	}
	if sb.scores["Alice"]["coins"] != 42 {
		t.Fatalf("分数回放错误: %d", sb.scores["Alice"]["coins"])
	}
	if sb.display[java.DisplaySlotSidebar] != "coins" {
		t.Fatalf("槽位回放错误: %q", sb.display[java.DisplaySlotSidebar])
	}
	if sb.tracked["coins"] != true {
		t.Fatal("回放槽位应标记 tracked")
	}
	if tt := sb.teams["blue"]; tt == nil || tt.Prefix != "[蓝]" || tt.Color != java.TeamColorBlue || !tt.ColorSet || len(tt.Members) != 1 {
		t.Fatalf("队伍回放错误: %+v", tt)
	}
}
