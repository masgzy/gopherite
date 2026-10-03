package server

import (
	"math"
	"math/rand"
	"sync"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M14 区域效果云（AreaEffectCloud）。滞留药水落点生成的效果云实体，
// 全部数值与生命周期对照 26.2 反编译源核验
// （research/dec262/out/net/minecraft/world/entity/AreaEffectCloud.java
// 与 projectile/throwableitemprojectile/ThrownLingeringPotion.java）：
//
//   - 生命周期：tickCount - waitTime >= duration 时消散（duration -1 为
//     永久）；等待期（tickCount < waitTime）不施加效果也不收缩；
//   - 半径：每 tick radius += radiusPerTick（滞留为 -3/600），低于
//     MINIMAL_RADIUS 0.5 时消散；
//   - 施加：非等待期每 5 tick（tickCount % 5 == 0）扫描一次，victims
//     表记录 (实体 → 上次施加 tick + reapplicationDelay)，到期移除后
//     才可再次施加；每次施加后 radius += radiusOnUse、
//     duration += durationOnUse，半径用尽同样消散；
//   - 效果：瞬时效果按 potency 0.5 结算（HealOrHarmMobEffect:
//     (int)(0.5*(4<<amp)+0.5) 治疗 / (int)(0.5*(6<<amp)+0.5) 伤害），
//     持续效果按药水完整时长施加（26.2 用 potionDurationScale 通路
//     取代了旧版的 duration/4 折扣，滞留药水未设缩放即 1.0）；
//   - 渲染：客户端依据元数据（半径/等待/粒子）自行生成粒子，服务端
//     只同步 DATA_RADIUS(8)/DATA_WAITING(9)/DATA_PARTICLE(10)。

// vanilla AreaEffectCloud constants.
const (
	cloudMinimalRadius   = 0.5  // MINIMAL_RADIUS：低于此值消散
	cloudMaxRadius       = 32.0 // MAX_RADIUS：setRadius 的钳制上限
	cloudDefaultRadius   = 3.0  // DEFAULT_RADIUS / 滞留药水初始半径
	cloudLingerDuration  = 600  // DEFAULT_LINGERING_DURATION：30 秒
	cloudLingerWait      = 10   // ThrownLingeringPotion.setWaitTime
	cloudScanInterval    = 5    // TIME_BETWEEN_APPLICATIONS
	cloudLingerReapp     = 20   // 默认 reapplicationDelay
	cloudLingerRadiusUse = -0.5 // ThrownLingeringPotion.setRadiusOnUse
	cloudInstantPotency  = 0.5  // applyInstantaneousEffect 的 scale
	cloudHeight          = 0.5  // 碰撞箱高度（水平躺着的圆盘）
)

// cloudEntity is one lingering-potion area effect cloud.
type cloudEntity struct {
	mu       sync.Mutex // 语义同 itemEntity.mu：锁序 e.mu -> Server.mu
	id       int32
	uuid     [16]byte
	x, y, z  float64
	potionID int32 // 药水注册序号（决定颜色与效果表）
	color    int32 // ARGB 粒子色（生成时定死，vanilla 在 setPotionContents 时算好）

	tickCount          int32   // Age
	duration           int32   // -1 = 永久
	waitTime           int32   // 等待期 tick 数
	reapplicationDelay int32   // 同一实体的效果免疫窗口
	durationOnUse      int32   // 每次施加后 duration 增量（滞留为 0）
	radiusOnUse        float32 // 每次施加后半径增量（滞留 -0.5）
	radiusPerTick      float32 // 每 tick 半径增量（滞留 -3/600）
	radius             float32
	waiting            bool

	victims map[int32]int32 // 实体 id → 免疫截止 tick（tickCount+reapplicationDelay）

	nx, ny, nz float64
	hasNet     bool
	dead       bool
	metaDirty  bool
	scanDue    bool // tick 到达 5 的倍数且非等待期；由消费趟结算
}

// newCloudEntity mints the lingering-potion cloud with ThrownLingeringPotion
// .onHitAsPotion's exact parameters.
func newCloudEntity(id int32, x, y, z float64, potionID int32) *cloudEntity {
	e := &cloudEntity{
		id: id,
		x:  x, y: y, z: z,
		potionID:           potionID,
		color:              potionColor(potionID),
		duration:           cloudLingerDuration,
		waitTime:           cloudLingerWait,
		reapplicationDelay: cloudLingerReapp,
		radiusOnUse:        cloudLingerRadiusUse,
		radius:             cloudDefaultRadius,
		victims:            map[int32]int32{},
	}
	e.radiusPerTick = -e.radius / float32(e.duration)
	_, _ = rand.Read(e.uuid[:])
	e.uuid[6] = (e.uuid[6] & 0x0F) | 0x40
	e.uuid[8] = (e.uuid[8] & 0x3F) | 0x80
	return e
}

func (e *cloudEntity) entityID() int32 { return e.id }
func (e *cloudEntity) typeID() int32   { return v776.EntityTypeAreaEffectCloud }
func (e *cloudEntity) xPos() (float64, float64, float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.x, e.y, e.z
}
func (e *cloudEntity) onGroundFlag() bool { return false }
func (e *cloudEntity) alive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.dead
}
func (e *cloudEntity) metadataDirty() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.metaDirty
}
func (e *cloudEntity) clearMetadataDirty() {
	e.mu.Lock()
	e.metaDirty = false
	e.mu.Unlock()
}
func (e *cloudEntity) netPos() (float64, float64, float64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nx, e.ny, e.nz, e.hasNet
}
func (e *cloudEntity) setNetPos(x, y, z float64) {
	e.mu.Lock()
	e.nx, e.ny, e.nz, e.hasNet = x, y, z, true
	e.mu.Unlock()
}

// tick advances the cloud lifecycle (AreaEffectCloud.serverTick port).
// Physics-free and static on the wire; effect application is deferred to
// the consume pass (packets never run under e.mu).
func (e *cloudEntity) tick(s *Server) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dead {
		return
	}
	e.tickCount++
	if e.duration != -1 && e.tickCount-e.waitTime >= e.duration {
		e.dead = true
		return
	}
	shouldWait := e.tickCount < e.waitTime
	if e.waiting != shouldWait {
		e.waiting = shouldWait
		e.metaDirty = true
	}
	if shouldWait {
		return
	}
	if e.radiusPerTick != 0 {
		e.radius += e.radiusPerTick
		e.metaDirty = true
		if e.radius < cloudMinimalRadius {
			e.dead = true
			return
		}
	}
	if e.tickCount%cloudScanInterval == 0 {
		e.scanDue = true
	}
}

// consumeCloudTicks resolves the every-5-tick effect scans of all live
// clouds. Runs on the ticker with Server.mu held by the caller (same lock
// order as consumeArrowHits: s.mu -> e.mu).
func (s *Server) consumeCloudTicks() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ent := range s.entities {
		cloud, ok := ent.(*cloudEntity)
		if !ok {
			continue
		}
		cloud.mu.Lock()
		scan := cloud.scanDue && !cloud.dead
		cloud.scanDue = false
		hasEffects := cloud.potionID >= 0 && int(cloud.potionID) < len(potionDefs) &&
			len(potionDefs[cloud.potionID].effects) > 0
		cloud.mu.Unlock()
		if !scan || !hasEffects {
			continue
		}
		s.applyCloudEffects(cloud)
	}
}

// applyCloudEffects grants the cloud's potion effects to every eligible
// living entity inside the disc. Caller holds Server.mu.
func (s *Server) applyCloudEffects(cloud *cloudEntity) {
	type candidate struct {
		player *player
		mob    *mobEntity
	}
	cloud.mu.Lock()
	age := cloud.tickCount
	radius := cloud.radius
	cx, cy, cz := cloud.x, cloud.y, cloud.z
	// victims 到期清理（vanilla: removeIf(tickCount >= expiry)）。
	for id, until := range cloud.victims {
		if age >= until {
			delete(cloud.victims, id)
		}
	}
	if cloud.potionID < 0 || int(cloud.potionID) >= len(potionDefs) {
		cloud.mu.Unlock()
		return
	}
	effects := potionDefs[cloud.potionID].effects
	var candidates []candidate

	// 玩家判定：0.6×1.8 身体箱与云的圆盘箱（2r×0.5）相交，vanilla
	// getEntitiesOfClass(box) 即箱体相交测试。
	for _, p := range s.players {
		if p.dead || p.gameMode != 0 {
			continue
		}
		if _, immune := cloud.victims[p.id]; immune {
			continue
		}
		if !cloudBoxOverlap(cx, cy, cz, float64(radius), p.x-0.3, p.y, p.z-0.3, p.x+0.3, p.y+1.8, p.z+0.3) {
			continue
		}
		candidates = append(candidates, candidate{player: p})
	}
	// 生物判定：用各自的 body 箱。
	for _, ent := range s.entities {
		m, ok := ent.(*mobEntity)
		if !ok {
			continue
		}
		m.mu.Lock()
		dying := m.deathTicks > 0 || m.removed || m.health <= 0
		mx, my, mz := m.x, m.y, m.z
		hw := float64(m.def.width) / 2
		h := float64(m.def.height)
		m.mu.Unlock()
		if dying {
			continue
		}
		if _, immune := cloud.victims[m.id]; immune {
			continue
		}
		if !cloudBoxOverlap(cx, cy, cz, float64(radius), mx-hw, my, mz-hw, mx+hw, my+h, mz+hw) {
			continue
		}
		candidates = append(candidates, candidate{mob: m})
	}
	cloud.mu.Unlock()

	for _, c := range candidates {
		cloud.mu.Lock()
		if cloud.dead {
			cloud.mu.Unlock()
			break
		}
		id := int32(-1)
		if c.player != nil {
			id = c.player.id
		} else {
			id = c.mob.id
		}
		cloud.victims[id] = cloud.tickCount + cloud.reapplicationDelay
		cloud.mu.Unlock()

		// 施加效果：瞬时按 0.5 效力结算，持续效果给完整时长。
		for _, pe := range effects {
			if effectDefs[pe.eff].instant {
				s.applyCloudInstantEffect(pe.eff, pe.amp, c.player, c.mob)
				continue
			}
			if c.player != nil {
				s.applyPlayerEffectLocked(c.player, pe.eff, pe.amp, pe.dur, false, true, true)
			} else {
				s.applyMobEffectLocked(c.mob, pe.eff, pe.amp, pe.dur)
			}
		}

		// 每次施加后的半径/时长结算（vanilla 在 per-entity 循环内）。
		cloud.mu.Lock()
		if cloud.radiusOnUse != 0 {
			cloud.radius += cloud.radiusOnUse
			cloud.metaDirty = true
			if cloud.radius < cloudMinimalRadius {
				cloud.dead = true
				cloud.mu.Unlock()
				return
			}
		}
		if cloud.durationOnUse != 0 && cloud.duration != -1 {
			cloud.duration += cloud.durationOnUse
			if cloud.duration <= 0 {
				cloud.dead = true
				cloud.mu.Unlock()
				return
			}
		}
		cloud.mu.Unlock()
	}
}

// applyCloudInstantEffect settles one instantaneous effect at the cloud's
// 0.5 potency (vanilla applyInstantaneousEffect scale), including the
// undead heal↔harm inversion for mobs.
func (s *Server) applyCloudInstantEffect(effID, amp int32, p *player, m *mobEntity) {
	// 玩家不是亡灵（isInvertedHealAndHarm 恒为 false），无反转分支；
	// 生物经 isUndeadMob 反转治疗与伤害。
	switch effID {
	case 5: // instant_health
		amount := float32(int(float64(cloudInstantPotency)*float64(int32(4)<<uint(amp)) + 0.5))
		if p != nil {
			healEffectTarget(p, nil, amount)
			return
		}
		if m != nil {
			if isUndeadMob(m) {
				s.damageEffectTargetLocked(nil, m, amount, v776.DamageTypeIndirectMagic)
			} else {
				healEffectTarget(nil, m, amount)
			}
		}
	case 6: // instant_damage
		amount := float32(int(float64(cloudInstantPotency)*float64(int32(6)<<uint(amp)) + 0.5))
		if p != nil {
			s.damageEffectTargetLocked(p, nil, amount, v776.DamageTypeIndirectMagic)
			return
		}
		if m != nil {
			if isUndeadMob(m) {
				healEffectTarget(nil, m, amount)
			} else {
				s.damageEffectTargetLocked(nil, m, amount, v776.DamageTypeIndirectMagic)
			}
		}
	case 22: // saturation：饱和度随效力折半（SaturationMobEffect scale 通路）
		if p != nil {
			amount := cloudInstantPotency * float32(amp+1)
			p.food = int32(math.Min(maxFood, float64(p.food)+float64(amount)))
			p.saturation = float32(math.Min(float64(p.food), float64(p.saturation)+float64(amount)))
			p.sendHealth()
		}
	}
}

// cloudBoxOverlap reports whether an entity body box intersects the
// cloud's disc box (2r wide, 0.5 tall, centered at cx/cy/cz).
func cloudBoxOverlap(cx, cy, cz, r, x1, y1, z1, x2, y2, z2 float64) bool {
	return x1 < cx+r && x2 > cx-r &&
		y1 < cy+cloudHeight/2 && y2 > cy-cloudHeight/2 &&
		z1 < cz+r && z2 > cz-r
}

// encodeCloudSpawn writes the add_entity payload: a static entity with no
// velocity and no auxiliary data.
func encodeCloudSpawn(w *protocol.Writer, e *cloudEntity) {
	e.mu.Lock()
	defer e.mu.Unlock()
	java.WriteAddEntity(w, e.id, e.uuid, e.typeID(), e.x, e.y, e.z, 0, 0, 0, 0, 0, 0, 0)
}

// writeCloudMetadata encodes the set_entity_data frame: radius, waiting
// flag and the entity_effect color particle.
func writeCloudMetadata(w *protocol.Writer, e *cloudEntity) {
	e.mu.Lock()
	defer e.mu.Unlock()
	w.VarInt(v776.PacketPlaySetEntityData)
	w.VarInt(e.id)
	w.Byte(v776.MetaIndexCloudRadius)
	w.VarInt(v776.MetaSerializerFloat)
	w.Float(e.radius)
	w.Byte(v776.MetaIndexCloudWaiting)
	w.VarInt(v776.MetaSerializerBool)
	w.Bool(e.waiting)
	w.Byte(v776.MetaIndexCloudParticle)
	w.VarInt(v776.MetaSerializerParticle)
	w.VarInt(v776.ParticleEntityEffect)
	w.Int32(e.color)
	w.Byte(0xFF)
}
