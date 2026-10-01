// Package ui renders the server's console output with the Catppuccin
// palette (Mocha values) using pure stdlib ANSI escapes — no external
// dependency. Colour is an enhancement only: every message carries an
// ASCII symbol prefix so semantics survive on monochrome terminals.
//
// Degradation chain (mirrors common CLI conventions):
//   - NO_COLOR set            -> plain text
//   - TERM=dumb               -> plain text
//   - stdout not a terminal   -> plain text (pipes, files, CI)
//   - CLICOLOR_FORCE=1        -> colour even when not a terminal
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// Catppuccin Mocha semantic colours (deep-terminal values).
const (
	mauve   = "#cba6f7" // 标题/强调
	green   = "#a6e3a1" // 成功
	peach   = "#fab387" // 警告
	red     = "#f38ba8" // 错误
	blue    = "#89b4fa" // 路径/链接
	yellow  = "#f9e2af" // 数字/计数
	sky     = "#89dceb" // 信息
	pink    = "#f5c2e7" // 关键字
	subtext = "#a6adc8" // 暗淡/说明
)

// ASCII symbol prefixes (colour-blind friendly).
const (
	SymSuccess = "OK"
	SymFail    = "X"
	SymWarn    = "!"
	SymInfo    = "i"
)

var enabled = detect()

func detect() bool {
	if os.Getenv("NO_COLOR") != "" {
		return false // NO_COLOR overrides everything
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	if os.Getenv("CLICOLOR_FORCE") == "1" {
		return true
	}
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}

// hexRGB converts #rrggbb to r;g;b for a truecolor SGR sequence.
func hexRGB(hex string) string {
	var r, g, b uint8
	fmt.Sscanf(hex, "#%02x%02x%02x", &r, &g, &b)
	return fmt.Sprintf("%d;%d;%d", r, g, b)
}

// paint wraps s in a truecolor SGR sequence, or returns it unchanged
// when colour is disabled.
func paint(hex, s string) string {
	if !enabled || s == "" {
		return s
	}
	return "\x1b[38;2;" + hexRGB(hex) + "m" + s + "\x1b[0m"
}

// Bold paints s with the bold attribute (colour-independent).
func Bold(s string) string {
	if !enabled || s == "" {
		return s
	}
	return "\x1b[1m" + s + "\x1b[0m"
}

// Semantically named painters. Callers embed the result anywhere inside a
// log message; a plain-text environment yields the bare string.
var (
	Title   = func(s string) string { return Bold(paint(mauve, s)) } // 标题/强调
	Success = func(s string) string { return Bold(paint(green, s)) } // 成功
	Warn    = func(s string) string { return Bold(paint(peach, s)) } // 警告
	Error   = func(s string) string { return Bold(paint(red, s)) }   // 错误
	Path    = func(s string) string { return paint(blue, s) }        // 路径/链接
	Number  = func(s string) string { return paint(yellow, s) }      // 数字/计数
	Info    = func(s string) string { return paint(sky, s) }         // 信息
	Keyword = func(s string) string { return Bold(paint(pink, s)) }  // 关键字
	Dim     = func(s string) string { return paint(subtext, s) }     // 暗淡
)

// Fprintln writes the pieces joined as one line to w (colour is already
// baked into the pieces by the painters above).
func Fprintln(w io.Writer, pieces ...string) {
	fmt.Fprintln(w, strings.Join(pieces, ""))
}

// Enabled reports whether colour output is active.
func Enabled() bool { return enabled }
