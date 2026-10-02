package server

// TNT（M12）：打火石点燃 → 引信实体 → 原版量级爆炸。爆炸体从 M9
// 爬行者实现中提取为通用 explodeAtLocked（每玩家击退向量、球体弹坑、
// 双侧伤害），爬行者与 TNT 共用；弹坑中炸到 TNT 方块会连锁点燃。

import (
	"log"
	"math"
	"math/rand"
	"sync"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// vanilla PrimedTnt parameters.
const (
	tntGravity     = 0.04
	tntFriction    = 0.98
	tntFuseTicks   = 80 // vanilla: 4 seconds
	tntRadius      = 4  // vanilla explosion power
	tntHalfWidth   = 0.49
	tntChainMin    = 8 // 连锁引信下限（原版 fuse = 8~40 随机）
	tntChainSpread = 33
)

// tntEntity 是点燃后的 PrimedTnt：重力下落、引信闪烁、到点爆炸。
// tick 内不持 Server.mu（与 item/mob 同序），爆炸意图经 pendingExplode
// 由 tickEntities 主循环在持锁态执行。
type tntEntity struct {
	mu         sync.Mutex
	id         int32
	uuid       [16]byte
	x, y, z    float64
	vx, vy, vz float64
	ground     bool
	fuse       int32

	causeID  int32 // 点燃者（玩家实体 id；-1 = 连锁/环境）
	sourceID int32 // 爆炸入账用的实体 id（自身）

	pendingExplode bool
	dead           bool

	nx, ny, nz float64
	hasNet     bool
}

func newTNTEntity(id, causeID int32, x, y, z float64, fuse int32) *tntEntity {
	var u [16]byte
	_, _ = rand.Read(u[:])
	u[6] = (u[6] & 0x0F) | 0x40
	u[8] = (u[8] & 0x3F) | 0x80
	return &tntEntity{
		id: id, uuid: u, causeID: causeID, sourceID: id,
		x: x, y: y, z: z,
		vx:   (rand.Float64() - 0.5) * 0.2,
		vy:   0.2,
		vz:   (rand.Float64() - 0.5) * 0.2,
		fuse: fuse,
	}
}

func (e *tntEntity) entityID() int32     { return e.id }
func (e *tntEntity) typeID() int32       { return v776.EntityTypeTNT }
func (e *tntEntity) onGroundFlag() bool  { e.mu.Lock(); defer e.mu.Unlock(); return e.ground }
func (e *tntEntity) alive() bool         { e.mu.Lock(); defer e.mu.Unlock(); return !e.dead }
func (e *tntEntity) metadataDirty() bool { return false }
func (e *tntEntity) clearMetadataDirty() {}
func (e *tntEntity) xPos() (float64, float64, float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.x, e.y, e.z
}
func (e *tntEntity) netPos() (float64, float64, float64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nx, e.ny, e.nz, e.hasNet
}
func (e *tntEntity) setNetPos(x, y, z float64) {
	e.mu.Lock()
	e.nx, e.ny, e.nz, e.hasNet = x, y, z, true
	e.mu.Unlock()
}

// fuseLeft 供测试观察引信余量。
func (e *tntEntity) fuseLeft() int32 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.fuse
}

// tick 推进一格：重力 + 引信倒计时（与 itemEntity 同一套物理参数，
// 半宽用 tntHalfWidth）。
func (e *tntEntity) tick(s *Server) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.dead {
		return
	}
	if e.fuse > 0 {
		e.fuse--
		if e.fuse == 0 {
			e.pendingExplode = true
			e.dead = true
			return
		}
	}

	e.vy -= tntGravity
	if e.ground {
		e.vx *= tntFriction
		e.vz *= tntFriction
	} else {
		e.vx *= tntFriction
		e.vz *= tntFriction
	}

	ny := e.y + e.vy
	if e.vy != 0 && s.entityBoxCollidesDim(e.x, ny, e.z, tntHalfWidth, 0.98) {
		if e.vy < 0 {
			ny = math.Floor(ny) + 1
		}
		e.vy = 0
	}
	e.y = ny
	e.ground = s.entityBoxCollidesDim(e.x, e.y-0.001, e.z, tntHalfWidth, 0.98)

	nx := e.x + e.vx
	if e.vx != 0 && s.entityBoxCollidesDim(nx, e.y, e.z, tntHalfWidth, 0.98) {
		nx = e.x
		e.vx = 0
	}
	e.x = nx
	nz := e.z + e.vz
	if e.vz != 0 && s.entityBoxCollidesDim(e.x, e.y, nz, tntHalfWidth, 0.98) {
		nz = e.z
		e.vz = 0
	}
	e.z = nz
}

// entityBoxCollidesDim 是 entityBoxCollides 的参数化版本：r 为半宽，
// h 为箱体高度（TNT 0.98 高、底部 0.49 半宽）。
func (s *Server) entityBoxCollidesDim(x, y, z, r, h float64) bool {
	for bx := int(math.Floor(x - r)); bx <= int(math.Floor(x+r)); bx++ {
		for by := int(math.Floor(y)); by <= int(math.Floor(y+h)); by++ {
			for bz := int(math.Floor(z - r)); bz <= int(math.Floor(z+r)); bz++ {
				if s.world.getBlock(bx, by, bz) != stateAir {
					return true
				}
			}
		}
	}
	return false
}

// igniteTNTLocked 把 (x, y, z) 的 TNT 方块替换为点燃实体。fuse<=0 时用
// 原版默认 80 ticks。连锁路径的格子已被弹坑清空——方块清理尽力而为，
// 实体必须总是生成。Caller holds s.mu.
func (s *Server) igniteTNTLocked(x, y, z int, causeID int32, fuse int32) {
	if fuse <= 0 {
		fuse = tntFuseTicks
	}
	if cur := s.world.getBlock(x, y, z); cur != stateAir {
		s.world.setBlock(x, y, z, stateAir)
		s.broadcastBlockUpdateLocked(int32(x), int32(y), int32(z), stateAir)
	}
	e := newTNTEntity(s.allocEntityID(), causeID, float64(x)+0.5, float64(y), float64(z)+0.5, fuse)
	s.spawnEntityLocked(e)
	s.broadcastSoundLocked("minecraft:entity.tnt.primed", v776.SoundSourceBlocks,
		float32(x)+0.5, float32(y)+0.5, float32(z)+0.5, 1.0, 1.0)
}

// explodeAtLocked 是通用爆炸：客户端爆炸包（每人独立击退向量）、球体
// 弹坑（1/radius 掉落概率）、弹坑内容器倾泻、TNT 方块连锁点燃、对玩家
// 与生物的原版伤害曲线。Caller holds s.mu。
func (s *Server) explodeAtLocked(cx, cy, cz float64, radius float64, sourceID, causeID int32) {
	reach := 2 * radius

	// Client feedback: one explosion packet per player with its own
	// knockback (vanilla sends the per-player vector inside the packet).
	for _, p := range s.players {
		dx := p.x - cx
		dy := (p.y + 0.9) - cy
		dz := p.z - cz
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		var kx, ky, kz float64
		if d < reach && d > 0.001 {
			frac := 1 - d/reach
			kx = dx / d * frac * 1.2
			ky = math.Max(0.3*frac, 0.15)
			kz = dz / d * frac * 1.2
		}
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlayExplode)
		java.WritePlayExplode(body, cx, cy, cz, float32(radius), 0, kx, ky, kz,
			29 /* explosion_emitter */, "minecraft:entity.generic.explode")
		_ = p.conn.sendPacket(body.Bytes())
	}

	// Block destruction: a sphere with vanilla's 1/radius drop chance.
	for bx := int(cx) - int(radius); bx <= int(cx)+int(radius); bx++ {
		for by := int(cy) - int(radius); by <= int(cy)+int(radius); by++ {
			for bz := int(cz) - int(radius); bz <= int(cz)+int(radius); bz++ {
				ddx, ddy, ddz := float64(bx)+0.5-cx, float64(by)+0.5-cy, float64(bz)+0.5-cz
				if ddx*ddx+ddy*ddy+ddz*ddz > radius*radius {
					continue
				}
				st := s.world.getBlock(bx, by, bz)
				if st == stateAir {
					continue
				}
				if !s.world.setBlock(bx, by, bz, stateAir) {
					continue
				}
				s.broadcastBlockUpdateLocked(int32(bx), int32(by), int32(bz), stateAir)
				// Container block entities spill everything (vanilla
				// explosions always drop chest/furnace contents).
				if b := s.blockEnts[[3]int{bx, by, bz}]; b != nil {
					s.removeBlockEntity(b)
				}
				// 连锁：弹坑里的 TNT 方块变成点燃实体（原版随机引信）。
				if blockNameOf(int(st)) == "minecraft:tnt" {
					s.igniteTNTLocked(bx, by, bz, causeID, tntChainMin+rand.Int31n(tntChainSpread))
					continue
				}
				if rand.Int31n(int32(radius)) == 0 {
					s.spawnExplosionDrop(bx, by, bz, st)
				}
			}
		}
	}

	// Entity damage: vanilla curve (1-d/2R) shaped, both players and mobs.
	for _, p := range s.players {
		dx := p.x - cx
		dy := (p.y + 0.9) - cy
		dz := p.z - cz
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if d >= reach {
			continue
		}
		frac := 1 - d/reach
		dmg := float32((frac*frac+frac)/2*7*radius + 1)
		s.damagePlayerLocked(p, dmg, v776.DamageTypeExplosion, sourceID, causeID)
	}
	for _, ent := range s.entities {
		m2, ok := ent.(*mobEntity)
		if !ok || m2.id == sourceID {
			continue
		}
		m2.mu.Lock()
		if m2.removed || m2.deathTicks > 0 {
			m2.mu.Unlock()
			continue
		}
		dx := m2.x - cx
		dy := (m2.y + 0.9) - cy
		dz := m2.z - cz
		d := math.Sqrt(dx*dx + dy*dy + dz*dz)
		if d < reach {
			frac := 1 - d/reach
			dmg := float32((frac*frac+frac)/2*7*radius + 1)
			// 锁序：mobEntity.mu -> Server.mu。先解 mob 锁再伤害。
			m2.mu.Unlock()
			s.damageMobLocked(m2, dmg, v776.DamageTypeExplosion, sourceID, causeID)
			continue
		}
		m2.mu.Unlock()
	}

	log.Printf(ui.Info("✦ ")+"爆炸 (%.1f, %.1f, %.1f) 半径 %.0f", cx, cy, cz, radius)
}

// explodeCreeperLocked 变为通用爆炸的爬行者薄壳。
func (s *Server) explodeCreeperLocked(m *mobEntity) {
	m.mu.Lock()
	cx, cy, cz := m.x, m.y+0.0625, m.z
	id := m.id
	m.fireTicks = 0
	m.deathTicks = 0
	m.removed = true
	m.mu.Unlock()
	s.explodeAtLocked(cx, cy, cz, creeperRadius, id, id)
}

// resolveTNTIntents 在主循环持锁态处理点燃实体的爆炸意图。
// Called from tickEntities; caller holds no lock.
func (s *Server) resolveTNTIntents() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ent := range s.entities {
		if e, ok := ent.(*tntEntity); ok {
			e.mu.Lock()
			fired := e.pendingExplode
			e.pendingExplode = false
			cause := e.causeID
			x, y, z := e.x, e.y+0.0625, e.z
			e.mu.Unlock()
			if fired {
				s.explodeAtLocked(x, y, z, tntRadius, e.id, cause)
			}
		}
	}
}
