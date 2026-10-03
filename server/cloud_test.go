package server

import (
	"math"
	"testing"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M14 区域效果云测试。生命周期与数值对照 26.2 反编译源
// （AreaEffectCloud.java / ThrownLingeringPotion.java）。

func newCloudTestPlayer() *player {
	return &player{health: 10, food: 20, gameMode: 0}
}

// 药水颜色：多效果按 (amp+1) 加权平均；无效果回退缺省色。
func TestPotionColorWeightedAverage(t *testing.T) {
	// turtle_master = slowness(amp 3) + resistance(amp 2)：权重 4 和 3。
	slowness := effectColors[1]
	resistance := effectColors[10]
	wantR := (4*((slowness>>16)&0xFF) + 3*((resistance>>16)&0xFF)) / 7
	wantG := (4*((slowness>>8)&0xFF) + 3*((resistance>>8)&0xFF)) / 7
	wantB := (4*(slowness&0xFF) + 3*(resistance&0xFF)) / 7
	id, ok := potionIDByName("turtle_master")
	if !ok {
		t.Fatal("turtle_master 应存在于药水注册表")
	}
	c := uint32(potionColor(id))
	if r, g, b := (c>>16)&0xFF, (c>>8)&0xFF, c&0xFF; r != uint32(wantR) || g != uint32(wantG) || b != uint32(wantB) {
		t.Fatalf("turtle_master 颜色应按权重平均：got #%08x want r=%d g=%d b=%d", c, wantR, wantG, wantB)
	}
	water, _ := potionIDByName("water")
	if potionColor(water) != v776.PotionDefaultColor {
		t.Fatalf("无效果药水应回退缺省色，实际 %d", potionColor(water))
	}
}

// 出生参数 = ThrownLingeringPotion.onHitAsPotion 的原版数值。
func TestCloudSpawnParameters(t *testing.T) {
	e := newCloudEntity(1, 8.5, 64, -3.25, 13 /* swiftness */)
	if e.radius != 3.0 || e.duration != 600 || e.waitTime != 10 || e.radiusOnUse != -0.5 {
		t.Fatalf("出生参数不符：r=%v dur=%v wait=%v rUse=%v", e.radius, e.duration, e.waitTime, e.radiusOnUse)
	}
	if e.radiusPerTick != -3.0/600.0 {
		t.Fatalf("radiusPerTick 应为 -3/600，实际 %v", e.radiusPerTick)
	}
	if e.color != potionColor(13) {
		t.Fatal("云颜色应取药水加权色")
	}
}

// 生命周期：等待期不收缩，结束后每 tick 收缩，半径耗尽消散。
func TestCloudLifecycleShrinkAndDiscard(t *testing.T) {
	s := potionTestServer(t)
	e := newCloudEntity(1, 0, 64, 0, 13)
	s.mu.Lock()
	s.entities[1] = e
	s.mu.Unlock()

	// vanilla：super.tick 先自增 tickCount，故第 10 tick 起等待结束。
	for i := 0; i < 9; i++ {
		e.tick(s)
	}
	e.mu.Lock()
	if !e.waiting {
		e.mu.Unlock()
		t.Fatal("前 9 tick 内 waiting 应为 true")
	}
	if e.radius != 3.0 {
		e.mu.Unlock()
		t.Fatalf("等待期内半径不应收缩，实际 %v", e.radius)
	}
	e.mu.Unlock()
	e.tick(s) // tickCount = 10：等待结束，开始收缩
	e.mu.Lock()
	if e.waiting || e.radius >= 3.0 {
		r := e.radius
		e.mu.Unlock()
		t.Fatalf("tickCount 10 应脱离等待并开始收缩：waiting=%v r=%v", e.waiting, r)
	}
	e.mu.Unlock()

	// 半径从 3.0 收缩至 0.5 需要 500 次收缩；float32 逐 tick 累加的舍入
	// 与 vanilla 一致，消散会浮动在目标 tick 附近，断言取区间。
	for i := 0; i < 700 && e.alive(); i++ {
		e.tick(s)
	}
	e.mu.Lock()
	deathTick := e.tickCount
	e.mu.Unlock()
	if e.alive() {
		t.Fatal("半径收缩耗尽后云应消散")
	}
	if deathTick < 500 || deathTick > 550 {
		t.Fatalf("收缩期应约 500 tick（理想 509），实际 %d", deathTick)
	}
	// 消散应早于 duration 到期（tickCount 610 的 duration 分支）。
	if deathTick >= e.duration+e.waitTime {
		t.Fatal("半径收缩应先于 duration 触发消散")
	}
}

// 施加通路：等待期后、扫描 tick 上，云内的玩家获得效果；20t 内不重复。
// 半径同时受 radiusPerTick（每 tick）与 radiusOnUse（每次施加）叠加。
func TestCloudAppliesEffectsAndVictimWindow(t *testing.T) {
	s := potionTestServer(t)
	p := newCloudTestPlayer()
	p.conn = &conn{s: s}
	p.x, p.y, p.z = 0.2, 64, 0
	e := newCloudEntity(1, 0, 64, 0, 13 /* swiftness 3600t */)
	s.mu.Lock()
	s.entities[1] = e
	s.players[p.conn] = p
	s.mu.Unlock()

	// 推进 15 tick：10 等待 + 5 到达第一个 5 的倍数扫描点（tickCount 15）。
	for i := 0; i < 15; i++ {
		e.tick(s)
	}
	s.consumeCloudTicks()
	if !p.hasEffect(0) {
		t.Fatal("云内玩家应获得速度效果")
	}
	if amp := p.amplifierOf(0); amp != 0 {
		t.Fatalf("速度等级应为 0，实际 %d", amp)
	}
	// 半径：3.0 - 6×0.005（tickCount 10..15 的收缩） - 0.5（radiusOnUse）。
	e.mu.Lock()
	r1 := e.radius
	e.mu.Unlock()
	if math.Abs(float64(r1-2.47)) > 1e-3 {
		t.Fatalf("首次施加后半径应为 2.47，实际 %v", r1)
	}

	// tickCount 20/25 仍在免疫窗（截止 15+20=35）内，不重复施加。
	for i := 0; i < 10; i++ {
		e.tick(s)
	}
	s.consumeCloudTicks()
	e.mu.Lock()
	r2 := e.radius
	e.mu.Unlock()
	if math.Abs(float64(r2-2.42)) > 1e-3 {
		t.Fatalf("免疫窗内不应重复施加（仅每 tick 收缩），半径应为 2.42，实际 %v", r2)
	}

	// 推进至 tickCount 35 的扫描点：免疫窗（age>=35）解除，再次施加。
	for i := 0; i < 10; i++ {
		e.tick(s)
	}
	s.consumeCloudTicks()
	e.mu.Lock()
	r3 := e.radius
	e.mu.Unlock()
	// 半径：2.42 - 10×0.005 - 0.5 = 1.87。
	if math.Abs(float64(r3-1.87)) > 1e-3 {
		t.Fatalf("免疫窗过后应再次施加，半径应为 1.87，实际 %v", r3)
	}
}

// 瞬时效果按 0.5 效力结算（instant_damage amp0 → (int)(0.5×6+0.5)=3）。
func TestCloudInstantPotency(t *testing.T) {
	s := potionTestServer(t)
	p := newCloudTestPlayer()
	p.conn = &conn{s: s}
	p.health = 10
	p.x, p.y, p.z = 0, 64, 0
	e := newCloudEntity(1, 0, 64, 0, 26 /* harming：instant_damage amp0 */)
	s.mu.Lock()
	s.entities[1] = e
	s.players[p.conn] = p
	s.mu.Unlock()
	for i := 0; i < 15; i++ {
		e.tick(s)
	}
	s.consumeCloudTicks()
	if p.health != 7 {
		t.Fatalf("0.5 效力的瞬间伤害应造成 3 点伤害，实际生命 %v", p.health)
	}
}

// 滞留药水撞击 → 效果云实体入表；喷溅仍走 applySplashAt。
func TestLingeringImpactSpawnsCloud(t *testing.T) {
	s := potionTestServer(t)
	pot := newThrownPotion(s, newCloudTestPlayer(), 13, true)
	pot.mu.Lock()
	pot.impacted = true
	pot.x, pot.y, pot.z = 3, 64, 4
	pot.mu.Unlock()
	s.entities[pot.id] = pot
	s.consumePotionImpacts() // 自取 Server.mu
	_, hasCloud := func() (*cloudEntity, bool) {
		s.mu.Lock()
		defer s.mu.Unlock()
		return findCloudLocked(s)
	}()
	if !hasCloud {
		t.Fatal("滞留药水撞击应生成 area_effect_cloud 实体")
	}
	if pot.alive() {
		t.Fatal("撞击后药水实体应被移除")
	}
}

// findCloudLocked returns the first cloud entity in the registry
// (caller holds s.mu).
func findCloudLocked(s *Server) (*cloudEntity, bool) {
	for _, e := range s.entities {
		if c, ok := e.(*cloudEntity); ok {
			return c, true
		}
	}
	return nil, false
}

// 云元数据：半径/等待/粒子三段 + 0xFF 结尾，粒子为 entity_effect+ARGB。
func TestCloudMetadataEncoding(t *testing.T) {
	e := newCloudEntity(7, 0, 64, 0, 13)
	w := protocol.NewWriter()
	writeCloudMetadata(w, e)
	r := protocol.NewReader(w.Bytes())
	if id, _ := r.VarInt(); int(id) != v776.PacketPlaySetEntityData {
		t.Fatalf("应写入 set_entity_data 包 ID，实际 %d", id)
	}
	if eid, _ := r.VarInt(); eid != 7 {
		t.Fatalf("实体 ID 应为 7，实际 %d", eid)
	}
	if idx, _ := r.Byte(); idx != v776.MetaIndexCloudRadius {
		t.Fatalf("首段索引应为 %d，实际 %d", v776.MetaIndexCloudRadius, idx)
	}
	if ser, _ := r.VarInt(); ser != v776.MetaSerializerFloat {
		t.Fatal("半径序列化器应为 Float")
	}
	if val, _ := r.Float(); val != 3.0 {
		t.Fatalf("半径应为 3.0，实际 %v", val)
	}
	if idx, _ := r.Byte(); idx != v776.MetaIndexCloudWaiting {
		t.Fatalf("次段索引应为 %d，实际 %d", v776.MetaIndexCloudWaiting, idx)
	}
	if ser, _ := r.VarInt(); ser != v776.MetaSerializerBool {
		t.Fatal("等待序列化器应为 Bool")
	}
	if val, _ := r.Bool(); val {
		t.Fatal("出生时 waiting 应为 false")
	}
	if idx, _ := r.Byte(); idx != v776.MetaIndexCloudParticle {
		t.Fatalf("末段索引应为 %d，实际 %d", v776.MetaIndexCloudParticle, idx)
	}
	if ser, _ := r.VarInt(); ser != v776.MetaSerializerParticle {
		t.Fatal("粒子序列化器应为 16")
	}
	if pid, _ := r.VarInt(); int(pid) != v776.ParticleEntityEffect {
		t.Fatalf("粒子类型应为 entity_effect(%d)，实际 %d", v776.ParticleEntityEffect, pid)
	}
	if col, _ := r.Int32(); col != potionColor(13) {
		t.Fatalf("粒子颜色应为药水色，实际 %d", col)
	}
	if term, _ := r.Byte(); term != 0xFF {
		t.Fatal("元数据应以 0xFF 结尾")
	}
}

// 药水箭元数据：potion>0 时带颜色段；普通箭无差异不发包。
func TestArrowMetadataEncoding(t *testing.T) {
	tipped := &arrowEntity{id: 9, potion: 13 + 1}
	w := protocol.NewWriter()
	writeArrowMetadata(w, tipped)
	r := protocol.NewReader(w.Bytes())
	if id, _ := r.VarInt(); int(id) != v776.PacketPlaySetEntityData {
		t.Fatal("药水箭应写入 set_entity_data")
	}
	_, _ = r.VarInt() // entity id
	if idx, _ := r.Byte(); idx != v776.MetaIndexArrowEffectColor {
		t.Fatalf("药水箭应写颜色段（索引 11），实际 %d", idx)
	}
	if ser, _ := r.VarInt(); ser != v776.MetaSerializerVarInt {
		t.Fatal("颜色序列化器应为 VarInt")
	}
	if col, _ := r.VarInt(); col != potionColor(13) {
		t.Fatalf("颜色应为药水色，实际 %d", col)
	}

	plain := &arrowEntity{id: 10}
	w2 := protocol.NewWriter()
	writeArrowMetadata(w2, plain)
	if w2.Len() != 0 {
		t.Fatal("普通箭不应发送元数据")
	}
}

// 药水箭插墙 600t 褪色回普通箭。
func TestTippedArrowGroundDecay(t *testing.T) {
	s := potionTestServer(t)
	a := &arrowEntity{id: 3, stuck: true, potion: 13 + 1, fromPlayer: true}
	s.mu.Lock()
	s.entities[3] = a
	s.mu.Unlock()
	for i := 0; i < arrowPotionDecay; i++ {
		a.tick(s)
	}
	a.mu.Lock()
	decayed := a.potion == 0
	a.mu.Unlock()
	if !decayed {
		t.Fatal("插墙 600t 后药水应褪色")
	}
}

// giveItemPotion：药水栈不与普通栈合并（组件相等性）。
func TestGiveItemPotionNoCrossStacking(t *testing.T) {
	s := potionTestServer(t)
	p := newCloudTestPlayer()
	p.conn = &conn{s: s}
	s.mu.Lock()
	p.slots[0] = invSlot{item: itemIDByName["minecraft:tipped_arrow"], count: 32}
	left1 := p.giveItemPotion(itemIDByName["minecraft:tipped_arrow"], 10, 13+1)
	left2 := p.giveItemPotion(itemIDByName["minecraft:tipped_arrow"], 5, 0)
	s.mu.Unlock()
	if left1 != 0 || left2 != 0 {
		t.Fatalf("两种入包都应成功，实际 %d/%d", left1, left2)
	}
	// 药水箭（potion=14）不得并入普通栈：独立成栈在 slot 1；普通 5 支
	// 则正常并入 slot 0（同为无组件栈）。
	if p.slots[0].count != 37 || p.slots[0].potion != 0 {
		t.Fatalf("普通箭应并入 slot 0 至 37 支，实际 %+v", p.slots[0])
	}
	if p.slots[1].count != 10 || p.slots[1].potion != 14 {
		t.Fatalf("药水箭应独立成栈（10 支、potion=14），实际 %+v", p.slots[1])
	}
}
