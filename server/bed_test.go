package server

import (
	"testing"

	"github.com/masgzy/gopherite/protocol/java/v776"
)

// placeBedBoth 是测试辅助：完整放置双格床（head + foot）。
func placeBedBoth(s *Server, block string, x, y, z int, facing string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.placeBed(block, x, y, z, facing) {
		return false
	}
	foot := stateIDOf(block, map[string]string{"facing": facing, "part": "foot", "occupied": "false"})
	return foot > 0 && s.world.setBlock(x, y, z, foot)
}

// TestPlaceBedTwoBlocks 验证双格放置：head/foot 的 part 与 facing 属性。
func TestPlaceBedTwoBlocks(t *testing.T) {
	s, _, p := joinedBot(t, "Sleeper")

	x, y, z := int(p.x)+2, int(p.y), int(p.z)
	facing := "east" // head 落在 (x+1, z)
	ok := placeBedBoth(s, "minecraft:white_bed", x, y, z, facing)
	if !ok {
		t.Fatal("双格床放置失败")
	}
	head := s.world.getBlock(x+1, y, z)
	foot := s.world.getBlock(x, y, z)
	hp := blockPropsOf(int(head))
	fp := blockPropsOf(int(foot))
	if hp["part"] != "head" || hp["facing"] != facing {
		t.Fatalf("head 态错误: %v", hp)
	}
	if fp["part"] != "foot" || fp["facing"] != facing {
		t.Fatalf("foot 态错误: %v", fp)
	}
}

// TestBreakBedRemovesBoth 破坏一半时另一半消失。
func TestBreakBedRemovesBoth(t *testing.T) {
	s, _, p := joinedBot(t, "Wrecker")

	x, y, z := int(p.x)+2, int(p.y), int(p.z)
	placeBedBoth(s, "minecraft:white_bed", x, y, z, "east")
	head := s.world.getBlock(x+1, y, z)

	s.mu.Lock()
	// 真实流程：breakBlock 先移除主格，再联动清另一半。
	s.world.setBlock(x+1, y, z, stateAir)
	s.breakBed(x+1, y, z, head)
	s.mu.Unlock()
	if s.world.getBlock(x+1, y, z) != stateAir || s.world.getBlock(x, y, z) != stateAir {
		t.Fatalf("破坏 head 后两格都应为空气: head=%d foot=%d",
			s.world.getBlock(x+1, y, z), s.world.getBlock(x, y, z))
	}
}

// TestSleepSkipsNightAndSetsSpawn 夜晚睡觉：时间跳到清晨并设置重生点。
func TestSleepSkipsNightAndSetsSpawn(t *testing.T) {
	s, _, p := joinedBot(t, "Dreamer")

	s.mu.Lock()
	s.timeTicks = 14000 // 夜里
	x, y, z := int(p.x)+2, int(p.y), int(p.z)
	s.placeBed("minecraft:white_bed", x, y, z, "east")
	s.mu.Unlock()

	if msg := s.trySleepLocked(p, x, y, z); msg == "" {
		t.Fatal("夜晚睡觉应有提示文案")
	}
	s.mu.Lock()
	tod := s.timeOfDayLocked()
	spawned := p.hasSpawn
	s.mu.Unlock()
	if tod != 0 {
		t.Fatalf("睡觉后应为清晨 0，得到 %d", tod)
	}
	if !spawned {
		t.Fatal("睡觉未设置重生点")
	}
}

// TestSleepDeniedByDay 白天拒绝睡觉。
func TestSleepDeniedByDay(t *testing.T) {
	s, _, p := joinedBot(t, "Earlybird")

	s.mu.Lock()
	s.timeTicks = 1000
	x, y, z := int(p.x)+2, int(p.y), int(p.z)
	s.placeBed("minecraft:white_bed", x, y, z, "east")
	s.mu.Unlock()

	if msg := s.trySleepLocked(p, x, y, z); msg == "" {
		t.Fatal("白天拒绝应有提示文案")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.hasSpawn {
		t.Fatal("白天不应设置重生点")
	}
	if tod := s.timeOfDayLocked(); tod != 1000 {
		t.Fatalf("白天交互不应推动时钟: %d", tod)
	}
}

// TestRespawnUsesBedSpawn 验证死亡重生回到床边。
func TestRespawnUsesBedSpawn(t *testing.T) {
	s, _, p := joinedBot(t, "Respawner")

	s.mu.Lock()
	p.spawnX, p.spawnY, p.spawnZ = 3.5, 70, 4.5
	p.hasSpawn = true
	s.mu.Unlock()

	s.damagePlayer(p, 1000, v776.DamageTypeOutOfWorld, -1, -1)
	if !p.dead {
		t.Fatal("致死伤害未触发死亡")
	}
	// respawn 位置选择逻辑（handleClientCommand 内部走同一函数）。
	s.mu.Lock()
	respawnPositionLocked(s, p)
	s.mu.Unlock()
	if p.x != 3.5 || p.z != 4.5 {
		t.Fatalf("重生未回到床边: (%v, %v)", p.x, p.z)
	}
}
