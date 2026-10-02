package server

import (
	"math"
	"math/rand"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M13 效果引擎的结算与网络同步层。玩家效果由 s.mu 保护（在
// tickSurvival / 伤害管线内调用）；生物效果在持有 s.mu 的世界 tick 中
// 统一驱动（见 tickAllMobEffects 的锁序说明）。效果包广播到全部在线玩家——追踪者判定对药水这类低频包
// 不是热点，取最简实现。

// sendMobEffectPacketLocked 把一个已编码效果包写给所有玩家（调用方
// 持有 s.mu，读 s.players 安全）。
func (s *Server) sendMobEffectPacketLocked(entityID int32, encode func(w *protocol.Writer)) {
	body := protocol.NewWriter()
	encode(body)
	for _, p := range s.players {
		_ = p.conn.sendPacket(body.Bytes())
	}
}

// syncPlayerEffectLocked 向目标玩家与其余玩家发送 Update Mob Effect。
func (s *Server) syncPlayerEffectLocked(p *player, effID int32, inst *mobEffectInstance) {
	s.sendMobEffectPacketLocked(p.id, func(w *protocol.Writer) {
		java.WriteUpdateMobEffect(w, p.id, effID, inst.amp, inst.duration, inst.wireFlags())
	})
}

// syncPlayerRemoveEffectLocked 向全部玩家发送 Remove Mob Effect。
func (s *Server) syncPlayerRemoveEffectLocked(entityID, effID int32) {
	s.sendMobEffectPacketLocked(entityID, func(w *protocol.Writer) {
		java.WriteRemoveMobEffect(w, entityID, effID)
	})
}

// syncPlayerSharedFlagsLocked 重算并广播玩家的隐身/发光共享标志。
func (s *Server) syncPlayerSharedFlagsLocked(p *player) {
	var flags byte
	if p.hasEffect(13) { // invisibility
		flags |= java.SharedFlagInvisible
	}
	if p.hasEffect(23) { // glowing
		flags |= java.SharedFlagGlowing
	}
	s.sendMobEffectPacketLocked(p.id, func(w *protocol.Writer) {
		java.WriteSetEntityDataFlags(w, p.id, flags)
	})
}

// applyEffectInstance 实现 vanilla 的升级/刷新规则：
//   - 即时效果直接结算一次，不入驻留表；
//   - 同效果：更强等级或（同等级且更长时长）时替换，否则保留旧值。
//
// 返回是否实际写入（用于决定是否发包）。
func applyEffectInstance(store *effectTarget, effID int32, inst *mobEffectInstance) bool {
	if effectDefs[effID].instant {
		return false // 即时效果走 resolveInstantEffect 单独结算
	}
	store.initEffects()
	if cur := store.getEffect(effID); cur != nil {
		if inst.amp < cur.amp || (inst.amp == cur.amp && inst.duration < cur.duration) {
			return false // 旧效果更优：保留
		}
	}
	store.effects[effID] = inst
	return true
}

// resolveInstantEffect 结算即时效果：瞬间治疗/伤害（亡灵生物反转）与
// 饱和。玩家与生物共用。
func resolveInstantEffect(s *Server, t *effectTarget, effID int32, amp int32, undead bool, victim *player, mob *mobEntity) {
	switch effID {
	case 5: // instant_health
		amount := float32(int32(4) << uint(amp))
		if undead {
			s.damageEffectTargetLocked(victim, mob, amount, v776.DamageTypeIndirectMagic)
		} else {
			healEffectTarget(victim, mob, amount)
		}
	case 6: // instant_damage
		amount := float32(int32(6) << uint(amp))
		if undead {
			healEffectTarget(victim, mob, amount)
		} else {
			s.damageEffectTargetLocked(victim, mob, amount, v776.DamageTypeIndirectMagic)
		}
	case 22: // saturation: food += amp+1, saturation += amp+1
		if victim != nil {
			victim.food = int32(math.Min(maxFood, float64(victim.food+amp+1)))
			victim.saturation = float32(math.Min(float64(victim.food), float64(victim.saturation)+float64(amp+1)))
			victim.sendHealth()
		}
	}
}

// healEffectTarget heals the player or mob (capped by max health).
func healEffectTarget(p *player, m *mobEntity, amount float32) {
	if p != nil {
		p.health = float32(math.Min(float64(maxHealth), float64(p.health+amount)))
		p.sendHealth()
		return
	}
	if m != nil {
		m.health = float32(math.Min(float64(m.def.maxHealth), float64(m.health+amount)))
	}
}

// damageEffectTargetLocked damages the player or mob with a magic-class
// source (instant effects ignore armor like vanilla magic damage).
func (s *Server) damageEffectTargetLocked(p *player, m *mobEntity, amount float32, dmgType int32) {
	if p != nil {
		s.damagePlayerLocked(p, amount, dmgType, -1, -1)
		return
	}
	if m != nil {
		s.damageMobLocked(m, amount, dmgType, -1, -1)
	}
}

// --- 玩家效果 API（调用方持有 s.mu）----------------------------------------

// applyPlayerEffectLocked adds/refreshes one effect on a player and
// syncs the packets. Instant effects settle immediately with no packet.
func (s *Server) applyPlayerEffectLocked(p *player, effID, amp, duration int32, ambient, particles, icon bool) {
	if effID < 0 || int(effID) >= len(effectDefs) {
		return
	}
	inst := &mobEffectInstance{amp: amp, duration: duration, ambient: ambient, particles: particles, icon: icon}
	if effectDefs[effID].instant {
		resolveInstantEffect(s, &p.effectTarget, effID, amp, false, p, nil)
		return
	}
	if !applyEffectInstance(&p.effectTarget, effID, inst) {
		return
	}
	s.syncPlayerEffectLocked(p, effID, inst)
	switch effID {
	case 21: // absorption: (amp+1) * 2 吸收心，vanilla MAX_ABSORPTION +4/级
		p.absorption = float32(2 * (amp + 1))
	case 13, 23: // invisibility / glowing → 共享标志
		s.syncPlayerSharedFlagsLocked(p)
	}
}

// removePlayerEffectLocked drops one effect and syncs.
func (s *Server) removePlayerEffectLocked(p *player, effID int32) {
	if !p.hasEffect(effID) {
		return
	}
	p.removeEffectSlot(effID)
	s.syncPlayerRemoveEffectLocked(p.id, effID)
	if effID == 21 {
		p.absorption = 0
	}
	if effID == 13 || effID == 23 {
		s.syncPlayerSharedFlagsLocked(p)
	}
}

// clearPlayerEffectsLocked drops every effect (death, milk, /effect clear).
func (s *Server) clearPlayerEffectsLocked(p *player) {
	ids := make([]int32, 0, len(p.effects))
	for id := range p.effects {
		ids = append(ids, id)
	}
	for _, id := range ids {
		s.removePlayerEffectLocked(p, id)
	}
	p.absorption = 0
}

// tickPlayerEffectsLocked advances durations and periodic effects once
// per tick (called from tickSurvival, before the hunger pipeline).
func (s *Server) tickPlayerEffectsLocked(p *player) {
	if len(p.effects) == 0 {
		return
	}
	ids := make([]int32, 0, len(p.effects))
	for id := range p.effects {
		ids = append(ids, id)
	}
	for _, id := range ids {
		inst := p.getEffect(id)
		if inst == nil {
			continue
		}
		inst.duration--
		if inst.duration <= 0 {
			s.removePlayerEffectLocked(p, id)
			continue
		}
		// 周期结算：vanilla 在 duration 递减后以 duration % interval == 0
		// 触发（MobEffectInstance.tickServer 语义），interval 为 0 表示
		// 每 tick 结算（50>>amp 下溢为 0 的高等级情形）。
		interval := effectTickInterval(id, inst.amp)
		if interval <= 0 || inst.duration%interval == 0 {
			s.applyPeriodicPlayerEffect(p, id)
			if p.dead {
				return
			}
		}
	}
}

// applyPeriodicPlayerEffect settles one periodic tick of a held effect.
func (s *Server) applyPeriodicPlayerEffect(p *player, effID int32) {
	inst := p.getEffect(effID)
	if inst == nil {
		return
	}
	switch effID {
	case 9: // regeneration: heal 1.0
		if p.health < maxHealth {
			p.health++
			p.sendHealth()
		}
	case 18: // poison: 1.0 magic damage, never fatal (health > 1.0 gate)
		if p.health > 1.0 {
			s.damagePlayerLocked(p, 1, v776.DamageTypeMagic, -1, -1)
		}
	case 19: // wither: 1.0 wither damage
		s.damagePlayerLocked(p, 1, v776.DamageTypeWither, -1, -1)
	case 16: // hunger: exhaustion 0.005 * (amp+1) per tick
		p.exhaustion += 0.005 * float32(inst.amp+1)
	}
}

// --- 生物效果 API -----------------------------------------------------------
//
// 锁序约定：统一为 Server.mu → m.mu（与 hurtMobTypeLocked/damageMobLocked
// 一致）。生物效果的周期结算不放进 mobEntity.tick（那里只持 m.mu，再拿
// s.mu 广播会反转锁序），而是由世界 tick 在持有 s.mu 的状态下统一驱动：
// 先在 m.mu 内快照+递减，释放后再发包与结算伤害。

// applyMobEffectLocked adds/refreshes one effect on a mob.
// Caller holds Server.mu and m.mu.
func (s *Server) applyMobEffectLocked(m *mobEntity, effID, amp, duration int32) {
	if effID < 0 || int(effID) >= len(effectDefs) {
		return
	}
	inst := &mobEffectInstance{amp: amp, duration: duration, particles: true, icon: true}
	if effectDefs[effID].instant {
		resolveInstantEffect(s, &m.effectTarget, effID, amp, isUndeadMob(m), nil, m)
		return
	}
	if !applyEffectInstance(&m.effectTarget, effID, inst) {
		return
	}
	s.sendMobEffectPacketLocked(m.id, func(w *protocol.Writer) {
		java.WriteUpdateMobEffect(w, m.id, effID, inst.amp, inst.duration, inst.wireFlags())
	})
}

// removeMobEffectLocked drops one effect from a mob.
// Caller holds Server.mu and m.mu.
func (s *Server) removeMobEffectLocked(m *mobEntity, effID int32) {
	if !m.hasEffect(effID) {
		return
	}
	m.removeEffectSlot(effID)
	s.sendMobEffectPacketLocked(m.id, func(w *protocol.Writer) {
		java.WriteRemoveMobEffect(w, m.id, effID)
	})
}

// isUndeadMob reports the vanilla undead inversion set we model.
func isUndeadMob(m *mobEntity) bool {
	return m.def != nil && (m.def.name == "zombie" || m.def.name == "skeleton")
}

// tickAllMobEffects advances effect durations on every mob once per tick.
// Caller holds Server.mu. Per mob: m.mu guards the state mutation, then
// the periodic settle (damage/heal/packets) runs without m.mu because
// damageMobLocked takes m.mu itself.
func (s *Server) tickAllMobEffects() {
	for _, e := range s.entities {
		m, ok := e.(*mobEntity)
		if !ok || len(m.effects) == 0 {
			continue
		}
		type settle struct {
			effID int32
			amp   int32
		}
		var removed []int32
		var due []settle

		m.mu.Lock()
		if m.deathTicks > 0 || m.removed {
			m.mu.Unlock()
			continue
		}
		for id, inst := range m.effects {
			inst.duration--
			if inst.duration <= 0 {
				removed = append(removed, id)
				continue
			}
			interval := effectTickInterval(id, inst.amp)
			if interval <= 0 || inst.duration%interval == 0 {
				due = append(due, settle{effID: id, amp: inst.amp})
			}
		}
		for _, id := range removed {
			m.removeEffectSlot(id)
		}
		m.mu.Unlock()

		for _, id := range removed {
			s.sendMobEffectPacketLocked(m.id, func(w *protocol.Writer) {
				java.WriteRemoveMobEffect(w, m.id, id)
			})
		}
		for _, d := range due {
			switch d.effID {
			case 9: // regeneration: heal 1.0
				m.mu.Lock()
				if m.health < m.def.maxHealth {
					m.health++
				}
				m.mu.Unlock()
			case 18: // poison: 1.0 magic damage, never fatal
				m.mu.Lock()
				alive := m.health > 1.0
				m.mu.Unlock()
				if alive {
					s.damageMobLocked(m, 1, v776.DamageTypeMagic, -1, -1)
				}
			case 19: // wither: 1.0 wither damage
				s.damageMobLocked(m, 1, v776.DamageTypeWither, -1, -1)
			}
		}
	}
}

// --- 药水与食物的入口 ------------------------------------------------------

// applyPotionEffects grants every effect of a potion to a player. Used
// by drinking and splash impacts.
func (s *Server) applyPotionEffectsToPlayer(p *player, potionID int32, durationScale float32) {
	if potionID < 0 || int(potionID) >= len(potionDefs) {
		return
	}
	for _, pe := range potionDefs[potionID].effects {
		dur := int32(math.Max(1, math.Round(float64(float32(pe.dur)*durationScale))))
		s.applyPlayerEffectLocked(p, pe.eff, pe.amp, dur, false, true, true)
	}
}

// applyPotionEffectsToMob grants every effect of a potion to a mob.
func (s *Server) applyPotionEffectsToMob(m *mobEntity, potionID int32, durationScale float32) {
	if potionID < 0 || int(potionID) >= len(potionDefs) {
		return
	}
	for _, pe := range potionDefs[potionID].effects {
		dur := int32(math.Max(1, math.Round(float64(float32(pe.dur)*durationScale))))
		s.applyMobEffectLocked(m, pe.eff, pe.amp, dur)
	}
}

// applyConsumableEffects settles the on-eat effects (golden apples,
// rotten flesh, spider eye, pufferfish, milk). Caller holds s.mu.
func (s *Server) applyConsumableEffects(p *player, itemName string) {
	cons, ok := consumableByItemName[itemName]
	if !ok {
		return
	}
	if cons.clearAll {
		s.clearPlayerEffectsLocked(p)
		return
	}
	for _, ce := range cons.effects {
		if ce.prob < 1.0 && rand01() >= ce.prob {
			continue // 概率判定（腐肉 80% 饥饿）
		}
		s.applyPlayerEffectLocked(p, ce.eff, ce.amp, ce.dur, false, true, true)
	}
}

// rand01 is a small helper for probability checks.
func rand01() float32 {
	return rand.Float32()
}
