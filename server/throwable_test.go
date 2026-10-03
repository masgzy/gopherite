package server

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M15 投掷物测试。数值与行为对照 26.2 反编译源
// （ThrowableProjectile / Snowball / ThrownEgg / ThrownEnderpearl）。

// launch jitter contributes at most ±0.0172275·1.5 per axis, so the
// squared launch speed of a level throw stays within ~0.12 of 2.25.
const throwSpeed2Tolerance = 0.12

// TestThrowableTypeIDs 验证 26.2 权威实体 ID（register 声明序核验）。
func TestThrowableTypeIDs(t *testing.T) {
	if v776.EntityTypeSnowball != 120 || v776.EntityTypeEgg != 39 || v776.EntityTypeEnderPearl != 44 {
		t.Fatalf("实体 ID 应为 snowball=120/egg=39/ender_pearl=44，实际 %d/%d/%d",
			v776.EntityTypeSnowball, v776.EntityTypeEgg, v776.EntityTypeEnderPearl)
	}
	s, _, p := joinedBot(t, "Thrower")
	cases := map[throwableKind]int32{
		throwSnowball: v776.EntityTypeSnowball,
		throwEgg:      v776.EntityTypeEgg,
		throwPearl:    v776.EntityTypeEnderPearl,
	}
	for kind, want := range cases {
		s.mu.Lock()
		e := newThrowable(s, p, kind)
		s.mu.Unlock()
		if e.typeID() != want {
			t.Fatalf("kind %d typeID = %d, want %d", kind, e.typeID(), want)
		}
	}
}

// TestThrowableLaunchSpeedAndPhysics 验证 spawnProjectileFromRotation 的
// 威力 1.5 与弹道常数（重力 0.03、阻力 0.99）：水平直视速度模长平方
// ≈2.25（散布容差内），一 tick 后水平分量乘 0.99、vy 递减 0.03。
func TestThrowableLaunchSpeedAndPhysics(t *testing.T) {
	s, _, p := joinedBot(t, "Physicist")
	s.mu.Lock()
	p.pitch = 0 // 水平直视
	e := newThrowable(s, p, throwSnowball)
	s.entities[e.id] = e
	s.mu.Unlock()

	e.mu.Lock()
	speed2 := e.vx*e.vx + e.vy*e.vy + e.vz*e.vz
	h0 := e.vx*e.vx + e.vz*e.vz
	v0 := e.vy
	e.mu.Unlock()
	if math.Abs(speed2-2.25) > throwSpeed2Tolerance {
		t.Fatalf("水平投掷速度平方应为 ~2.25，实际 %v", speed2)
	}

	e.tick(s)
	e.mu.Lock()
	h2 := e.vx*e.vx + e.vz*e.vz
	vy := e.vy
	e.mu.Unlock()
	if math.Abs(h2-h0*0.99*0.99) > 1e-9 {
		t.Fatalf("水平分量应乘 0.99：初 %v 现 %v", h0, h2)
	}
	// vy：先乘 0.99 再减重力，散布抖动由 v0 基线吸收。
	if math.Abs(vy-(v0*throwableInertia-throwableGravity)) > 1e-9 {
		t.Fatalf("一 tick 后 vy 应为 v0·0.99-0.03，实际 %v（v0=%v）", vy, v0)
	}
}

// TestThrowableSurvivalConsumption 生存模式投掷扣件：16 个雪球掷一次剩 15，
// 并生成一个飞行实体。
func TestThrowableSurvivalConsumption(t *testing.T) {
	s, _, p := joinedBot(t, "Consumer")
	snow := id(t, "minecraft:snowball")
	s.mu.Lock()
	p.slots[0] = invSlot{item: snow, count: 16}
	p.heldSlot = 0
	s.mu.Unlock()

	p.conn.useItemStart(0)

	s.mu.Lock()
	count := p.slots[0].count
	var flight *throwableEntity
	for _, e := range s.entities {
		if t2, ok := e.(*throwableEntity); ok {
			flight = t2
		}
	}
	s.mu.Unlock()
	if count != 15 {
		t.Fatalf("生存投掷后应剩 15 个，实际 %d", count)
	}
	if flight == nil {
		t.Fatal("投掷后应有雪球实体")
	}
}

// TestThrowableCreativeNoConsume 创造模式：投掷不扣件（vanilla
// ItemStack.consume 对 creative 是 no-op），药水同理（M13 路径的修正）。
func TestThrowableCreativeNoConsume(t *testing.T) {
	s, _, p := joinedBot(t, "Creator")
	s.mu.Lock()
	p.gameMode = 1
	p.slots[0] = invSlot{item: id(t, "minecraft:egg"), count: 12}
	p.slots[1] = invSlot{item: id(t, "minecraft:splash_potion"), count: 3, potion: 14}
	p.heldSlot = 0
	s.mu.Unlock()

	p.conn.useItemStart(0)
	s.mu.Lock()
	eggCount := p.slots[0].count
	s.mu.Unlock()
	if eggCount != 12 {
		t.Fatalf("创造投掷不应扣件，实际剩 %d", eggCount)
	}

	s.mu.Lock()
	p.heldSlot = 1
	s.mu.Unlock()
	p.conn.useItemStart(1)
	s.mu.Lock()
	potionCount := p.slots[1].count
	potions := 0
	for _, e := range s.entities {
		if _, ok := e.(*potionEntity); ok {
			potions++
		}
	}
	s.mu.Unlock()
	if potionCount != 3 {
		t.Fatalf("创造掷药水不应扣件，实际剩 %d", potionCount)
	}
	if potions == 0 {
		t.Fatal("创造模式应能掷出药水实体（M13 修正）")
	}
}

// joinedSecondBot joins a second player onto an existing test server
// (joinedBot always boots its own server).
func joinedSecondBot(t *testing.T, s *Server, name string) (*botConn, *player) {
	t.Helper()
	b := joinBotToPlay(t, s, name)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		if p.name == name {
			return b, p
		}
	}
	t.Fatal("second bot has no player after join")
	return b, nil
}

// TestSnowballHitPlayerNoDamage 雪球命中玩家：hurt(thrown, 0)——生命不变、
// 有受击反馈与击退、实体事件 3 破碎粒子广播、投掷物消散。事件与击退都
// 在受害者（同为 tracker）的流上断言。
func TestSnowballHitPlayerNoDamage(t *testing.T) {
	s, _, p1 := joinedBot(t, "Shooter")
	b2, p2 := joinedSecondBot(t, s, "Target")

	s.mu.Lock()
	e := newThrowable(s, p1, throwSnowball)
	s.entities[e.id] = e
	p2.x, p2.y, p2.z = p1.x, p1.y, p1.z
	e.mu.Lock()
	e.impacted = true
	e.pendingPlayer = p2.id
	e.px, e.py, e.pz = p2.x, p2.y, p2.z
	// 撞击点偏离受害者 1 格：保证击退方向向量存在。
	e.x, e.y, e.z = p2.x+1, p2.y, p2.z
	e.mu.Unlock()
	p1.seenEnt[e.id] = true
	p2.seenEnt[e.id] = true
	hp := p2.health
	s.mu.Unlock()

	s.consumeThrowableImpacts()

	s.mu.Lock()
	hpAfter := p2.health
	gone := !e.alive()
	s.mu.Unlock()
	if hpAfter != hp {
		t.Fatalf("雪球 0 伤害不应扣血：%v → %v", hp, hpAfter)
	}
	if !gone {
		t.Fatal("撞击后投掷物应标记消散")
	}
	// 受害者流上：先破碎事件（实体事件 3），后击退运动包。
	expectPacket(t, b2, v776.PacketPlayEntityEvent)
	expectPacket(t, b2, v776.PacketPlaySetEntityMotion)
}

// TestEggHatchesBabyChicken 鸡蛋命中：1/8 孵化，孵出的是幼年鸡
// （setAge(-24000) → babyTicks 24000），逐 tick 成长回成年。
func TestEggHatchesBabyChicken(t *testing.T) {
	s, _, p := joinedBot(t, "EggThrower")

	var chick *mobEntity
	for i := 0; i < 400 && chick == nil; i++ {
		s.mu.Lock()
		e := newThrowable(s, p, throwEgg)
		e.impacted = true
		e.ownerID = p.id
		e.px, e.py, e.pz = p.x, p.y, p.z
		e.x, e.y, e.z = p.x, p.y, p.z
		s.entities[e.id] = e
		s.mu.Unlock()
		s.consumeThrowableImpacts()
		s.mu.Lock()
		for _, ent := range s.entities {
			if m, ok := ent.(*mobEntity); ok && m.def.name == "chicken" {
				chick = m
			}
		}
		s.mu.Unlock()
	}
	if chick == nil {
		t.Fatalf("400 次投掷全未触发 1/8 孵化（概率 %g），实现不可能这样倒霉",
			math.Pow(7.0/8.0, 400))
	}
	chick.mu.Lock()
	baby := chick.babyTicks
	chick.mu.Unlock()
	if baby != babyChickenAge {
		t.Fatalf("孵化雏鸡应为幼年（babyTicks %d），实际 %d", babyChickenAge, baby)
	}

	// 成长：babyTicks 归零即成年。
	chick.mu.Lock()
	chick.babyTicks = 2
	chick.mu.Unlock()
	chick.tick(s)
	chick.tick(s)
	chick.mu.Lock()
	grown := chick.babyTicks
	chick.mu.Unlock()
	if grown != 0 {
		t.Fatalf("两 tick 后应转成年，实际 babyTicks=%d", grown)
	}
}

// TestPearlTeleportsOwner 末影珍珠命中：主人传送到 oldPosition、
// 摔落距离清零、固定 5 点 ender_pearl 伤害、客户端收到位置同步。
func TestPearlTeleportsOwner(t *testing.T) {
	s, b, p := joinedBot(t, "Pearler")
	s.mu.Lock()
	e := newThrowable(s, p, throwPearl)
	s.entities[e.id] = e
	e.mu.Lock()
	e.impacted = true
	e.px, e.py, e.pz = 10.5, 65.0, 10.5
	e.x, e.y, e.z = 10.4, 65.1, 10.4
	e.vx, e.vy, e.vz = 0, 0, 0
	e.mu.Unlock()
	p.fallDistance = 7.5
	hp := p.health
	s.mu.Unlock()

	s.consumeThrowableImpacts()

	s.mu.Lock()
	hpAfter, fall, px, py, pz := p.health, p.fallDistance, p.x, p.y, p.z
	s.mu.Unlock()
	if px != 10.5 || py != 65.0 || pz != 10.5 {
		t.Fatalf("应传送到 oldPosition (10.5,65,10.5)，实际 (%v,%v,%v)", px, py, pz)
	}
	if fall != 0 {
		t.Fatalf("传送后摔落距离应清零，实际 %v", fall)
	}
	if hpAfter != hp-5 {
		t.Fatalf("珍珠传送应造成 5 点 ender_pearl 伤害：%v → %v", hp, hpAfter)
	}
	expectPacket(t, b, v776.PacketPlayPlayerPosition)
}

// TestPearlVanishesWhenOwnerDead 主人死亡即消散
// （ENDER_PEARLS_VANISH_ON_DEATH 默认 true），不传送。
func TestPearlVanishesWhenOwnerDead(t *testing.T) {
	s, _, p := joinedBot(t, "Ghost")
	s.mu.Lock()
	e := newThrowable(s, p, throwPearl)
	s.entities[e.id] = e
	p.dead = true
	s.mu.Unlock()

	e.tick(s)
	e.mu.Lock()
	dead := e.dead
	e.mu.Unlock()
	if !dead {
		t.Fatal("主人死亡时珍珠应在 tick 中消散")
	}
}

// TestPearlPortalParticles 命中点广播 32 颗 portal 粒子（逐颗下发，
// 对应 ThrownEnderpearl.onHit 的 32 次 addParticle）。
func TestPearlPortalParticles(t *testing.T) {
	s, b, p := joinedBot(t, "Watcher")
	s.mu.Lock()
	e := newThrowable(s, p, throwPearl)
	s.entities[e.id] = e
	e.mu.Lock()
	e.impacted = true
	e.px, e.py, e.pz = p.x, p.y, p.z
	e.x, e.y, e.z = p.x, p.y, p.z
	e.mu.Unlock()
	s.mu.Unlock()

	s.consumeThrowableImpacts()

	count := 0
	for count < 32 {
		idn, r := b.next()
		if idn != v776.PacketPlayLevelParticles {
			continue
		}
		if got := readParticleID(t, r); got != v776.ParticlePortal {
			t.Fatalf("粒子应为 portal(%d)，实际 %d", v776.ParticlePortal, got)
		}
		count++
	}
}

// readParticleID 消费 level_particles 载荷：bool×2 + double×3 +
// float×4 + int32(count) + varint(粒子 ID)。
func readParticleID(t *testing.T, r *protocol.Reader) int32 {
	t.Helper()
	if _, err := r.Bool(); err != nil {
		t.Fatalf("overrideLimiter: %v", err)
	}
	if _, err := r.Bool(); err != nil {
		t.Fatalf("alwaysShow: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := r.Double(); err != nil {
			t.Fatalf("position: %v", err)
		}
	}
	for i := 0; i < 4; i++ {
		if _, err := r.Float(); err != nil {
			t.Fatalf("spread/speed: %v", err)
		}
	}
	if _, err := r.Int32(); err != nil {
		t.Fatalf("count: %v", err)
	}
	v, err := r.VarInt()
	if err != nil {
		t.Fatalf("particle id: %v", err)
	}
	return v
}

// expectPacket 跳过背景流量（keep-alive/实体同步等）直到等到目标包。
// 每次读取刷新 2 秒短截止（join 流程遗留的绝对截止会污染后续读取），
// 失败时输出已扫过的包序列，便于诊断流序问题。
func expectPacket(t *testing.T, b *botConn, want int32) {
	t.Helper()
	defer func() { _ = b.nc.SetReadDeadline(time.Time{}) }()
	seen := ""
	for i := 0; i < 500; i++ {
		_ = b.nc.SetReadDeadline(time.Now().Add(2 * time.Second))
		idn, _ := b.next()
		if idn == want {
			return
		}
		if len(seen) < 400 {
			seen = seen + fmt.Sprintf("0x%x ", idn)
		}
	}
	t.Fatalf("未等到包 0x%x；已见：%s", want, seen)
}

// TestLevelParticlesWireShape 校验 level_particles 的 26.2 字段序：
// count 是 int32（非 varint），末尾为粒子注册 ID。
func TestLevelParticlesWireShape(t *testing.T) {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayLevelParticles)
	java.WritePlayLevelParticles(w, v776.ParticlePortal, 1.5, 64.25, -3.5, 0.1, 0.2, 0.3, 0, 1)
	got := w.Bytes()
	// 1(id) + 1+1(bool) + 24(double×3) + 16(float×4) + 4(int32) + 1(varint 67) = 48
	if len(got) != 48 {
		t.Fatalf("level_particles 帧长应为 48，实际 %d", len(got))
	}
	// count 字段（int32 大端）位于偏移 1+1+1+24+16=43，值 1。
	if got[43] != 0 || got[44] != 0 || got[45] != 0 || got[46] != 1 {
		t.Fatalf("count 应为大端 int32 编码的 1，实际 %v", got[43:47])
	}
	// 末尾 varint 为 portal=67。
	if got[47] != 67 {
		t.Fatalf("末尾应为 portal 粒子 ID 67，实际 %d", got[47])
	}
}
