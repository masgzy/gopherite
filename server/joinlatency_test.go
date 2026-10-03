package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol/java/v776"
)

// joinLatencyBudget is the wall-clock budget for the initial chunk batch.
// GitHub Actions runners are shared 2-core machines and -race multiplies
// the cost of every hot path, so the strict local budget (10s) trips there
// even though the number is healthy; CI therefore gets a generous ceiling
// that still catches order-of-magnitude regressions.
func joinLatencyBudget() time.Duration {
	if os.Getenv("GITHUB_ACTIONS") == "true" {
		return 90 * time.Second
	}
	return 10 * time.Second
}

// TestJoinLatency measures the wall-clock time from TCP connect to the
// final chunk-batch-done packet at the production view distance. The
// client's loading screen lasts exactly this long, so this keeps the
// "slow join" complaint measurable. Run with -v to see the number.
func TestJoinLatency(t *testing.T) {
	s, err := New(Options{
		ListenAddr:        "127.0.0.1:0",
		MOTD:              "latency",
		MaxPlayers:        5,
		VersionName:       v776.Name,
		ProtocolNumber:    v776.ProtocolNumber,
		ViewDistance:      8,
		LevelName:         "latencyworld", // production-like: world + herd
		KeepAliveInterval: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Listen(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = s.Serve() }()
	<-s.started
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	start := time.Now()
	b := joinBotToPlayVD(t, s, "Timer", 8)
	elapsed := time.Since(start)
	budget := joinLatencyBudget()
	t.Logf("join (ViewDistance 8, %d chunks): %v (budget %v)", b.chunks, elapsed, budget)
	if elapsed > budget {
		t.Fatalf("join took %v — way past vanilla-feel territory (budget %v)", elapsed, budget)
	}
}
