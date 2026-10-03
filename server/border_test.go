package server

// M16 world border tests: model defaults, resize validation, lerp
// progress, movement clamping, out-of-border damage and the
// world_border.dat persistence round trip.

import (
	"math"
	"testing"

	"github.com/masgzy/gopherite/protocol/java/v776"
)

func TestWorldBorderDefaults(t *testing.T) {
	b := newWorldBorder()
	if b.size != borderDefaultSize || b.damagePerBlock != borderDefaultDamage ||
		b.safeZone != borderDefaultSafe || b.warningBlocks != borderDefaultWarning ||
		b.warningTime != borderDefaultWarnT || b.absoluteMax != borderAbsoluteMax {
		t.Fatalf("默认值不符: %+v", b)
	}
	// 默认直径 59999997 超过 absoluteMax（29999984），两翼被钳住。
	if b.minX() != -float64(b.absoluteMax) || b.maxX() != float64(b.absoluteMax) {
		t.Fatalf("默认边界框错误: [%f, %f]", b.minX(), b.maxX())
	}
}

func TestWorldBorderSetSizeValidation(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.setBorderSizeLocked(0.5, 0); err == nil {
		t.Fatal("小于 1 的大小应被拒绝")
	}
	if err := s.setBorderSizeLocked(borderMaxSize+1, 0); err == nil {
		t.Fatal("超过上限的大小应被拒绝")
	}
	cur := s.border.size
	if err := s.setBorderSizeLocked(cur, 0); err == nil {
		t.Fatal("相同大小应被拒绝")
	}
	if err := s.setBorderSizeLocked(1000, 0); err != nil {
		t.Fatalf("合法立即缩放失败: %v", err)
	}
	if s.border.size != 1000 || s.border.moving {
		t.Fatalf("立即缩放未生效: size=%f moving=%v", s.border.size, s.border.moving)
	}
}

func TestWorldBorderLerp(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	if err := s.setBorderSizeLocked(1000, 20); err != nil {
		t.Fatalf("lerp 启动失败: %v", err)
	}
	if !s.border.moving || s.border.lerpDuration != 20 {
		t.Fatalf("lerp 状态错误: %+v", s.border)
	}
	for i := 0; i < 19; i++ {
		s.tickBorder()
		if s.border.moving && (s.border.size <= 1000 || s.border.size >= borderDefaultSize) {
			t.Fatalf("插值中点尺寸越界: %f @%d", s.border.size, i)
		}
	}
	s.tickBorder()
	if s.border.moving || s.border.size != 1000 {
		t.Fatalf("lerp 未收敛: size=%f moving=%v", s.border.size, s.border.moving)
	}
	s.mu.Unlock()
}

func TestWorldBorderClampMove(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.setBorderSizeLocked(100, 0); err != nil {
		t.Fatal(err)
	}
	p := &player{id: 1, name: "cl", x: 0.5, z: 0.5}

	// 界内 → 出界目标被钳回边内（max 边留 1e-5 余量）。
	nx, nz := s.clampMoveLocked(p, 200, -3)
	if !s.border.isWithin(nx, nz) || math.Abs(nx-s.border.maxX()) > 1e-4 {
		t.Fatalf("钳制失败: (%f, %f) maxX=%f", nx, nz, s.border.maxX())
	}

	// 界外玩家（边界收缩罩住后）不瞬移，可自由走回。
	p.x, p.z = 200, 0
	nx, nz = s.clampMoveLocked(p, 400, 0)
	if nx != 400 || nz != 0 {
		t.Fatalf("界外玩家坐标不应被改动: (%f, %f)", nx, nz)
	}
	nx, nz = s.clampMoveLocked(p, 40, 0)
	if nx != 40 || nz != 0 {
		t.Fatalf("界外玩家向内走被阻: (%f, %f)", nx, nz)
	}
}

func TestWorldBorderDamage(t *testing.T) {
	s := startTestServer(t)
	s.mu.Lock()
	if err := s.setBorderSizeLocked(100, 0); err != nil {
		t.Fatal(err)
	}
	s.mu.Unlock()

	joinBotToPlay(t, s, "Borderer")
	var p *player
	for _, q := range s.players {
		if q.name == "Borderer" {
			p = q
		}
	}
	if p == nil {
		t.Fatal("bot 未入服")
	}
	// 玩家放在 (200, 0.5)：距 maxX=50 有 150 格。
	s.mu.Lock()
	p.x, p.z = 200, 0.5
	p.gameMode = 0
	s.tickCount = 0
	s.mu.Unlock()

	s.mu.Lock()
	s.tickBorder() // tickCount%10==0 触发伤害判定
	s.mu.Unlock()

	s.mu.Lock()
	dist := s.border.distanceTo(p.x, p.z) + s.border.safeZone
	want := float32(math.Max(1, math.Floor(-dist*s.border.damagePerBlock)))
	if p.health >= maxHealth {
		t.Fatalf("越界未掉血: health=%f dist=%f want=%f", p.health, dist, want)
	}
	got := maxHealth - p.health
	if got < want*0.5 { // 护甲/吸收可能削伤，但裸装应全额
		t.Fatalf("伤害量偏低: got=%f want=%f", got, want)
	}
	// 创造模式免伤。
	p.health = maxHealth
	p.gameMode = 1
	s.tickCount = 0
	s.mu.Unlock()
	s.mu.Lock()
	s.tickBorder()
	s.mu.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.health != maxHealth {
		t.Fatalf("创造模式不应掉血: %f", p.health)
	}
}

func TestWorldBorderDamageTypeRegistered(t *testing.T) {
	// 伤害类型走 outside_border（registry id 33）。
	if v776.DamageTypeOutsideBorder != 33 {
		t.Fatalf("outside_border id 变动: %d", v776.DamageTypeOutsideBorder)
	}
}

func TestWorldBorderPersistRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s := startTestServer(t)

	s.mu.Lock()
	if err := s.setBorderSizeLocked(2048, 0); err != nil {
		t.Fatal(err)
	}
	if err := s.setBorderCenterLocked(64, -128); err != nil {
		t.Fatal(err)
	}
	s.border.damagePerBlock = 1.5
	s.border.safeZone = 7
	s.border.warningBlocks = 9
	s.border.warningTime = 20
	if err := s.saveBorderLocked(dir); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	// 复位到默认后回放。
	s.border = newWorldBorder()
	s.loadBorder(dir)
	b := s.border
	s.mu.Unlock()

	if b.size != 2048 || b.centerX != 64 || b.centerZ != -128 ||
		b.damagePerBlock != 1.5 || b.safeZone != 7 ||
		b.warningBlocks != 9 || b.warningTime != 20 {
		t.Fatalf("回放不一致: %+v", b)
	}
}
