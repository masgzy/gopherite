package server

import (
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M9 day/night. The 26.2 time packet carries named world clocks; the
// overworld clock's totalTicks drives the client sun/moon. The server
// advances the clock one tick per tick and re-syncs every few seconds so
// the client-side rate-1.0 free run never drifts far. Hostile spawning
// and the daylight burn both read the same clock.

// Vanilla overworld constants.
const (
	ticksPerDay      = 24000
	nightStart       = 13000 // hostile spawn window opens
	nightEnd         = 23000 // sun comes back up, undead start burning
	timeResyncPeriod = 100   // ticks between clock re-syncs (5 s)
)

// timeOfDayLocked returns the overworld time of day (0..23999).
// Caller holds s.mu.
func (s *Server) timeOfDayLocked() int64 {
	t := s.timeTicks % ticksPerDay
	if t < 0 {
		t += ticksPerDay
	}
	return t
}

// isNightLocked reports whether hostiles may spawn and undead are safe
// from the sun. Caller holds s.mu.
func (s *Server) isNightLocked() bool {
	t := s.timeOfDayLocked()
	return t >= nightStart && t < nightEnd
}

// isDayLocked is the burn window: undead outside the night band catch
// fire (vanilla burns them from sunrise until dusk).
func (s *Server) isDayLocked() bool { return !s.isNightLocked() }

// broadcastTimeLocked pushes a full clock sync to every player.
// Caller holds s.mu.
func (s *Server) broadcastTimeLocked() {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetTime)
	java.WritePlaySetTime(body, s.timeTicks, []java.ClockState{{
		ClockID:     s.world.clockID("overworld"),
		TotalTicks:  s.timeTicks,
		PartialTick: 0,
		Rate:        1.0,
	}})
	for _, p := range s.players {
		_ = p.conn.sendPacket(body.Bytes())
	}
}

// tickTime advances the overworld clock and periodically re-syncs
// clients. Runs on the ticker goroutine as part of tickOnce.
func (s *Server) tickTime() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.timeTicks++
	if s.tickCount%timeResyncPeriod == 0 {
		s.broadcastTimeLocked()
	}
}
