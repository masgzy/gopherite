package server

import (
	"context"
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol/java/v776"
)

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
	t.Logf("join (ViewDistance 8, %d chunks): %v", b.chunks, elapsed)
	if elapsed > 10*time.Second {
		t.Fatalf("join took %v — way past vanilla-feel territory", elapsed)
	}
}
