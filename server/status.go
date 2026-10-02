package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/masgzy/gopherite/protocol/java"
)

// pngMagic is the 8-byte signature every PNG file starts with. Anything
// else would either fail the client-side base64/image decode or silently
// render garbage, so we refuse it up front.
var pngMagic = []byte("\x89PNG\r\n\x1a\n")

// maxFaviconRawBytes bounds the raw icon file. The vanilla client reads
// the whole status document as ONE string capped at
// java.MaxStatusJSONLen (32767) characters and aborts the ping when it is
// longer, so the base64 icon must leave room for the rest of the
// document: 18 KiB encodes to 24576 base64 chars plus the 22-char data
// prefix, well under the limit. Vanilla icons are 64x64 and normally
// 1-3 KiB, so the budget is generous in practice.
const maxFaviconRawBytes = 18 << 10

// statusDocument builds the JSON server list response. The document is
// small and per-connection: we simply marshal a fresh struct each time
// (json.Marshal has no shared state and the payload is ~200 bytes;
// caching per tick is a planned micro-optimisation).
func (s *Server) statusDocument() string {
	online := 0 // wired to the player list in M2
	build := func(motd, favicon string) string {
		resp := java.StatusResponse{
			VersionName:        s.opts.VersionName,
			Protocol:           s.opts.ProtocolNumber,
			MaxPlayers:         s.opts.MaxPlayers,
			OnlinePlayers:      online,
			DescriptionText:    motd,
			Favicon:            favicon,
			EnforcesSecureChat: false,
		}
		b, err := json.Marshal(resp)
		if err != nil {
			// Marshalling a plain-text status cannot fail in practice; fall
			// back to a minimal valid document rather than dropping the ping.
			return `{"version":{"name":"error","protocol":-1},"players":{"max":0,"online":0},"description":{"text":"status marshalling failed"}}`
		}
		return string(b)
	}

	motd := s.opts.MOTD
	doc := build(motd, s.favicon)
	if len(doc) > java.MaxStatusJSONLen && s.favicon != "" {
		// Shed the heavy base64 icon first: an over-long document kills
		// the ping outright on the client, while a missing icon merely
		// hides the picture.
		doc = build(motd, "")
	}
	for len(doc) > java.MaxStatusJSONLen && len(motd) > 0 {
		// Absurdly long MOTDs keep shrinking until the document fits.
		runes := []rune(motd)
		runes = runes[:len(runes)/2]
		motd = string(runes)
		doc = build(motd, "")
	}
	if len(doc) > java.MaxStatusJSONLen {
		return `{"version":{"name":"error","protocol":-1},"players":{"max":0,"online":0},"description":{"text":"status document too large"}}`
	}
	return doc
}

// loadFaviconDataURI reads a 64x64 PNG and encodes it as the data URI the
// server list expects. An empty path or missing file disables the icon.
func loadFaviconDataURI(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // optional by design
		}
		return "", err
	}
	if !bytes.HasPrefix(data, pngMagic) {
		return "", errors.New("不是有效的 PNG 文件")
	}
	if len(data) > maxFaviconRawBytes {
		return "", fmt.Errorf("图标 %d 字节超过上限 %d：过大的图标 base64 展开后会超出客户端 32767 字符的状态包上限，导致 MOTD 无法显示", len(data), maxFaviconRawBytes)
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), nil
}
