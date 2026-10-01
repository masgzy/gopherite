package server

import (
	"log"
	"strings"

	"github.com/masgzy/gopherite/internal/ui"
)

// handleCommand dispatches a serverbound chat command (the string arrives
// without the leading slash). The command surface is intentionally tiny:
// /tpsbar and /tps today, growing with later milestones.
func (c *conn) handleCommand(line string) error {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return nil
	}
	log.Printf(ui.Info("i ")+"%s 执行命令: /%s", c.username, line)

	switch fields[0] {
	case "tpsbar":
		return c.toggleTpsbar()
	case "tps":
		return c.sendTpsChat()
	default:
		return c.sendSystemChat("§c未知命令: " + fields[0] + " §7(可用: /tpsbar, /tps)")
	}
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
