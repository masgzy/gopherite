package server

import (
        "math"
        "math/rand"
        "sync"

        "github.com/masgzy/gopherite/protocol"
        "github.com/masgzy/gopherite/protocol/java"
        "github.com/masgzy/gopherite/protocol/java/v776"
)

// M15 通用投掷物：雪球 / 鸡蛋 / 末影珍珠。对应 26.2 的
// ThrowableItemProjectile 家族（ThrowableProjectile 弹道 + 物品栈元数据），
// 行为逐条对照反编译源核验：
//
//   - ThrowableProjectile.tick：重力 0.03、空气阻力 0.99、沿本 tick 位移
//     向量做命中扫描，命中后 setPos 到命中点再结算；
//   - Projectile.getMovementToShoot：方向归一化后每轴加
//     triangle(0, 0.0172275×uncertainty) 散布，再乘威力 1.5；
//   - Projectile.shootFromRotation 末尾继承投掷者动量
//     （onGround 只继承水平分量）；
//   - Snowball/ThrownEgg.onHit：广播实体事件 3（客户端在其位置播放
//     8 个 item 破碎粒子）后 discard；命中实体走 hurt(thrown, 0/3)；
//   - ThrownEgg.onHit：1/8 孵出小鸡，其中 1/32 变成 4 只（setAge(-24000)）；
//   - ThrownEnderpearl.onHit：32 颗 portal 粒子 → 把主人传送到
//     oldPosition（本 tick 起点位置）→ resetFallDistance →
//     hurtServer(ender_pearl, 5.0) → 播放 entity.player.teleport；
//   - ThrownEnderpearl.tick：主人死亡即消散
//     （GameRules.ENDER_PEARLS_VANISH_ON_DEATH 默认 true）。

const (
        throwableGravity  = 0.03        // ThrowableProjectile.getDefaultGravity
        throwableInertia  = 0.99        // ThrowableProjectile.getAirDrag
        throwableSpeed    = 1.5         // 三个物品 use() 的统一威力 pow
        throwableSpread   = 0.0172275   // getMovementToShoot 的三角散布系数
        throwableLifespan = 3600        // 兜底消散（原版无超时，防实体泄漏）
        throwableHitR2    = 0.45 * 0.45 // 线段命中阈值，与箭一致

        pearlDamage    = 5.0   // ThrownEnderpearl 传送后固定伤害
        babyChickenAge = 24000 // setAge(-24000)：幼年成长回成年所需 tick
)

// throwableKind discriminates the three throwable item projectiles.
type throwableKind int8

const (
        throwSnowball throwableKind = iota + 1
        throwEgg
        throwPearl
)

// throwableEntity is one flying snowball/egg/ender pearl.
type throwableEntity struct {
        mu         sync.Mutex
        id         int32
        uuid       [16]byte
        kind       throwableKind
        x, y, z    float64
        vx, vy, vz float64
        yaw, pitch float32
        ownerID    int32
        age        int32
        dead       bool

        // 撞击标记：tick 趟置位，consumeThrowableImpacts 消费（与箭/药水
        // 相同的锁序：s.mu → e.mu，包发送绝不持 e.mu）。
        impacted      bool
        pendingPlayer int32
        pendingMob    int32

        px, py, pz   float64 // 本 tick 起点（≈ 原版 oldPosition，珍珠传送落点）
        nx, ny, nz   float64
        hasNet       bool
        nyaw, npitch float32
        hasRot       bool
}

// newThrowable builds one projectile at the thrower's eye level.
func newThrowable(s *Server, p *player, kind throwableKind) *throwableEntity {
        // ThrowableItemProjectile(owner)：出生点 = (x, eyeY-0.1, z)；
        // spawnProjectileFromRotation(..., yOffset=0, pow=1.5, uncertainty=1.0)。
        // 服务器不追踪客户端动量，动量继承按静止处理（站立投掷即精确值）。
        yawRad := float64(p.yaw) * math.Pi / 180
        pitchRad := float64(p.pitch) * math.Pi / 180
        dx := -math.Sin(yawRad) * math.Cos(pitchRad)
        dy := -math.Sin(pitchRad)
        dz := math.Cos(yawRad) * math.Cos(pitchRad)
        // triangle(0, d) = (r1 - r2)·d：先加散布再乘威力（(dir+j)·pow 等价于
        // dir·pow + j·pow）。
        jx := (rand.Float64() - rand.Float64()) * throwableSpread
        jy := (rand.Float64() - rand.Float64()) * throwableSpread
        jz := (rand.Float64() - rand.Float64()) * throwableSpread
        e := &throwableEntity{
                id:   s.allocEntityID(),
                kind: kind,
                // 眼高 1.62（vanilla getEyeY）-0.1。
                x: p.x, y: p.y + 1.52, z: p.z,
                vx:  (dx + jx) * throwableSpeed,
                vy:  (dy + jy) * throwableSpeed,
                vz:  (dz + jz) * throwableSpeed,
                yaw: p.yaw, pitch: p.pitch,
                ownerID: p.id,
                px:      p.x, py: p.y + 1.52, pz: p.z,
        }
        _, _ = rand.Read(e.uuid[:])
        e.uuid[6] = (e.uuid[6] & 0x0F) | 0x40
        e.uuid[8] = (e.uuid[8] & 0x3F) | 0x80
        return e
}

func (e *throwableEntity) entityID() int32 { return e.id }
func (e *throwableEntity) typeID() int32 {
        switch e.kind {
        case throwEgg:
                return v776.EntityTypeEgg
        case throwPearl:
                return v776.EntityTypeEnderPearl
        default:
                return v776.EntityTypeSnowball
        }
}
func (e *throwableEntity) xPos() (float64, float64, float64) {
        e.mu.Lock()
        defer e.mu.Unlock()
        return e.x, e.y, e.z
}
func (e *throwableEntity) onGroundFlag() bool { return false }
func (e *throwableEntity) alive() bool {
        e.mu.Lock()
        defer e.mu.Unlock()
        return !e.dead
}
func (e *throwableEntity) metadataDirty() bool { return false }
func (e *throwableEntity) clearMetadataDirty() {}
func (e *throwableEntity) netPos() (float64, float64, float64, bool) {
        e.mu.Lock()
        defer e.mu.Unlock()
        return e.nx, e.ny, e.nz, e.hasNet
}
func (e *throwableEntity) setNetPos(x, y, z float64) {
        e.mu.Lock()
        e.nx, e.ny, e.nz, e.hasNet = x, y, z, true
        e.mu.Unlock()
}
func (e *throwableEntity) curRot() (float32, float32) {
        e.mu.Lock()
        defer e.mu.Unlock()
        return e.yaw, e.pitch
}
func (e *throwableEntity) netRot() (float32, float32, bool) {
        e.mu.Lock()
        defer e.mu.Unlock()
        return e.nyaw, e.npitch, e.hasRot
}
func (e *throwableEntity) setNetRot(yaw, pitch float32) {
        e.mu.Lock()
        e.nyaw, e.npitch, e.hasRot = yaw, pitch, true
        e.mu.Unlock()
}

// tick advances one flight step: inertia, gravity, block impact and the
// entity hit scan along this tick's segment (Projectile.tick 移植)。
// 珍珠的主人检查与方块/实体命中都要读共享状态（玩家列表、实体表、方块
// 网格），在 e.mu 之内取 s.mu——与箭实体同款锁序；两条消费趟只跑在
// ticker 协程上，无 ABBA 交叠。
func (e *throwableEntity) tick(s *Server) {
        e.mu.Lock()
        defer e.mu.Unlock()
        e.age++
        if e.age >= throwableLifespan {
                e.dead = true
                return
        }
        s.mu.Lock()
        // 珍珠：主人死亡/离线即消散（ENDER_PEARLS_VANISH_ON_DEATH 默认）。
        if e.kind == throwPearl {
                owner := s.playerByIDLocked(e.ownerID)
                if owner == nil || owner.dead {
                        e.dead = true
                        s.mu.Unlock()
                        return
                }
        }
        s.mu.Unlock()

        e.vx *= throwableInertia
        e.vy = e.vy*throwableInertia - throwableGravity
        e.vz *= throwableInertia
        e.px, e.py, e.pz = e.x, e.y, e.z
        e.x += e.vx
        e.y += e.vy
        e.z += e.vz
        e.updateRotation()

        // 方块命中：回退本步位移并标记撞击（命中点即本 tick 起点）。
        // entityBoxCollides 读方块网格（setBlock 持 s.mu 写入），快照读取。
        s.mu.Lock()
        blocked := s.entityBoxCollides(e.x, e.y, e.z)
        if blocked {
                e.x -= e.vx
                e.y -= e.vy
                e.z -= e.vz
                e.vx, e.vy, e.vz = 0, 0, 0
                e.impacted = true
                s.mu.Unlock()
                return
        }

        // 实体命中：本 tick 飞行线段对各候选身体盒的最近点扫描（复用箭的
        // 机制）。owner 不参与命中（原版 leftOwner 语义的保守实现）。
        var bestPlayer *player
        var bestMob *mobEntity
        bestD2 := 1e9
        for _, p := range s.players {
                if p.dead || p.gameMode != 0 || p.id == e.ownerID {
                        continue
                }
                d2 := segmentBoxDist2(e.px, e.py, e.pz, e.x, e.y, e.z, p.x, p.y, p.z)
                if d2 < bestD2 {
                        bestD2 = d2
                        bestPlayer = p
                }
        }
        for _, o := range s.entities {
                m, ok := o.(*mobEntity)
                if !ok {
                        continue
                }
                m.mu.Lock()
                dying := m.deathTicks > 0 || m.removed || m.health <= 0
                mx, my, mz := m.x, m.y, m.z
                hw, h := float64(m.def.width)/2, float64(m.def.height)
                m.mu.Unlock()
                if dying {
                        continue
                }
                d2 := segmentBoxDist2Dims(e.px, e.py, e.pz, e.x, e.y, e.z, mx, my, mz, hw, h)
                if d2 < bestD2 {
                        bestD2 = d2
                        bestPlayer = nil
                        bestMob = m
                }
        }
        s.mu.Unlock()
        if bestD2 < throwableHitR2 {
                if bestPlayer != nil {
                        e.pendingPlayer = bestPlayer.id
                } else if bestMob != nil {
                        e.pendingMob = bestMob.id
                }
                e.impacted = true
        }
}

// updateRotation follows Projectile.updateRotation: yaw/pitch from the
// current velocity so客户端修正包与自模拟轨迹一致。
func (e *throwableEntity) updateRotation() {
        hs := math.Sqrt(e.vx*e.vx + e.vz*e.vz)
        e.yaw = float32(math.Atan2(e.vx, e.vz) * 180 / math.Pi)
        e.pitch = float32(math.Atan2(e.vy, hs) * 180 / math.Pi)
}

// playerByIDLocked resolves a player entity id; caller holds Server.mu.
func (s *Server) playerByIDLocked(id int32) *player {
        for _, p := range s.players {
                if p.id == id {
                        return p
                }
        }
        return nil
}

// mobDefByName resolves a species definition by registry name (M15 鸡蛋
// 孵化用)；name follows the mobDefs table spelling ("chicken", ...).
func mobDefByName(name string) *mobDef {
        for i := range mobDefs {
                if mobDefs[i].name == name {
                        return &mobDefs[i]
                }
        }
        return nil
}

// consumeThrowableImpacts resolves impacted throwables: snowball hit
// feedback (event 3 + 0-damage knockback), egg chick hatch, pearl owner
// teleport. Runs on the ticker with Server.mu held by the caller
// (consumeArrowHits / consumePotionImpacts 锁序)。
func (s *Server) consumeThrowableImpacts() {
        s.mu.Lock()
        defer s.mu.Unlock()
        for _, ent := range s.entities {
                t, ok := ent.(*throwableEntity)
                if !ok {
                        continue
                }
                t.mu.Lock()
                if !t.impacted || t.dead {
                        t.mu.Unlock()
                        continue
                }
                t.impacted = false
                t.dead = true
                kind := t.kind
                id := t.id
                ownerID := t.ownerID
                hitPlayer := t.pendingPlayer
                t.pendingPlayer = 0
                hitMob := t.pendingMob
                t.pendingMob = 0
                // oldPosition（本 tick 起点）与撞击点快照。
                ox, oy, oz := t.px, t.py, t.pz
                hx, hy, hz := t.x, t.y, t.z
                t.mu.Unlock()

                switch kind {
                case throwSnowball:
                        s.resolveSnowballImpact(id, ownerID, hitPlayer, hitMob, hx, hy, hz)
                case throwEgg:
                        s.resolveEggImpact(id, ownerID, hitPlayer, hx, hy, hz)
                case throwPearl:
                        s.resolvePearlImpact(id, ownerID, hitPlayer, hitMob, ox, oy, oz, hx, hy, hz)
                }
        }
}

// resolveSnowballImpact mirrors Snowball.onHit/onHitEntity: the entity
// event 3 makes every client burst 8 item break particles at the impact
// point; the hit entity takes hurt(thrown, 0) —— 无伤害但有受击动画、
// 击退与受击音效（Blaze 命中应为 3 点，服务器尚无烈焰人，判据先落位）。
func (s *Server) resolveSnowballImpact(projectileID, ownerID, hitPlayer, hitMob int32, hx, hy, hz float64) {
        owner := s.playerByIDLocked(ownerID)
        s.broadcastEntityEventLocked(projectileID, 3)
        if hitMob != 0 {
                m, isMob := s.entities[hitMob].(*mobEntity)
                if !isMob {
                        return
                }
                dmg := float32(0)
                if m.def.name == "blaze" {
                        dmg = 3 // entity instanceof Blaze ? 3 : 0
                }
                if owner != nil {
                        s.hurtMobTypeLocked(m, owner, dmg, v776.DamageTypeThrown)
                } else {
                        s.damageMobLocked(m, dmg, v776.DamageTypeThrown, -1, -1)
                }
        }
        if hitPlayer != 0 {
                victim := s.playerByIDLocked(hitPlayer)
                if victim != nil && !victim.dead && victim.gameMode == 0 {
                        s.hitPlayerFeedbackLocked(victim, owner, 0, v776.DamageTypeThrown, projectileID, hx, hy, hz)
                }
        }
}

// resolveEggImpact mirrors ThrownEgg.onHit: 1/8 chance to hatch chicks
// (1, or 4 on a further 1/32), each a baby (age -24000); then the break
// event. 0 伤害命中实体（hurt(thrown, 0.0F)）。
func (s *Server) resolveEggImpact(projectileID, ownerID, hitPlayer int32, hx, hy, hz float64) {
        owner := s.playerByIDLocked(ownerID)
        if hitPlayer != 0 {
                victim := s.playerByIDLocked(hitPlayer)
                if victim != nil && !victim.dead && victim.gameMode == 0 {
                        s.hitPlayerFeedbackLocked(victim, owner, 0, v776.DamageTypeThrown, projectileID, hx, hy, hz)
                }
        }
        if rand.Intn(8) == 0 {
                count := 1
                if rand.Intn(32) == 0 {
                        count = 4
                }
                for i := 0; i < count; i++ {
                        def := mobDefByName("chicken")
                        if def == nil {
                                break
                        }
                        chick := newMobEntity(def, s.allocEntityID(), hx, hy, hz)
                        chick.babyTicks = babyChickenAge
                        chick.metaDirty = true
                        s.spawnEntityLocked(chick)
                }
        }
        s.broadcastEntityEventLocked(projectileID, 3)
}

// resolvePearlImpact mirrors ThrownEnderpearl.onHit: 32 portal particles,
// owner teleport to oldPosition, resetFallDistance, 5 hp ender_pearl
// damage, then the teleport sound. 末影螨 5% 概率待 endermite 实体落地后
// 接入（本里程碑不含新生物）。
func (s *Server) resolvePearlImpact(projectileID, ownerID, hitPlayer, hitMob int32, ox, oy, oz, hx, hy, hz float64) {
        owner := s.playerByIDLocked(ownerID)
        // 32 颗 portal 粒子逐颗下发：位置 y+rand·2、偏移 x/z 高斯
        // （ThrownEnderpearl.onHit 的 32 次 addParticle 等价体）。
        for i := 0; i < 32; i++ {
                body := protocol.NewWriter()
                body.VarInt(v776.PacketPlayLevelParticles)
                java.WritePlayLevelParticles(body, v776.ParticlePortal,
                        hx, hy+rand.Float64()*2.0, hz,
                        rand.NormFloat64(), 0, rand.NormFloat64(), 0, 1)
                s.broadcastToNearbyLocked(body.Bytes(), hx, hy, hz)
        }
        if hitMob != 0 {
                // ThrownEnderpearl.onHitEntity：hurt(thrown, 0.0F)。
                if m, isMob := s.entities[hitMob].(*mobEntity); isMob && owner != nil {
                        s.hurtMobTypeLocked(m, owner, 0, v776.DamageTypeThrown)
                }
        }
        if hitPlayer != 0 && hitPlayer != ownerID {
                victim := s.playerByIDLocked(hitPlayer)
                if victim != nil && !victim.dead && victim.gameMode == 0 {
                        s.hitPlayerFeedbackLocked(victim, owner, 0, v776.DamageTypeThrown, projectileID, hx, hy, hz)
                }
        }
        if owner == nil || owner.dead {
                // isAllowedToTeleportOwner：主人不在/已死 → 只消散。
                return
        }
        // ServerPlayer.teleport(TeleportTransition)：位置=oldPosition，
        // 速度清零（Relative.DELTA），旋转保持（Relative.ROTATION）。
        teleportPlayer(s, owner, ox, oy, oz)
        owner.fallDistance = 0 // resetFallDistance：传送不计摔落
        s.damagePlayerLocked(owner, pearlDamage, v776.DamageTypeEnderPearl, -1, -1)
        s.broadcastSoundLocked("minecraft:entity.player.teleport", v776.SoundSourcePlayers,
                float32(ox), float32(oy), float32(oz), 1.0, randomPitch())
}

// hitPlayerFeedbackLocked applies a zero-damage projectile hit on a
// player: hurt animation + damage event + knockback + hurt sound ——
// vanilla LivingEntity.hurt(0) 的可感知部分（生命不变）。damage>0 时走
// damagePlayerLocked 正常扣血。
func (s *Server) hitPlayerFeedbackLocked(victim *player, attacker *player, dmg float32, dmgType int32, projectileID int32, hx, hy, hz float64) {
        if dmg > 0 {
                cause := int32(-1)
                direct := int32(-1)
                if attacker != nil {
                        cause = attacker.id
                }
                direct = projectileID
                s.damagePlayerLocked(victim, dmg, dmgType, cause, direct)
                return
        }
        if attacker != nil {
                _ = victim.conn.sendDamageEvent(victim.id, dmgType, attacker.id, projectileID)
        } else {
                _ = victim.conn.sendDamageEvent(victim.id, dmgType, -1, projectileID)
        }
        _ = victim.conn.sendHurtAnimation(victim.id, victim.yaw)
        s.broadcastSoundLocked("minecraft:entity.player.hurt", v776.SoundSourcePlayers,
                float32(victim.x), float32(victim.y+0.9), float32(victim.z), 1.0, randomPitch())
        // 击退沿命中点→受害者方向（与 hurt() 的 knockback 同向）。
        dx := victim.x - hx
        dz := victim.z - hz
        d := math.Sqrt(dx*dx + dz*dz)
        if d > 0.001 {
                kx, kz := dx/d*mobKnockbackH, dz/d*mobKnockbackH
                kb := protocol.NewWriter()
                kb.VarInt(v776.PacketPlaySetEntityMotion)
                java.WriteSetEntityMotion(kb, victim.id, kx, mobKnockbackV, kz)
                _ = victim.conn.sendPacket(kb.Bytes())
        }
}

// broadcastEntityEventLocked fans out an entity event (event 3 = the
// client-side item break particle burst) to every tracker.
// Caller holds Server.mu.
func (s *Server) broadcastEntityEventLocked(entityID int32, event byte) {
        body := protocol.NewWriter()
        body.VarInt(v776.PacketPlayEntityEvent)
        java.WriteEntityEvent(body, entityID, event)
        for _, p := range s.players {
                if p.seenEnt[entityID] || p.id == entityID {
                        _ = p.conn.sendPacket(body.Bytes())
                }
        }
}

// broadcastToNearbyLocked sends a pre-encoded packet to every player
// within the shared 48-block proximity radius. Caller holds Server.mu.
func (s *Server) broadcastToNearbyLocked(payload []byte, x, y, z float64) {
        for _, p := range s.players {
                dx, dy, dz := p.x-x, p.y-y, p.z-z
                if dx*dx+dy*dy+dz*dz <= soundRange2 {
                        _ = p.conn.sendPacket(payload)
                }
        }
}

// throwThrowable consumes one snowball/egg/ender pearl (survival only;
// creative keeps the stack) and spawns the projectile. vanilla
// XxxItem.use 的顺序：投掷音效 → spawnProjectileFromRotation →
// itemStack.consume（主手挥手由客户端侧 SUCCESS 触发，这里广播给他人）。
func (c *conn) throwThrowable(kind throwableKind) {
        s := c.s
        p := c.player
        if p == nil || p.dead {
                return
        }

        s.mu.Lock()
        // 生存模式扣件（vanilla itemStack.consume(1, player)）。
        if p.gameMode == 0 {
                slot := p.slots[p.heldSlot]
                slot.count--
                if slot.count <= 0 {
                        slot = invSlot{}
                }
                p.slots[p.heldSlot] = slot
                p.conn.sendSlot(p.heldSlot, slot)
        }
        e := newThrowable(s, p, kind)
        s.spawnEntityLocked(e)
        s.broadcastSwing(p)
        s.mu.Unlock()

        // 投掷音效：NEUTRAL，音量 0.5，音调 0.4/(rand·0.4+0.8)。
        sound := "minecraft:entity.snowball.throw"
        switch kind {
        case throwEgg:
                sound = "minecraft:entity.egg.throw"
        case throwPearl:
                sound = "minecraft:entity.ender_pearl.throw"
        }
        s.broadcastSound(sound, v776.SoundSourceNeutral,
                float32(p.x), float32(p.y+1.62), float32(p.z), 0.5, 0.4/(rand.Float32()*0.4+0.8))
}

// encodeThrowableSpawn writes the add_entity payload: launch velocity so
// the client free-simulates the arc between server corrections（与药水
// 实体同一修复原则：无速度会让客户端把实体画在原点）。
func encodeThrowableSpawn(w *protocol.Writer, e *throwableEntity) {
        e.mu.Lock()
        defer e.mu.Unlock()
        java.WriteAddEntity(w, e.id, e.uuid, e.typeID(), e.x, e.y, e.z,
                e.vx, e.vy, e.vz,
                angleByte(e.pitch), angleByte(e.yaw), angleByte(e.yaw), 0)
}

// writeThrowableMetadata encodes the DATA_ITEM_STACK sync (index 8,
// item_stack serializer): the client renders the flying item from it.
func writeThrowableMetadata(w *protocol.Writer, e *throwableEntity) {
        e.mu.Lock()
        defer e.mu.Unlock()
        item := itemIDByName["minecraft:snowball"]
        switch e.kind {
        case throwEgg:
                item = itemIDByName["minecraft:egg"]
        case throwPearl:
                item = itemIDByName["minecraft:ender_pearl"]
        }
        java.WriteSetEntityDataItem(w, e.id, item, 1, 0)
}
