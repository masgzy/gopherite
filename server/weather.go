package server

// 天气系统（M12）：全局晴/雨/雷暴状态机 + 26.2 Game Event 同步 +
// 雷暴落雷（闪电实体、雷击伤害与音效、起火）。
//
// 简化点（相对原版，P 阶段再收敛）：
//   - 单一全局状态（原版按生物群系降水渲染由客户端处理）；
//   - 周期取原版量级的简化区间；
//   - 落雷点选在随机玩家附近（原版按 region 随机；无玩家不落雷）。

import (
	"log"
	"math/rand"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// 天气状态（与 Game Event 语义对齐）。
const (
	weatherClear   = int32(0)
	weatherRain    = int32(1)
	weatherThunder = int32(2)
)

// 天气 Game Event 事件码（26.2 ClientboundGameEventPacket）。
const (
	gameEventBeginRaining = 1
	gameEventEndRaining   = 2
	gameEventRainLevel    = 7
	gameEventThunderLevel = 9
)

// 天气周期（ticks）：晴 → 雨 →（半数概率）雷暴 → 雨 → 晴。
const (
	weatherClearMin = 12000 // 10 min
	weatherClearMax = 60000 // 50 min
	weatherRainMin  = 6000  // 5 min
	weatherRainMax  = 12000 // 10 min
	weatherBoltMin  = 3000  // 2.5 min 雷暴段
	weatherBoltMax  = 10000 // ~8 min
	thunderOdds     = 2     // 雨结束后 1/2 概率升级雷暴
	lightningOdds   = 9000  // 雷暴期每 tick 1/N 概率落雷
	lightningDamage = float32(5)
	lightningReach  = 3.5
)

// lightningEntity 是一次落雷的视觉实体：26.2 直接走 AddEntity
// (type minecraft:lightning_bolt)，客户端自行播放闪烁，2 tick 后移除。
type lightningEntity struct {
	id      int32
	uuid    [16]byte
	x, y, z float64
	life    int32 // 剩余存活 ticks
}

func (e *lightningEntity) entityID() int32                   { return e.id }
func (e *lightningEntity) typeID() int32                     { return v776.EntityTypeLightningBolt }
func (e *lightningEntity) xPos() (float64, float64, float64) { return e.x, e.y, e.z }
func (e *lightningEntity) onGroundFlag() bool                { return false }
func (e *lightningEntity) netPos() (float64, float64, float64, bool) {
	return 0, 0, 0, false
}
func (e *lightningEntity) setNetPos(float64, float64, float64) {}
func (e *lightningEntity) metadataDirty() bool                 { return false }
func (e *lightningEntity) clearMetadataDirty()                 {}

func (e *lightningEntity) alive() bool { return e.life > 0 }

func (e *lightningEntity) tick(s *Server) {
	e.life-- // alive() 变 false 后由 tickEntities 统一回收并广播移除
}

// tickWeather 推进天气状态机一格；ticker 协程专用，自取 Server.mu。
func (s *Server) tickWeather() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.weatherTicks > 0 {
		s.weatherTicks--
	}
	if s.weatherTicks <= 0 {
		s.advanceWeatherLocked()
	}
	if s.weather == weatherThunder {
		s.maybeLightningLocked()
	}
}

// advanceWeatherLocked 转移到下一天气并广播。Caller holds s.mu.
func (s *Server) advanceWeatherLocked() {
	switch s.weather {
	case weatherClear:
		s.setWeatherLocked(weatherRain, weatherTicksLocked(weatherRainMin, weatherRainMax))
	case weatherRain:
		if rand.Intn(thunderOdds) == 0 {
			s.setWeatherLocked(weatherThunder, weatherTicksLocked(weatherBoltMin, weatherBoltMax))
		} else {
			s.setWeatherLocked(weatherClear, weatherTicksLocked(weatherClearMin, weatherClearMax))
		}
	default: // thunder
		s.setWeatherLocked(weatherRain, weatherTicksLocked(weatherRainMin, weatherRainMax))
	}
}

func weatherTicksLocked(min, max int64) int64 {
	if max <= min {
		return min
	}
	return min + rand.Int63n(max-min)
}

// setWeatherLocked 设置新状态并全员同步 Game Event。Caller holds s.mu.
// 原版在 Begin/End Raining 之外还会连发两个强度事件让客户端平滑过渡。
func (s *Server) setWeatherLocked(w int32, ticks int64) {
	s.weather = w
	s.weatherTicks = ticks

	raining := w != weatherClear
	// 单帧组装：每个事件一条包，全部玩家同一份。
	frames := make([][]byte, 0, 4)
	push := func(event byte, value float32) {
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlayGameEvent)
		java.WritePlayGameEvent(body, event, value)
		frames = append(frames, body.Bytes())
	}
	if raining {
		push(gameEventBeginRaining, 0)
	} else {
		push(gameEventEndRaining, 0)
	}
	push(gameEventRainLevel, boolF32(raining))
	push(gameEventThunderLevel, boolF32(w == weatherThunder))
	for _, p := range s.players {
		for _, f := range frames {
			_ = p.conn.sendPacket(f)
		}
	}
	if raining && w != s.lastLoggedWeather {
		log.Printf(ui.Info("✦ ")+"天气变化：%s", weatherName(w))
	}
	s.lastLoggedWeather = w
}

func boolF32(b bool) float32 {
	if b {
		return 1
	}
	return 0
}

func weatherName(w int32) string {
	switch w {
	case weatherRain:
		return "降雨"
	case weatherThunder:
		return "雷暴"
	}
	return "晴朗"
}

// sendWeather 把当前天气发给单个玩家（join 同步；调用方无需持锁，
// 内部快照后发帧）。
func (c *conn) sendWeather() {
	s := c.s
	s.mu.Lock()
	w := s.weather
	s.mu.Unlock()

	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayGameEvent)
	raining := w != weatherClear
	if raining {
		java.WritePlayGameEvent(body, gameEventBeginRaining, 0)
	} else {
		java.WritePlayGameEvent(body, gameEventEndRaining, 0)
	}
	_ = c.sendPacket(body.Bytes())

	body = protocol.NewWriter()
	body.VarInt(v776.PacketPlayGameEvent)
	java.WritePlayGameEvent(body, gameEventRainLevel, boolF32(raining))
	_ = c.sendPacket(body.Bytes())

	body = protocol.NewWriter()
	body.VarInt(v776.PacketPlayGameEvent)
	java.WritePlayGameEvent(body, gameEventThunderLevel, boolF32(w == weatherThunder))
	_ = c.sendPacket(body.Bytes())
}

// maybeLightningLocked 在雷暴期掷骰落雷。落点取随机玩家周围
// 12-48 格的随机偏移，无玩家直接返回。Caller holds s.mu.
func (s *Server) maybeLightningLocked() {
	if len(s.players) == 0 || rand.Intn(lightningOdds) != 0 {
		return
	}
	i := rand.Intn(len(s.players))
	var px, pz float64
	for _, p := range s.players {
		if i == 0 {
			px, pz = p.x, p.z
			break
		}
		i--
	}
	dx := float64(rand.Intn(96) - 48)
	dz := float64(rand.Intn(96) - 48)
	s.spawnLightningLocked(px+dx, pz+dz)
}

// spawnLightningLocked 在 (x, z) 的地表落雷：闪电实体 2 ticks、雷声与
// 落地音效、附近生物/玩家伤害并点燃、地表起火。Caller holds s.mu.
func (s *Server) spawnLightningLocked(x, z float64) {
	y := float64(s.surfaceY(int(x), int(z)))
	e := &lightningEntity{id: s.allocEntityID(), x: x, y: y, z: z, life: 2}
	_, _ = rand.Read(e.uuid[:])
	e.uuid[6] = (e.uuid[6] & 0x0F) | 0x40
	e.uuid[8] = (e.uuid[8] & 0x3F) | 0x80
	s.spawnEntityLocked(e)
	s.broadcastSoundLocked("minecraft:entity.lightning_bolt.thunder",
		v776.SoundSourceWeather, float32(x), float32(y), float32(z), 100.0, 1.0)
	s.broadcastSoundLocked("minecraft:entity.lightning_bolt.impact",
		v776.SoundSourceWeather, float32(x), float32(y), float32(z), 2.0, 0.5)

	// 雷击伤害：reach 内玩家与生物各 5 点；生物点燃（原版 lightning_bolt
	// 伤害类型简化复用 in_fire，死亡文案可能不一致——P 阶段接入完整表；
	// 玩家无燃烧状态字段，只受直接伤害）。
	for _, p := range s.players {
		if dist2D(p.x, p.z, x, z) > lightningReach || absF64(p.y-y) > lightningReach {
			continue
		}
		s.damagePlayerLocked(p, lightningDamage, v776.DamageTypeInFire, -1, -1)
	}
	for _, ent := range s.entities {
		m, ok := ent.(*mobEntity)
		if !ok || m.removed {
			continue
		}
		m.mu.Lock()
		dx, dz := m.x-x, m.z-z
		near := dx*dx+dz*dz <= lightningReach*lightningReach &&
			absF64(m.y-y) <= lightningReach
		if near {
			m.fireTicks = 8 * 20
		}
		m.mu.Unlock()
		if near {
			s.damageMobLocked(m, lightningDamage, v776.DamageTypeInFire, -1, -1)
		}
	}

	// 地表起火：落点为实心方块且上方为空气时点燃。
	surface := int(s.surfaceY(int(x), int(z)))
	if s.world.getBlock(int(x), surface-1, int(z)) != stateAir &&
		s.world.getBlock(int(x), surface, int(z)) == stateAir {
		if fire := defaultStateOf("minecraft:fire"); fire > 0 {
			s.world.setBlock(int(x), surface, int(z), int32(fire))
			s.broadcastBlockUpdateLocked(int32(x), int32(surface), int32(z), int32(fire))
		}
	}
}

func dist2D(ax, az, bx, bz float64) float64 {
	dx, dz := ax-bx, az-bz
	return dx*dx + dz*dz
}

func absF64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
