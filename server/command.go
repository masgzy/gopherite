package server

import (
	"log"
	"strings"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// handleCommand dispatches a serverbound chat command (the string arrives
// without the leading slash) through the Brigadier tree. M16 fix: the
// tree existed since M3 but this dispatcher only knew /tps and /tpsbar —
// every other registered command answered "未知命令".
func (c *conn) handleCommand(line string) error {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	log.Printf(ui.Info("i ")+"%s 执行命令: /%s", c.username, line)

	if pc := tryParse(getCommandRoot(), line); pc != nil && pc.run != nil {
		return pc.run(c, pc.args)
	}
	return c.sendSystemChat("§c未知命令: " + fields[0] + " §7(输入 /help 查看可用命令)")
}

// handleCommandSuggestion answers a Tab-complete request with the
// matches from suggestionsFor (start/length mark the replaced span).
func (c *conn) handleCommandSuggestion() error {
	id, text, err := java.ReadCommandSuggestion(c.rd)
	if err != nil {
		return err
	}
	start, length, matches := suggestionsFor(text)
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayCBCommandSuggestion)
	java.WriteCommandSuggestions(body, id, start, length, matches)
	return c.sendPacket(body.Bytes())
}

// toggleTpsbar flips the player's personal performance boss bar.
func (c *conn) toggleTpsbar() error {
	p := c.player
	if p == nil {
		return nil
	}
	s := c.s
	s.mu.Lock()
	on := !p.tpsbar
	p.tpsbar = on
	s.mu.Unlock()

	if !on {
		if err := c.sendBossRemove(); err != nil {
			return err
		}
		return c.sendSystemChat("§7已关闭 TPS/MSPT 监视条")
	}
	// Show real data immediately; the ticker keeps it live at 1 Hz.
	mspt, tps, _, _ := s.stats.snapshot()
	color, _ := tpsColor(tps)
	if err := c.sendBossAdd(tpsbarTitle(tps, mspt, snapshotRuntime()), float32(tps/20), color); err != nil {
		return err
	}
	return c.sendSystemChat("§a已开启 TPS/MSPT 监视条 §7(再次输入 /tpsbar 关闭)")
}

// sendTpsChat answers /tps with the text readout.
func (c *conn) sendTpsChat() error {
	mspt, t1, t5, t15 := c.s.stats.snapshot()
	return c.sendSystemChat(formatTPS(mspt, t1, t5, t15))
}
