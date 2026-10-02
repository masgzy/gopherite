package server

// 智能垃圾回收调控器（gcTuner）：闭环反馈系统。用 runtime/metrics 的
// 无锁指标周期性采样 GC 行为，按阈值分档策略动态调节 GOGC——分配高峰
// 时更早、更频繁地回收（每次更小 → STW 更短），空闲时放宽以节省 CPU。
// GOMEMLIMIT 作为内存天花板一次性设定，防止被系统 OOM Killer 击杀；
// 堆水位逼近上限时决策器把 GOGC 直接压到下限档联动兜底。
//
// 三个层次：
//
//      采样  sample()   —— runtime/metrics，每周期一次 Read，无锁
//      决策  nextGOGC() —— 纯函数：阈值分档 + 步进 + 冷却 + 防震荡
//      执行  apply()    —— debug.SetGCPercent / debug.SetMemoryLimit
//
// 调控协程默认 1s 一拍；/gc 命令经 snapshot() 读取状态。

import (
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/masgzy/gopherite/internal/ui"
)

// GC 采样用到的 runtime/metrics 指标名。
const (
	mGCPhases    = "/gc/cycles/total:gc-cycles"
	mAllocBytes  = "/gc/heap/allocs:bytes"
	mHeapGoal    = "/gc/heap/goal:bytes"
	mHeapLive    = "/gc/heap/live:bytes"
	mPauseHist   = "/gc/pauses:seconds"
	mGCCPU       = "/cpu/classes/gc/total:cpu-seconds"
	mTotalCPU    = "/cpu/classes/total:cpu-seconds"
	mHeapObjects = "/memory/classes/heap/objects:bytes"
)

// gcTunerDefaults 是调控器的出厂参数。
const (
	gcDefaultInterval = time.Second      // 采样与决策周期
	gcMinGOGC         = 20               // GOGC 下限档
	gcMaxGOGC         = 300              // GOGC 上限档
	gcDefaultGOGC     = 100              // 启动基线（Go 运行时默认）
	gcTargetPauseMS   = 2.0              // P99 暂停目标（毫秒）
	gcStepDown        = 0.8              // 延迟压力下每拍收缩系数
	gcStepUp          = 1.25             // 宽松期每拍扩张系数
	gcCoolDown        = 5 * time.Second  // 同向调整冷却
	gcReverseCoolDown = 12 * time.Second // 方向反转冷却（防抖动）
	gcCPUHigh         = 0.10             // GC CPU 占比高压线（10%）
	gcCPULow          = 0.02             // GC CPU 占比宽松线（2%）
	gcAllocHigh       = 32 << 20         // 视为分配高峰的字节速率（32 MB/s）
	gcHeapHighFrac    = 0.85             // 堆水位警戒线 → 直接压到下限档
	gcHeapLowFrac     = 0.60             // 堆水位宽松线
)

// gcConfig 是 gcTuner 的静态配置（来自 server.properties）。
type gcConfig struct {
	Enabled     bool
	TargetPause time.Duration // P99 暂停目标
	MemLimit    int64         // 字节；0 = 按环境自动探测
	MinGOGC     int
	MaxGOGC     int
	BaseGOGC    int
	Interval    time.Duration
}

// gcSample 是一轮采样的原始与派生数据。AllocCum 与 CPUCum 是累计值，
// 仅供 diff() 差分；其余为可直接消费的现值/速率。
type gcSample struct {
	GCCount     uint64  // 累计 GC 轮数
	AllocCum    uint64  // 累计分配字节
	AllocRate   float64 // 分配速率，字节/秒（差分后）
	HeapGoal    float64 // 当前堆目标，字节
	HeapLive    float64 // 存活堆，字节
	HeapObjects float64 // 堆上对象，字节
	PauseP99MS  float64 // GC 暂停 P99（直方图桶界），毫秒
	GCCPUFrac   float64 // GC 占用的 CPU 比例 0-1（差分后）
	CPUGCCum    float64 // 累计 GC CPU 秒
	CPUTotCum   float64 // 累计总 CPU 秒
}

// gcDecision 是 nextGOGC 的输出。
type gcDecision struct {
	GOGC   int    // 新的 GOGC 值（可能与当前相同）
	Reason string // 调整原因（中文，日志用）；"hold" 表示未动
}

// gcTuner 实现闭环调控。
type gcTuner struct {
	cfg     gcConfig
	samples []metrics.Sample // 构造时绑定槽位，Read 复用

	mu          sync.Mutex // 保护以下全部状态
	curGOGC     int
	memLimit    int64 // 生效的内存上限（字节；0 = 未设）
	lastDir     int   // 上次调整方向 +1/-1/0
	lastAdjust  time.Time
	adjustments int64
	lastSample  gcSample
	started     time.Time

	// 差分锚点（仅调控协程读写，为统一加锁仍置于 mu 之下）。
	prevAlloc   uint64
	prevCPUGC   float64
	prevCPUTot  float64
	prevGCCount uint64
	prevSet     bool
	lastRead    time.Time
}

// newGCTuner 构造调控器并绑定 runtime/metrics 采样槽位。
// 缺失的指标（运行时改名/精简）按零值处理，不影响决策安全。
func newGCTuner(cfg gcConfig) *gcTuner {
	if cfg.MinGOGC <= 0 {
		cfg.MinGOGC = gcMinGOGC
	}
	if cfg.MaxGOGC < cfg.MinGOGC {
		cfg.MaxGOGC = gcMaxGOGC
	}
	if cfg.BaseGOGC <= 0 {
		cfg.BaseGOGC = gcDefaultGOGC
	}
	if cfg.Interval <= 0 {
		cfg.Interval = gcDefaultInterval
	}
	if cfg.TargetPause <= 0 {
		cfg.TargetPause = time.Duration(gcTargetPauseMS * float64(time.Millisecond))
	}
	want := []string{mGCPhases, mAllocBytes, mHeapGoal, mHeapLive, mPauseHist, mGCCPU, mTotalCPU, mHeapObjects}
	have := make(map[string]bool)
	for _, d := range metrics.All() {
		have[d.Name] = true
	}
	t := &gcTuner{cfg: cfg, curGOGC: cfg.BaseGOGC}
	for _, n := range want {
		if have[n] {
			t.samples = append(t.samples, metrics.Sample{Name: n})
		}
	}
	return t
}

// sample 读取一轮指标（单次 Read）。槽位按名字取值，缺失指标为零值。
func (t *gcTuner) sample() gcSample {
	metrics.Read(t.samples)
	var s gcSample
	for _, sp := range t.samples {
		switch v := sp.Value; sp.Name {
		case mGCPhases:
			s.GCCount = v.Uint64()
		case mAllocBytes:
			s.AllocCum = v.Uint64()
		case mHeapGoal:
			s.HeapGoal = float64(v.Uint64())
		case mHeapLive:
			s.HeapLive = float64(v.Uint64())
		case mPauseHist:
			s.PauseP99MS = pauseP99MS(v.Float64Histogram())
		case mGCCPU:
			s.CPUGCCum = v.Float64()
		case mTotalCPU:
			s.CPUTotCum = v.Float64()
		case mHeapObjects:
			s.HeapObjects = float64(v.Uint64())
		}
	}
	return s
}

// pauseP99MS 从 GC 暂停直方图里取 P99 所在桶的上界（秒 → 毫秒）。
func pauseP99MS(h *metrics.Float64Histogram) float64 {
	if h == nil || len(h.Counts) < 2 {
		return 0
	}
	var total uint64
	for _, c := range h.Counts {
		total += c
	}
	if total == 0 {
		return 0
	}
	target := total * 99 / 100
	var acc uint64
	for i, c := range h.Counts {
		acc += c
		if acc >= target {
			return h.Buckets[i] * 1000
		}
	}
	return h.Buckets[len(h.Buckets)-1] * 1000
}

// diff 把累计值差分成速率。首拍只建立锚点，返回 ok=false。
// 仅调控协程调用。
func (t *gcTuner) diff(raw gcSample, now time.Time) (gcSample, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if !t.prevSet {
		t.prevSet = true
		t.prevAlloc = raw.AllocCum
		t.prevCPUGC = raw.CPUGCCum
		t.prevCPUTot = raw.CPUTotCum
		t.prevGCCount = raw.GCCount
		t.lastRead = now
		return gcSample{}, false
	}
	dt := now.Sub(t.lastRead).Seconds()
	if dt <= 0 {
		dt = t.cfg.Interval.Seconds()
	}
	out := raw
	out.AllocRate = float64(raw.AllocCum-t.prevAlloc) / dt
	if raw.CPUTotCum > t.prevCPUTot {
		out.GCCPUFrac = (raw.CPUGCCum - t.prevCPUGC) / (raw.CPUTotCum - t.prevCPUTot)
	}
	t.prevAlloc = raw.AllocCum
	t.prevCPUGC = raw.CPUGCCum
	t.prevCPUTot = raw.CPUTotCum
	t.prevGCCount = raw.GCCount
	t.lastRead = now
	return out, true
}

// nextGOGC 是决策核心：阈值分档 + 步进 + 冷却 + 防震荡。纯函数。
//
// 优先级（高→低）：
//  1. 堆水位 ≥ gcHeapHighFrac → 直接压到下限档（OOM 联动兜底）
//  2. 冷却期内 → 保持（防震荡）
//  3. GC CPU 占比超高压线，或 P99 暂停超目标且分配速率过热 → 收缩
//  4. GC CPU 占比低于宽松线且堆水位不高 → 扩张
//  5. 其余 → 保持
func nextGOGC(cur int, s gcSample, cfg gcConfig, lastDir int, sinceAdjust time.Duration) gcDecision {
	target := float64(cfg.TargetPause) / float64(time.Millisecond)
	limit := s.HeapGoal
	if limit <= 0 {
		limit = s.HeapLive + 1 // 无 goal 指标时退化为以存活堆估计
	}
	heapFrac := s.HeapObjects / limit

	// 1) 内存水位优先：逼近上限，直接压到下限档。
	if heapFrac >= gcHeapHighFrac {
		if cfg.MinGOGC == cur {
			return gcDecision{cur, "hold"}
		}
		return gcDecision{cfg.MinGOGC, fmt.Sprintf("堆水位 %.0f%% 触顶，压至下限档 %d", heapFrac*100, cfg.MinGOGC)}
	}

	// 2) 冷却：同向短冷、反向长冷。
	if sinceAdjust < gcCoolDown {
		return gcDecision{cur, "hold"}
	}

	// 3) 延迟压力 → 收缩。
	hot := s.GCCPUFrac > gcCPUHigh ||
		(s.PauseP99MS > target && s.AllocRate > gcAllocHigh)
	if hot {
		if lastDir == 1 && sinceAdjust < gcReverseCoolDown {
			return gcDecision{cur, "hold"} // 刚扩张过，反转需更长冷却
		}
		v := int(float64(cur) * gcStepDown)
		if v < cfg.MinGOGC {
			v = cfg.MinGOGC
		}
		if v == cur {
			return gcDecision{cur, "hold"}
		}
		why := fmt.Sprintf("GC CPU %.0f%% 超压线", s.GCCPUFrac*100)
		if s.GCCPUFrac <= gcCPUHigh {
			why = fmt.Sprintf("P99 暂停 %.2fms 超目标 %.1fms（分配 %.1fMB/s）",
				s.PauseP99MS, target, s.AllocRate/(1<<20))
		}
		return gcDecision{v, why}
	}

	// 4) 宽松 → 扩张。
	if s.GCCPUFrac < gcCPULow && heapFrac < gcHeapLowFrac {
		if lastDir == -1 && sinceAdjust < gcReverseCoolDown {
			return gcDecision{cur, "hold"}
		}
		v := int(float64(cur) * gcStepUp)
		if v > cfg.MaxGOGC {
			v = cfg.MaxGOGC
		}
		if v == cur {
			return gcDecision{cur, "hold"}
		}
		return gcDecision{v, fmt.Sprintf("GC CPU %.0f%% 宽松，放宽至 %d", s.GCCPUFrac*100, v)}
	}

	return gcDecision{cur, "hold"}
}

// apply 将决策落到运行时。返回是否实际调整过。
func (t *gcTuner) apply(d gcDecision) bool {
	t.mu.Lock()
	old := t.curGOGC
	if d.GOGC == old {
		t.mu.Unlock()
		return false
	}
	t.curGOGC = d.GOGC
	t.lastDir = dirOf(old, d.GOGC)
	t.lastAdjust = time.Now()
	t.adjustments++
	t.mu.Unlock()
	_ = debug.SetGCPercent(d.GOGC)
	return true
}

func dirOf(old, new int) int {
	switch {
	case new < old:
		return -1
	case new > old:
		return 1
	}
	return 0
}

// setMemoryLimit 应用静态内存天花板；limit<=0 时按环境探测
// （cgroup v2 → v1 → /proc/meminfo，取最小可用值 × 90%）。
func (t *gcTuner) setMemoryLimit(limit int64) {
	if limit <= 0 {
		limit = detectMemLimitBytes(os.ReadFile)
	}
	if limit <= 0 {
		return
	}
	debug.SetMemoryLimit(limit)
	t.mu.Lock()
	t.memLimit = limit
	t.mu.Unlock()
}

// detectMemLimitBytes 读取运行环境的内存上限（字节），无法确定时返回 0。
// readFile 可注入以便测试。
func detectMemLimitBytes(readFile func(string) ([]byte, error)) int64 {
	best := int64(0)
	consider := func(v int64) {
		if v > 0 && (best == 0 || v < best) {
			best = v
		}
	}
	// cgroup v2：单一文件，"max" 表示未限制。
	if b, err := readFile("/sys/fs/cgroup/memory.max"); err == nil {
		consider(parseMemSize(string(b)))
	}
	// cgroup v1：层级文件。
	if b, err := readFile("/sys/fs/cgroup/memory/memory.limit_in_bytes"); err == nil {
		consider(parseMemSize(string(b)))
	}
	// 物理内存兜底：MemTotal:  16384000 kB
	if b, err := readFile("/proc/meminfo"); err == nil {
		if v, ok := parseMemTotal(string(b)); ok {
			consider(v)
		}
	}
	if best <= 0 {
		return 0
	}
	return best * 9 / 10
}

// parseMemSize 解析 "max"、纯字节或 "8388608 kB" 形式的内存值。
func parseMemSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "max" {
		return 0
	}
	fields := strings.Fields(s)
	v, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || v <= 0 {
		return 0
	}
	if len(fields) > 1 && fields[1] == "kB" {
		v *= 1024
	}
	return v
}

// parseMemTotal 从 /proc/meminfo 提取 MemTotal 字节数。
func parseMemTotal(s string) (int64, bool) {
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			return parseMemSize(strings.TrimPrefix(line, "MemTotal:")), true
		}
	}
	return 0, false
}

// gcSnapshot 是 /gc 命令与日志的可读快照。
type gcSnapshot struct {
	Enabled     bool
	GOGC        int
	MemLimitMiB int64
	HeapMB      float64
	AllocMBPS   float64
	GCCPUPct    float64
	PauseP99MS  float64
	Adjusts     int64
}

func (t *gcTuner) snapshot() gcSnapshot {
	t.mu.Lock()
	defer t.mu.Unlock()
	return gcSnapshot{
		Enabled:     t.cfg.Enabled,
		GOGC:        t.curGOGC,
		MemLimitMiB: t.memLimit >> 20,
		HeapMB:      t.lastSample.HeapObjects / (1 << 20),
		AllocMBPS:   t.lastSample.AllocRate / (1 << 20),
		GCCPUPct:    t.lastSample.GCCPUFrac * 100,
		PauseP99MS:  t.lastSample.PauseP99MS,
		Adjusts:     t.adjustments,
	}
}

// start 启动调控循环直到 stop 关闭。仅 production Serve 调用。
func (t *gcTuner) start(stop <-chan struct{}, wg *sync.WaitGroup) {
	// 启动即设定基线与内存天花板。
	if t.cfg.BaseGOGC != gcDefaultGOGC {
		_ = debug.SetGCPercent(t.cfg.BaseGOGC)
	}
	t.setMemoryLimit(t.cfg.MemLimit)
	logGCConfig(t)

	wg.Add(1)
	go func() {
		defer wg.Done()
		tk := time.NewTicker(t.cfg.Interval)
		defer tk.Stop()
		for {
			select {
			case <-tk.C:
				t.tick()
			case <-stop:
				return
			}
		}
	}()
}

// tick 执行一轮 采样 → 差分 → 决策 → 执行。
func (t *gcTuner) tick() {
	raw := t.sample()
	s, ok := t.diff(raw, time.Now())
	if !ok {
		return
	}
	t.mu.Lock()
	t.lastSample = s
	cur, lastDir, since := t.curGOGC, t.lastDir, time.Since(t.lastAdjust)
	t.mu.Unlock()

	d := nextGOGC(cur, s, t.cfg, lastDir, since)
	if d.Reason == "hold" {
		return
	}
	if t.apply(d) {
		log.Printf(ui.Info("✦ ")+"GC 调整 GOGC %d → %d（%s）", cur, d.GOGC, d.Reason)
	}
}

// logGCConfig 打印启用横幅。
func logGCConfig(t *gcTuner) {
	lim := "未设置"
	if m := t.snapshotMemLimit(); m > 0 {
		lim = fmt.Sprintf("%d MiB", m>>20)
	}
	log.Printf(ui.Info("✦ ")+"智能 GC 调控已启用：基线 GOGC=%d（档位 %d-%d），内存上限 %s，P99 暂停目标 %.1fms，采样周期 %s",
		t.cfg.BaseGOGC, t.cfg.MinGOGC, t.cfg.MaxGOGC, lim,
		float64(t.cfg.TargetPause)/float64(time.Millisecond), t.cfg.Interval)
}

func (t *gcTuner) snapshotMemLimit() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.memLimit
}
