package server

import (
	"testing"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// drainBot 持续消费服务端帧（不解码），防止 TCP 写缓冲写满后
// 持锁广播阻塞 ticker。测试结束、连接关闭后自动退出。
func drainBot(b *botConn) {
	go func() {
		for {
			if _, err := b.fr.Next(); err != nil {
				return
			}
		}
	}()
}

// TestIgniteTNTSpawnsEntity 验证点燃：方块被清除、生成引信实体、
// 默认引信 80 ticks。
func TestIgniteTNTSpawnsEntity(t *testing.T) {
	s, _, p := joinedBot(t, "Firer")

	x, y, z := int(p.x)+2, int(p.y), int(p.z)
	tntState := defaultStateOf("minecraft:tnt")
	if tntState < 0 {
		t.Fatal("生成表缺 minecraft:tnt")
	}
	s.world.setBlock(x, y, z, int32(tntState))

	s.mu.Lock()
	s.igniteTNTLocked(x, y, z, p.id, 0)
	s.mu.Unlock()

	if got := s.world.getBlock(x, y, z); got != stateAir {
		t.Fatalf("点燃后方块应清除，得到 %d", got)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var found *tntEntity
	for _, e := range s.entities {
		if t, ok := e.(*tntEntity); ok {
			found = t
		}
	}
	if found == nil {
		t.Fatal("未生成 TNT 实体")
	}
	if found.fuseLeft() != tntFuseTicks {
		t.Fatalf("默认引信应 80，得到 %d", found.fuseLeft())
	}
}

// TestTNTFuseExplodes 快进引信到爆炸：弹坑出现且邻近玩家受伤。
func TestTNTFuseExplodes(t *testing.T) {
	s, b0, p := joinedBot(t, "Gone")
	drainBot(b0)

	// TNT 就放在玩家脚边 1 格，爆炸必然波及。
	x, y, z := int(p.x)+1, int(p.y), int(p.z)
	s.world.setBlock(x, y, z, int32(defaultStateOf("minecraft:tnt")))
	healthBefore := p.health

	s.mu.Lock()
	s.igniteTNTLocked(x, y, z, p.id, 1) // 1 tick 引信加速测试
	s.mu.Unlock()

	s.mu.Lock()
	var ent *tntEntity
	for _, e := range s.entities {
		if t, ok := e.(*tntEntity); ok {
			ent = t
		}
	}
	s.mu.Unlock()
	if ent == nil {
		t.Fatal("TNT 实体缺失")
	}
	ent.tick(s)           // fuse 1→0：pendingExplode 置位
	s.resolveTNTIntents() // 内部自取 Server.mu

	if got := s.world.getBlock(x, y, z); got != stateAir {
		t.Fatalf("爆点应成弹坑，得到 %d", got)
	}
	if p.health >= healthBefore {
		t.Fatalf("爆炸未伤害玩家: %v -> %v", healthBefore, p.health)
	}
	if ent.alive() {
		t.Fatal("爆炸后实体应标记死亡")
	}
	s.tickEntities() // 尾部清理循环回收死实体
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.entities[ent.id]; ok {
		t.Fatal("tickEntities 后死实体应被移除")
	}
}

// TestTNTChainIgnition 弹坑中的 TNT 方块应转化为新的点燃实体。
func TestTNTChainIgnition(t *testing.T) {
	s, b0, p := joinedBot(t, "Chainer")
	drainBot(b0)

	x, y, z := int(p.x)+1, int(p.y), int(p.z)
	s.world.setBlock(x, y, z, int32(defaultStateOf("minecraft:tnt")))
	// 邻格放第二个 TNT 方块：在半径 4 的弹坑内。
	s.world.setBlock(x+1, y, z, int32(defaultStateOf("minecraft:tnt")))

	s.mu.Lock()
	s.igniteTNTLocked(x, y, z, p.id, 1)
	s.mu.Unlock()

	s.mu.Lock()
	var ent *tntEntity
	for _, e := range s.entities {
		if t, ok := e.(*tntEntity); ok {
			ent = t
		}
	}
	s.mu.Unlock()
	ent.tick(s)
	s.resolveTNTIntents() // 内部自取 Server.mu

	// 邻格 TNT 被炸到后应变成点燃实体而非直接消失。
	s.mu.Lock()
	defer s.mu.Unlock()
	chained := 0
	for _, e := range s.entities {
		if tn, ok := e.(*tntEntity); ok && tn.id != ent.id {
			chained++
			if tn.fuseLeft() < tntChainMin {
				t.Fatalf("连锁引信 %d 低于下限 %d", tn.fuseLeft(), tntChainMin)
			}
		}
	}
	if chained == 0 {
		t.Fatal("弹坑中的 TNT 方块未被连锁点燃")
	}
}

// TestCreeperUsesSharedExplosion 确认爬行者爆炸经通用路径仍然生效
// （回归保护：重构后 hostile 测试之外的快捷断言）。
func TestCreeperUsesSharedExplosion(t *testing.T) {
	s, _, p := joinedBot(t, "Boom")
	healthBefore := p.health
	s.mu.Lock()
	s.explodeAtLocked(p.x+2, p.y, p.z, creeperRadius, -1, -1)
	s.mu.Unlock()
	if p.health >= healthBefore {
		t.Fatalf("通用爆炸路径未伤害玩家: %v -> %v", healthBefore, p.health)
	}
}

// TestTNTSpawnEncode 验证 primed_tnt 生成帧携带引信 data。
func TestTNTSpawnEncode(t *testing.T) {
	s, _, _ := joinedBot(t, "Wire")
	e := newTNTEntity(77, -1, 1.5, 64, 2.5, 42)
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayAddEntity)
	s.encodeSpawn(w, e)
	r := protocol.NewReader(w.Bytes())
	if id, _ := r.VarInt(); id != v776.PacketPlayAddEntity {
		t.Fatal("帧 id 错误")
	}
}
