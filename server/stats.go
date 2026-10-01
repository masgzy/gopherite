package server

import (
	"runtime"
	"sync"
	"time"
)

// tickStats is the rolling tick-timing recorder behind /tpsbar and /tps.
// Durations keep the last 100 tick work times (MSPT window); ends keeps
// the last 15 minutes of tick completion timestamps (TPS windows).
type tickStats struct {
	mu sync.Mutex

	durations [100]time.Duration
	didx      int

	ends       [18000]int64 // unix nanos, 15 min at 20 TPS
	eidx       int
	ecount     int64
	firstStart time.Time // server start, guards the fresh-uptime TPS read
}

// record archives one completed tick.
func (st *tickStats) record(work time.Duration, end time.Time) {
	st.mu.Lock()
	st.durations[st.didx] = work
	st.didx = (st.didx + 1) % len(st.durations)
	if st.firstStart.IsZero() {
		st.firstStart = end
	}
	st.ends[st.eidx] = end.UnixNano()
	st.eidx = (st.eidx + 1) % len(st.ends)
	st.ecount++
	st.mu.Unlock()
}

// snapshot returns the average MSPT over the last 100 ticks and the TPS
// for the 1m/5m/15m windows, using the current wall clock.
func (st *tickStats) snapshot() (mspt float64, tps1m, tps5m, tps15m float64) {
	return st.snapshotAt(time.Now())
}

// snapshotAt is the injectable-clock variant used by tests.
func (st *tickStats) snapshotAt(now time.Time) (mspt float64, tps1m, tps5m, tps15m float64) {
	nowNano := now.UnixNano()

	st.mu.Lock()
	// MSPT over the filled part of the duration window.
	n := len(st.durations)
	if st.ecount < int64(n) {
		n = int(st.ecount)
	}
	if n == 0 {
		st.mu.Unlock()
		return 0, 20, 20, 20
	}
	var sum time.Duration
	for i := 0; i < n; i++ {
		sum += st.durations[i]
	}
	st.mu.Unlock()
	mspt = float64(sum.Microseconds()) / float64(n) / 1000.0

	tps1m = st.windowTPS(nowNano, int64(time.Minute))
	tps5m = st.windowTPS(nowNano, 5*int64(time.Minute))
	tps15m = st.windowTPS(nowNano, 15*int64(time.Minute))
	return mspt, tps1m, tps5m, tps15m
}

// windowTPS counts the recorded tick ends inside the trailing window and
// divides by the actually elapsed window time (bounded by uptime).
func (st *tickStats) windowTPS(nowNano, window int64) float64 {
	st.mu.Lock()
	defer st.mu.Unlock()

	start := nowNano - window
	// Cap at uptime: a 30 s old server has no more than 30 s of data.
	if !st.firstStart.IsZero() {
		if uptime := nowNano - st.firstStart.UnixNano(); uptime < window {
			start = nowNano - uptime
		}
	}
	var count int
	for i := 0; i < len(st.ends); i++ {
		if t := st.ends[i]; t >= start && t <= nowNano {
			count++
		}
	}
	elapsed := float64(nowNano-start) / float64(int64(time.Second))
	if elapsed <= 0 || count == 0 {
		return 20
	}
	tps := float64(count) / elapsed
	if tps > 20 {
		tps = 20
	}
	return tps
}

// runtimeSnapshot is the process-level view shown on the tpsbar.
type runtimeSnapshot struct {
	Goroutines int
	NumGC      uint32
	HeapMB     float64
	SysMB      float64
}

// snapshotRuntime collects goroutine count, cumulative GC count and the
// heap/total memory view. ReadMemStats briefly stops the world; once per
// second per server that cost is negligible.
func snapshotRuntime() runtimeSnapshot {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return runtimeSnapshot{
		Goroutines: runtime.NumGoroutine(),
		NumGC:      m.NumGC,
		HeapMB:     float64(m.HeapAlloc) / (1 << 20),
		SysMB:      float64(m.Sys) / (1 << 20),
	}
}
