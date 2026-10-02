package server

// 智能GC调控器测试：决策方向、限幅、冷却与防震荡、直方图 P99、
// 内存上限探测解析、差分与快照冒烟。

import (
	"errors"
	"runtime/metrics"
	"strings"
	"sync"
	"testing"
	"time"
)

func testGCConfig() gcConfig {
	return gcConfig{
		Enabled:     true,
		TargetPause: 2 * time.Millisecond,
		MinGOGC:     20,
		MaxGOGC:     300,
		BaseGOGC:    100,
		Interval:    time.Second,
	}
}

// gcTestSample 构造决策输入的便利函数。
func gcTestSample(gcCPU, pauseP99, allocMBPS, heapFrac float64) gcSample {
	return gcSample{
		GCCPUFrac:   gcCPU,
		PauseP99MS:  pauseP99,
		AllocRate:   allocMBPS * (1 << 20),
		HeapObjects: heapFrac * 1024,
		HeapGoal:    1024,
	}
}

func TestNextGOGCPressureShrinks(t *testing.T) {
	cfg := testGCConfig()
	// GC CPU 20% 超高压线 → 收缩。
	d := nextGOGC(100, gcTestSample(0.20, 0.5, 100, 0.3), cfg, 0, time.Minute)
	if d.GOGC != 80 {
		t.Fatalf("期望收缩至 80，得到 %d（%s）", d.GOGC, d.Reason)
	}
	if d.Reason == "hold" {
		t.Fatal("压力场景不允许 hold")
	}
}

func TestNextGOGCShrinkFloor(t *testing.T) {
	cfg := testGCConfig()
	// 已经在下限档：保持，不再触发原因。
	d := nextGOGC(20, gcTestSample(0.50, 9, 500, 0.3), cfg, -1, time.Minute)
	if d.GOGC != 20 || d.Reason != "hold" {
		t.Fatalf("下限档应保持，得到 %d（%s）", d.GOGC, d.Reason)
	}
}

func TestNextGOGCPauseTargetWithHotAlloc(t *testing.T) {
	cfg := testGCConfig()
	// GC CPU 未超线，但 P99 3ms 超目标且分配 64MB/s 过热 → 收缩。
	d := nextGOGC(100, gcTestSample(0.05, 3.0, 64, 0.3), cfg, 0, time.Minute)
	if d.GOGC != 80 {
		t.Fatalf("P99+分配过热应收缩，得到 %d（%s）", d.GOGC, d.Reason)
	}
	if !strings.Contains(d.Reason, "P99") {
		t.Fatalf("原因应提及 P99: %s", d.Reason)
	}
}

func TestNextGOGCPauseHighButAllocCold(t *testing.T) {
	cfg := testGCConfig()
	// P99 超目标但分配速率低（GC 并非瓶颈）→ 不收缩。
	d := nextGOGC(100, gcTestSample(0.05, 5.0, 1, 0.3), cfg, 0, time.Minute)
	if d.Reason != "hold" {
		t.Fatalf("低分配时暂停超标不应收缩，得到 %d（%s）", d.GOGC, d.Reason)
	}
}

func TestNextGOGCIdleGrows(t *testing.T) {
	cfg := testGCConfig()
	d := nextGOGC(100, gcTestSample(0.005, 0.2, 0, 0.3), cfg, 0, time.Minute)
	if d.GOGC != 125 {
		t.Fatalf("空闲应扩张至 125，得到 %d（%s）", d.GOGC, d.Reason)
	}
}

func TestNextGOGCGrowCeiling(t *testing.T) {
	cfg := testGCConfig()
	d := nextGOGC(300, gcTestSample(0.001, 0.1, 0, 0.3), cfg, 0, time.Minute)
	if d.GOGC != 300 || d.Reason != "hold" {
		t.Fatalf("上限档应保持，得到 %d（%s）", d.GOGC, d.Reason)
	}
}

func TestNextGOGCMiddleHolds(t *testing.T) {
	cfg := testGCConfig()
	// GC CPU 5%、P99 达标 → 既不收缩也不扩张。
	d := nextGOGC(100, gcTestSample(0.05, 0.5, 10, 0.3), cfg, 0, time.Minute)
	if d.Reason != "hold" {
		t.Fatalf("中间地带应保持，得到 %d（%s）", d.GOGC, d.Reason)
	}
}

func TestNextGOGCCoolDownHolds(t *testing.T) {
	cfg := testGCConfig()
	// 冷却期内（<5s）即使压力大也保持。
	d := nextGOGC(100, gcTestSample(0.30, 8, 500, 0.3), cfg, 0, 2*time.Second)
	if d.Reason != "hold" {
		t.Fatalf("冷却期应保持，得到 %d（%s）", d.GOGC, d.Reason)
	}
}

func TestNextGOGCReverseCooldown(t *testing.T) {
	cfg := testGCConfig()
	// 刚扩张（lastDir=+1）5~12s 内遇压力 → 反转被抑制。
	d := nextGOGC(125, gcTestSample(0.30, 8, 500, 0.3), cfg, 1, 8*time.Second)
	if d.Reason != "hold" {
		t.Fatalf("反转冷却应保持，得到 %d（%s）", d.GOGC, d.Reason)
	}
	// 超过 12s 反转放行。
	d = nextGOGC(125, gcTestSample(0.30, 8, 500, 0.3), cfg, 1, 13*time.Second)
	if d.GOGC != 100 {
		t.Fatalf("反转冷却后应收缩至 100，得到 %d（%s）", d.GOGC, d.Reason)
	}
}

func TestNextGOGCHeapHighOverridesCooldown(t *testing.T) {
	cfg := testGCConfig()
	// 堆水位触顶：无视冷却直接压到下限档。
	d := nextGOGC(100, gcTestSample(0.05, 0.5, 1, 0.90), cfg, 0, 0)
	if d.GOGC != 20 {
		t.Fatalf("堆水位触顶应压至下限档 20，得到 %d（%s）", d.GOGC, d.Reason)
	}
	// 已经在下限档且触顶：保持。
	d = nextGOGC(20, gcTestSample(0.05, 0.5, 1, 0.95), cfg, -1, 0)
	if d.GOGC != 20 || d.Reason != "hold" {
		t.Fatalf("下限档触顶应保持，得到 %d（%s）", d.GOGC, d.Reason)
	}
}

func TestNextGOGCHeapFracWithoutGoal(t *testing.T) {
	cfg := testGCConfig()
	// 无 goal 指标：以存活堆 + 1 估计水位。
	s := gcTestSample(0.05, 0.5, 1, 0.90)
	s.HeapGoal = 0
	d := nextGOGC(100, s, cfg, 0, time.Minute)
	if d.GOGC != 20 {
		t.Fatalf("无 goal 时水位触顶应压至下限档，得到 %d（%s）", d.GOGC, d.Reason)
	}
}

func TestPauseP99MS(t *testing.T) {
	// 桶界 [0,0.001,0.002,...]：100 个暂停里 99 个 ≤1ms、1 个 2ms → P99=1ms。
	hist := float64Hist([]float64{0, 0.001, 0.002}, []uint64{50, 49, 1})
	if got := pauseP99MS(hist); got != 1.0 {
		t.Fatalf("P99 期望 1.0ms，得到 %v", got)
	}
	// 全零直方图 → 0。
	zero := float64Hist([]float64{0, 1}, []uint64{0, 0})
	if got := pauseP99MS(zero); got != 0 {
		t.Fatalf("空直方图 P99 应为 0，得到 %v", got)
	}
}

func TestDetectMemLimitCgroupV2(t *testing.T) {
	files := map[string]string{
		"/sys/fs/cgroup/memory.max": "4194304000\n", // 4000 MiB
		"/proc/meminfo":             "MemTotal:  16000000 kB\n",
	}
	got := detectMemLimitBytes(fakeReadFile(files))
	// min(4000MiB, 15625MiB) × 0.9 = 3600MiB。
	if got != 3600<<20 {
		t.Fatalf("期望 3600MiB，得到 %d MiB", got>>20)
	}
}

func TestDetectMemLimitCgroupMax(t *testing.T) {
	files := map[string]string{
		"/sys/fs/cgroup/memory.max": "max\n",
		"/proc/meminfo":             "MemTotal:  16000000 kB\n",
	}
	got := detectMemLimitBytes(fakeReadFile(files))
	// cgroup 无限 → 取系统内存 16000000kB × 90%。
	if got != 16000000*1024*9/10 {
		t.Fatalf("cgroup 无限时取系统内存 90%%，得到 %d MiB", got>>20)
	}
}

func TestDetectMemLimitNone(t *testing.T) {
	if got := detectMemLimitBytes(func(string) ([]byte, error) { return nil, errors.New("no") }); got != 0 {
		t.Fatalf("全不可用应返回 0，得到 %d", got)
	}
}

func TestParseMemSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"max", 0},
		{"", 0},
		{"1073741824", 1 << 30},
		{"1048576 kB", 1 << 30},
		{"  8192 kB \n", 8 << 20},
		{"garbage", 0},
		{"-5", 0},
	}
	for _, c := range cases {
		if got := parseMemSize(c.in); got != c.want {
			t.Errorf("parseMemSize(%q)=%d，期望 %d", c.in, got, c.want)
		}
	}
}

func TestParseMemTotal(t *testing.T) {
	v, ok := parseMemTotal("MemFree: 1 kB\nMemTotal:  2000000 kB\nSwapTotal: 0 kB\n")
	if !ok || v != 2000000*1024 {
		t.Fatalf("MemTotal 解析失败: %d %v", v, ok)
	}
	if _, ok := parseMemTotal("MemFree: 1 kB\n"); ok {
		t.Fatal("缺 MemTotal 应返回 false")
	}
}

func TestGCTunerDiffRates(t *testing.T) {
	tr := newGCTuner(testGCConfig())
	base := gcSample{GCCount: 3, AllocCum: 1000, CPUGCCum: 1.0, CPUTotCum: 10.0, HeapObjects: 1 << 20, HeapGoal: 100 << 20}
	now := time.Now()
	// 首拍建立锚点。
	if _, ok := tr.diff(base, now); ok {
		t.Fatal("首拍应返回 ok=false")
	}
	// 1 秒后：分配 +32MiB，GC CPU +0.2s / 总 +2s → 10%。
	next := base
	next.GCCount = 6
	next.AllocCum = 1000 + 32<<20
	next.CPUGCCum = 1.2
	next.CPUTotCum = 12.0
	s, ok := tr.diff(next, now.Add(time.Second))
	if !ok {
		t.Fatal("第二拍应返回 ok=true")
	}
	if s.AllocRate != 32<<20 {
		t.Fatalf("分配速率期望 32MiB/s，得到 %f", s.AllocRate/(1<<20))
	}
	if s.GCCPUFrac < 0.099 || s.GCCPUFrac > 0.101 {
		t.Fatalf("GC CPU 占比期望 0.10，得到 %f", s.GCCPUFrac)
	}
	if s.GCCount-base.GCCount != 0 {
		// diff 不负责 GC 次数差分（仅锚点更新），确认锚点推进即可。
		if tr.prevGCCount != 6 {
			t.Fatal("GC 轮数锚点未推进")
		}
	}
}

func TestGCTunerApplyAndSnapshot(t *testing.T) {
	tr := newGCTuner(testGCConfig())
	if tr.apply(gcDecision{100, "hold"}) {
		t.Fatal("相同值不应触发调整")
	}
	if !tr.apply(gcDecision{60, "test"}) {
		t.Fatal("调整应生效")
	}
	if got := tr.snapshot().GOGC; got != 60 {
		t.Fatalf("curGOGC 期望 60，得到 %d", got)
	}
	tr.mu.Lock()
	if tr.lastDir != -1 || tr.adjustments != 1 {
		tr.mu.Unlock()
		t.Fatalf("方向/计数错误: dir=%d adjusts=%d", tr.lastDir, tr.adjustments)
	}
	tr.mu.Unlock()
	// 方向记录：上调。
	tr.apply(gcDecision{75, "test"})
	tr.mu.Lock()
	dir := tr.lastDir
	tr.mu.Unlock()
	if dir != 1 {
		t.Fatalf("上调方向期望 +1，得到 %d", dir)
	}
}

func TestGCTunerTickSmoke(t *testing.T) {
	// 真实 runtime/metrics 冒烟：两拍不 panic，快照可用。
	cfg := testGCConfig()
	cfg.Interval = time.Millisecond
	tr := newGCTuner(cfg)
	tr.tick()
	tr.tick()
	tr.setMemoryLimit(64 << 20)
	s := tr.snapshot()
	// 真实 runtime 数据可能触发合法调整，GOGC 未必停在基线；
	// 断言它落在配置档位内且内存上限生效即可。
	if !s.Enabled || s.GOGC < cfg.MinGOGC || s.GOGC > cfg.MaxGOGC || s.MemLimitMiB != 64 {
		t.Fatalf("快照异常: %+v", s)
	}
}

func TestDirOf(t *testing.T) {
	if dirOf(100, 80) != -1 || dirOf(100, 125) != 1 || dirOf(100, 100) != 0 {
		t.Fatal("dirOf 方向错误")
	}
}

// 并发冒烟：调控循环与 snapshot 并发不产生数据竞争（配合 -race）。
func TestGCTunerConcurrentSnapshot(t *testing.T) {
	cfg := testGCConfig()
	cfg.Interval = time.Millisecond
	tr := newGCTuner(cfg)
	stop := make(chan struct{})
	var wg sync.WaitGroup
	tr.start(stop, &wg)
	for i := 0; i < 50; i++ {
		_ = tr.snapshot()
	}
	close(stop)
	wg.Wait()
}

// ---- 测试辅助 ----

// float64Hist 构造 metrics.Float64Histogram 样品。
func float64Hist(buckets []float64, counts []uint64) *metrics.Float64Histogram {
	return &metrics.Float64Histogram{Buckets: buckets, Counts: counts}
}

// fakeReadFile 把路径映射到固定内容的 os.ReadFile 替身。
func fakeReadFile(files map[string]string) func(string) ([]byte, error) {
	return func(p string) ([]byte, error) {
		if v, ok := files[p]; ok {
			return []byte(v), nil
		}
		return nil, errors.New("not found")
	}
}
