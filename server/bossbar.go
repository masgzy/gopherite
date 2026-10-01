package server

import (
	"crypto/rand"
	"fmt"
	"strconv"
	"strings"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// The /tpsbar boss bar (a Leaves-server hallmark) rendered on top of the
// screen: line 1 carries TPS and MSPT, line 2 the Go runtime view
// (goroutines, cumulative GC count, heap memory).

const tpsbarWidth = 10

// tpsColor maps a TPS reading to a boss bar and text color bucket:
// healthy green, degraded yellow, struggling red.
func tpsColor(tps float64) (bossColor int32, code string) {
	switch {
	case tps >= 19:
		return java.BossColorGreen, "§a"
	case tps >= 15:
		return java.BossColorYellow, "§e"
	default:
		return java.BossColorRed, "§c"
	}
}

// barBlocks renders a 10-cell progress bar: filled █, empty ░.
func barBlocks(filled int) string {
	if filled < 0 {
		filled = 0
	}
	if filled > tpsbarWidth {
		filled = tpsbarWidth
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", tpsbarWidth-filled)
}

// tpsbarTitle builds the single-line bar title with legacy § spans. The
// vanilla boss bar font has no glyph for \n, so a two-line title shows a
// missing-glyph box mid-screen — everything lives on one line instead.
func tpsbarTitle(tps, mspt float64, rt runtimeSnapshot) string {
	_, code := tpsColor(tps)
	filled := int(tps/20*tpsbarWidth + 0.5)

	msptCode := "§a"
	switch {
	case mspt >= 50:
		msptCode = "§c"
	case mspt >= 25:
		msptCode = "§e"
	}

	return fmt.Sprintf("%sTPS §f%.1f %s%s §7| §bMSPT %s%.1fms §7| §d协程 §f%d §7| §dGC §f%d §7| §d内存 §f%.1fMB",
		code, tps, code, barBlocks(filled), msptCode, mspt,
		rt.Goroutines, rt.NumGC, rt.HeapMB)
}

// newBarUUID mints the per-player boss bar identifier.
func newBarUUID() [16]byte {
	var u [16]byte
	_, _ = rand.Read(u[:])
	u[6] = (u[6] & 0x0F) | 0x40 // RFC 4122 version 4
	u[8] = (u[8] & 0x3F) | 0x80
	return u
}

// sendBossAdd sends the ADD operation with the current values so the bar
// appears with real data instead of a blank frame.
func (c *conn) sendBossAdd(title string, progress float32, color int32) error {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayBossEvent)
	java.WriteBossAdd(body, c.player.barUUID, title, progress, color, java.BossOverlayNotched10, 0)
	return c.sendPacket(body.Bytes())
}

// sendBossRemove clears the bar on toggle-off.
func (c *conn) sendBossRemove() error {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayBossEvent)
	java.WriteBossRemove(body, c.player.barUUID)
	return c.sendPacket(body.Bytes())
}

// sendSystemChat delivers a command feedback line.
func (c *conn) sendSystemChat(text string) error {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySystemChat)
	java.WriteSystemChat(body, text)
	return c.sendPacket(body.Bytes())
}

// refreshTpsbars pushes fresh title/progress/style to every tpsbar user.
// The bar UUID is per player, so each payload is encoded individually;
// the volume (three small packets per user per second) is negligible.
func (s *Server) refreshTpsbars(targets []*player) {
	if len(targets) == 0 {
		return
	}
	mspt, tps, _, _ := s.stats.snapshot()
	rt := snapshotRuntime()
	title := tpsbarTitle(tps, mspt, rt)
	progress := float32(tps / 20)
	color, _ := tpsColor(tps)

	for _, p := range targets {
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlayBossEvent)
		java.WriteBossTitle(body, p.barUUID, title)
		if err := p.conn.sendPacket(body.Bytes()); err != nil {
			continue
		}
		body = protocol.NewWriter()
		body.VarInt(v776.PacketPlayBossEvent)
		java.WriteBossProgress(body, p.barUUID, progress)
		if err := p.conn.sendPacket(body.Bytes()); err != nil {
			continue
		}
		body = protocol.NewWriter()
		body.VarInt(v776.PacketPlayBossEvent)
		java.WriteBossStyle(body, p.barUUID, color, java.BossOverlayNotched10)
		_ = p.conn.sendPacket(body.Bytes())
	}
}

// formatTPS renders the /tps text readout.
func formatTPS(mspt, t1, t5, t15 float64) string {
	_, code := tpsColor(t1)
	return code + "TPS §f" + strconv.FormatFloat(t1, 'f', 1, 64) +
		" §7(1m) " + strconv.FormatFloat(t5, 'f', 1, 64) +
		" §7(5m) " + strconv.FormatFloat(t15, 'f', 1, 64) +
		" §7(15m)\n§bMSPT §f" + strconv.FormatFloat(mspt, 'f', 1, 64) + "ms"
}
