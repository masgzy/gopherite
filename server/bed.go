package server

// 床（M12）：两格放置（facing + part=head/foot）、夜晚睡觉跳过昼夜、
// 设置重生点、破坏联动。睡觉姿势（metadata pose + 相机）与占领状态
// 留待 P 阶段；重生点为内存态（服务器重启后回世界出生点），原版
// playerdata NBT 持久化随后续存档里程碑接入。

import (
	"log"
	"strings"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol/java"
)

// facingOffset 返回水平朝向的方块偏移（north=-z 等）。
func facingOffset(facing string) (int, int) {
	switch facing {
	case "north":
		return 0, -1
	case "south":
		return 0, 1
	case "west":
		return -1, 0
	case "east":
		return 1, 0
	}
	return 0, 1
}

// bedHalfOffset 计算床另一半的坐标：facing 指向 head（head 在 foot 的
// facing 方向）。因此当前格是 head 时另一半在反方向，是 foot 时在正方向。
func bedHalfOffset(facing, part string) (int, int) {
	dx, dz := facingOffset(facing)
	if part == "head" {
		return -dx, -dz
	}
	return dx, dz
}

// placeBed 放置一张双格床：foot 在目标格，head 在玩家朝向方向。
// 两格都必须为空。返回是否成功。Caller holds Server.mu（世界写）。
func (s *Server) placeBed(block string, x, y, z int, facing string) bool {
	hdx, hdz := facingOffset(facing)
	hx, hz := x+hdx, z+hdz
	if s.world.getBlock(hx, y, hz) != stateAir {
		return false
	}
	head := stateIDOf(block, map[string]string{"facing": facing, "part": "head", "occupied": "false"})
	foot := stateIDOf(block, map[string]string{"facing": facing, "part": "foot", "occupied": "false"})
	if head <= 0 || foot <= 0 {
		return false
	}
	// 先放 head：目标格（foot）由调用方放置路径持有目标位置语义。
	if !s.world.setBlock(hx, y, hz, head) {
		return false
	}
	s.broadcastBlockUpdateLocked(int32(hx), int32(y), int32(hz), head)
	return true
}

// placeBedAndConsume 在 UseItemOn 放置路径上放双格床并消耗一个物品。
// foot 落在被点击面相邻的目标格，head 落在其玩家朝向方向。
// Caller holds no lock（内部对齐 placeBlock 的锁序）。
func (c *conn) placeBedAndConsume(block string, u java.ServerboundUseItemOn, held invSlot) {
	p := c.player
	if u.Face < 0 || int(u.Face) >= len(faceOffset) {
		return
	}
	off := faceOffset[u.Face]
	x, y, z := int(u.X)+off[0], int(u.Y)+off[1], int(u.Z)+off[2]

	c.s.mu.Lock()
	defer c.s.mu.Unlock()
	if c.s.world.getBlock(x, y, z) != stateAir {
		return
	}
	foot := stateIDOf(block, map[string]string{"facing": facingFromYaw(p.yaw), "part": "foot", "occupied": "false"})
	if foot <= 0 {
		return
	}
	// head 先放：placeBed 校验 head 格为空并写 head 态。
	if !c.s.placeBed(block, x, y, z, facingFromYaw(p.yaw)) {
		return
	}
	if !c.s.world.setBlock(x, y, z, foot) {
		// foot 写入失败则回滚 head，避免悬空半床。
		hdx, hdz := facingOffset(facingFromYaw(p.yaw))
		c.s.world.setBlock(x+hdx, y, z+hdz, stateAir)
		c.s.broadcastBlockUpdateLocked(int32(x+hdx), int32(y), int32(z+hdz), stateAir)
		return
	}
	c.s.broadcastBlockUpdateLocked(int32(x), int32(y), int32(z), foot)
	held.count--
	p.slots[p.heldSlot] = held
	c.sendSlot(p.heldSlot, held)
	log.Printf(ui.Success("OK ")+"%s 放置了 %s (%d, %d, %d)", p.name, block, x, y, z)
}

// interactBed 处理玩家右键床（conn 薄壳，负责提示文案）。
// Caller holds Server.mu。
func (c *conn) interactBed(x, y, z int) {
	p := c.player
	if p == nil {
		return
	}
	if msg := c.s.trySleepLocked(p, x, y, z); msg != "" {
		_ = c.sendSystemChat(msg)
	}
}

// trySleepLocked 执行睡觉核心：夜晚设置重生点并把时间推进到清晨；
// 白天拒绝。返回给玩家的提示文案（空表示无提示）。
// Caller holds Server.mu。
func (s *Server) trySleepLocked(p *player, x, y, z int) string {
	if !s.isNightLocked() {
		return "§7你只能在夜晚睡觉"
	}
	// 重生点：床头顶格（原版把重生锚定在床的位置；此处取相邻站位）。
	facing := "north"
	if props := blockPropsOf(int(s.world.getBlock(x, y, z))); props != nil {
		if f, ok := props["facing"]; ok {
			facing = f
		}
	}
	hdx, hdz := facingOffset(facing)
	p.spawnX, p.spawnY, p.spawnZ = float64(x+hdx)+0.5, float64(y)+1, float64(z+hdz)+0.5
	p.hasSpawn = true

	// M17: slept_in_bed 触发（adventure 页 "Sweet Dreams"）。
	s.advEventSlept(p)

	// 跳过夜晚：把当日剩余时间一次性推到清晨 0 点。
	t := s.timeOfDayLocked()
	s.timeTicks += ticksPerDay - t
	s.broadcastTimeLocked()
	log.Printf(ui.Success("OK ")+"%s 在 (%d, %d, %d) 睡觉，夜晚已跳过", p.name, x, y, z)
	return "§7已设置重生点，一觉睡到天亮§f…"
}

// respawnPositionLocked 选择重生坐标：床重生点优先，否则世界出生点。
// Caller holds Server.mu。
func respawnPositionLocked(s *Server, p *player) {
	if p.hasSpawn {
		p.x, p.y, p.z = p.spawnX, p.spawnY, p.spawnZ
		return
	}
	p.x, p.y, p.z = 0.5, float64(s.surfaceY(0, 0)), 0.5
}

// breakBed 联动清理另一半床。被 breakBlock 在移除主格时调用；
// 另一半静默清除（不掉落），掉落由主格路径负责。Caller holds s.mu。
func (s *Server) breakBed(x, y, z int, state int32) {
	props := blockPropsOf(int(state))
	if props == nil {
		return
	}
	part, facing := props["part"], props["facing"]
	if part == "" {
		part = "foot"
	}
	odx, odz := bedHalfOffset(facing, part)
	ox, oz := x+odx, z+odz
	if other := s.world.getBlock(ox, y, oz); other != stateAir && strings.HasSuffix(blockNameOf(int(other)), "_bed") {
		s.world.setBlock(ox, y, oz, stateAir)
		s.broadcastBlockUpdateLocked(int32(ox), int32(y), int32(oz), stateAir)
	}
}
