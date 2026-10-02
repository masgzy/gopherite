package server

import (
        "testing"

        "github.com/masgzy/gopherite/protocol/java"
        "github.com/masgzy/gopherite/protocol/java/v776"
)

// M13 状态效果引擎测试。数值对照 26.2 反编译源
// （research/decomp_m13 下的 MobEffects / 各特殊效果类）。

func newEffectTestPlayer() *player {
        return &player{health: 10, food: 20, gameMode: 0}
}

// applyEffectInstance 的升级规则：同效果更强或更长才替换。
func TestApplyEffectInstanceUpgradeRules(t *testing.T) {
        var tgt effectTarget
        base := &mobEffectInstance{amp: 0, duration: 600, particles: true, icon: true}

        if !applyEffectInstance(&tgt, 0 /*speed*/, base) {
                t.Fatal("首次施加应写入")
        }
        // 更短的同级：保留旧值。
        short := &mobEffectInstance{amp: 0, duration: 100}
        if applyEffectInstance(&tgt, 0, short) {
                t.Fatal("同等级更短时长不应替换")
        }
        if got := tgt.getEffect(0); got.duration != 600 {
                t.Fatalf("旧效果应保留 600t，实际 %d", got.duration)
        }
        // 同级更长：刷新。
        longer := &mobEffectInstance{amp: 0, duration: 900}
        if !applyEffectInstance(&tgt, 0, longer) {
                t.Fatal("同等级更长时长应替换")
        }
        // 更强等级：替换。
        stronger := &mobEffectInstance{amp: 2, duration: 100}
        if !applyEffectInstance(&tgt, 0, stronger) {
                t.Fatal("更强等级应替换")
        }
        // 更弱等级：保留。
        weaker := &mobEffectInstance{amp: 1, duration: 9999}
        if applyEffectInstance(&tgt, 0, weaker) {
                t.Fatal("更弱等级不应替换")
        }
        if got := tgt.getEffect(0); got.amp != 2 {
                t.Fatalf("应保留 amp=2，实际 %d", got.amp)
        }
}

// 瞬时效果不驻留。
func TestInstantEffectsDoNotPersist(t *testing.T) {
        var tgt effectTarget
        ok := applyEffectInstance(&tgt, 5 /*instant_health*/, &mobEffectInstance{duration: 1})
        if ok || len(tgt.effects) != 0 {
                t.Fatal("瞬时效果不应写入驻留表")
        }
}

// 再生周期：50>>amp，与 decompiled shouldApplyEffectTickThisTick 一致。
func TestEffectTickIntervals(t *testing.T) {
        if got := effectTickInterval(9, 0); got != 50 { // regeneration
                t.Fatalf("再生 I 间隔应为 50，实际 %d", got)
        }
        if got := effectTickInterval(9, 2); got != 12 {
                t.Fatalf("再生 III 间隔应为 12，实际 %d", got)
        }
        if got := effectTickInterval(18, 0); got != 25 { // poison
                t.Fatalf("中毒 I 间隔应为 25，实际 %d", got)
        }
        if got := effectTickInterval(19, 0); got != 40 { // wither
                t.Fatalf("凋零 I 间隔应为 40，实际 %d", got)
        }
}

// 中毒不应致死（health > 1.0 门限）。
func TestPoisonNeverKills(t *testing.T) {
        s := startTestServer(t)
        p := newEffectTestPlayer()
        p.conn = &conn{s: s}
        p.health = 1.5

        s.mu.Lock()
        s.applyPlayerEffectLocked(p, 18 /*poison*/, 0, 100, false, true, true)
        s.applyPeriodicPlayerEffect(p, 18)
        s.mu.Unlock()
        if p.dead || p.health <= 0 {
                t.Fatalf("中毒不应把 1.5 血打死，实际 health=%v", p.health)
        }

        // 高于门限时正常造成 1.0 伤害。
        p.health = 5
        s.mu.Lock()
        s.applyPeriodicPlayerEffect(p, 18)
        s.mu.Unlock()
        if p.health != 4 {
                t.Fatalf("中毒应造成 1.0 伤害，实际 %v", p.health)
        }
}

// 抗性按 20%/级减伤且 starve/void 例外。
func TestResistanceFactor(t *testing.T) {
        var tgt effectTarget
        tgt.initEffects()
        tgt.effects[10] = &mobEffectInstance{amp: 1} // resistance II
        if got := resistanceFactor(&tgt, v776.DamageTypeMobAttack); got > 0.61 || got < 0.59 {
                t.Fatalf("抗性 II 应减伤至 ~0.6，实际 %v", got)
        }
        if got := resistanceFactor(&tgt, v776.DamageTypeOutOfWorld); got != 1 {
                t.Fatalf("虚空伤害应无视抗性，实际 %v", got)
        }
        if got := resistanceFactor(&tgt, v776.DamageTypeStarve); got != 1 {
                t.Fatalf("饥饿伤害应无视抗性，实际 %v", got)
        }
}

// 吸收心优先扣减。
func TestAbsorptionAbsorbsDamageFirst(t *testing.T) {
        s := startTestServer(t)
        p := newEffectTestPlayer()
        p.conn = &conn{s: s}
        p.health = 20
        p.absorption = 4

        s.mu.Lock()
        s.damagePlayerLocked(p, 3, v776.DamageTypeMobAttack, -1, -1)
        s.mu.Unlock()
        if p.health != 20 {
                t.Fatalf("吸收心应全额吸收 3 点伤害，实际 health=%v", p.health)
        }
        if p.absorption != 1 {
                t.Fatalf("吸收心应剩 1，实际 %v", p.absorption)
        }

        // 吸收心耗尽后伤害溢出。
        s.mu.Lock()
        s.damagePlayerLocked(p, 2, v776.DamageTypeMobAttack, -1, -1)
        s.mu.Unlock()
        if p.health != 19 {
                t.Fatalf("溢出 1 点应打进生命，实际 health=%v", p.health)
        }
}

// 力量/虚弱近战加成。
func TestMeleeBonusFromEffects(t *testing.T) {
        var tgt effectTarget
        if got := meleeBonusFromEffects(&tgt); got != 0 {
                t.Fatalf("无效果时应为 0，实际 %v", got)
        }
        tgt.initEffects()
        tgt.effects[4] = &mobEffectInstance{amp: 1} // strength II
        if got := meleeBonusFromEffects(&tgt); got != 6 {
                t.Fatalf("力量 II 应 +6，实际 %v", got)
        }
        tgt.effects[17] = &mobEffectInstance{amp: 0} // weakness I
        if got := meleeBonusFromEffects(&tgt); got != 2 {
                t.Fatalf("力量 II + 虚弱 I 应 +2，实际 %v", got)
        }
}

// 死亡清除全部效果并归零吸收心。
func TestDeathClearsEffects(t *testing.T) {
        s := startTestServer(t)
        p := newEffectTestPlayer()
        p.conn = &conn{s: s}
        p.health = 20

        s.mu.Lock()
        s.applyPlayerEffectLocked(p, 0 /*speed*/, 0, 400, false, true, true)
        s.applyPlayerEffectLocked(p, 21 /*absorption*/, 0, 600, false, true, true)
        s.mu.Unlock()
        if len(p.effects) != 2 {
                t.Fatalf("应有两个效果，实际 %d", len(p.effects))
        }

        s.mu.Lock()
        s.killPlayerLocked(p, "test")
        s.mu.Unlock()
        if len(p.effects) != 0 || p.absorption != 0 {
                t.Fatalf("死亡应清空效果与吸收心，实际 effects=%d absorption=%v", len(p.effects), p.absorption)
        }
}

// 吸收效果按等级给 2*(amp+1) 颗心（生命值单位）。
func TestAbsorptionGrantsHearts(t *testing.T) {
        s := startTestServer(t)
        p := newEffectTestPlayer()
        p.conn = &conn{s: s}
        s.mu.Lock()
        s.applyPlayerEffectLocked(p, 21 /*absorption*/, 3, 600, false, true, true)
        s.mu.Unlock()
        if p.absorption != 8 {
                t.Fatalf("吸收 IV 应给 8 点吸收血，实际 %v", p.absorption)
        }
}

// 时长到期自动移除。
func TestEffectExpiryRemoves(t *testing.T) {
        s := startTestServer(t)
        p := newEffectTestPlayer()
        p.conn = &conn{s: s}
        s.mu.Lock()
        s.applyPlayerEffectLocked(p, 0 /*speed*/, 0, 2, false, true, true)
        s.mu.Unlock()
        if !p.hasEffect(0) {
                t.Fatal("效果应已生效")
        }
        s.mu.Lock()
        s.tickPlayerEffectsLocked(p) // 2 -> 1
        s.tickPlayerEffectsLocked(p) // 1 -> 0: 移除
        s.mu.Unlock()
        if p.hasEffect(0) {
                t.Fatal("时长归零后应移除")
        }
}

// 更新包的标志位编码。
func TestWireFlags(t *testing.T) {
        inst := &mobEffectInstance{ambient: true, particles: true, icon: true}
        if inst.wireFlags() != java.EffectFlagAmbient|java.EffectFlagParticles|java.EffectFlagIcon {
                t.Fatalf("标志位编码错误: %02x", inst.wireFlags())
        }
        hide := &mobEffectInstance{particles: false, icon: true}
        if hide.wireFlags() != java.EffectFlagIcon {
                t.Fatalf("隐藏粒子的标志位应只有图标: %02x", hide.wireFlags())
        }
}

// 26.2 效果注册表抽样（MobEffects.java 顺序）。
func TestEffectRegistryOrder(t *testing.T) {
        cases := []struct {
                id   int32
                name string
        }{
                {0, "speed"}, {4, "strength"}, {9, "regeneration"}, {19, "wither"},
                {27, "slow_falling"}, {38, "infested"}, {39, "breath_of_the_nautilus"},
        }
        for _, tc := range cases {
                if effectDefs[tc.id].name != tc.name {
                        t.Fatalf("效果 %d 应为 %s，实际 %s", tc.id, tc.name, effectDefs[tc.id].name)
                }
        }
        if id, ok := effectIDByName("speed"); !ok || id != 0 {
                t.Fatalf("speed 解析错误: %d %v", id, ok)
        }
}
