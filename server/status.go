package server

import (
	"encoding/base64"
	"encoding/json"
	"os"

	"github.com/masgzy/gopherite/protocol/java"
)

// statusDocument builds the JSON server list response. The document is
// small and per-connection: we simply marshal a fresh struct each time
// (json.Marshal has no shared state and the payload is ~200 bytes;
// caching per tick is a planned micro-optimisation).
func (s *Server) statusDocument() string {
	online := 0 // joins are rejected pre-M2; wired to the player list later
	resp := java.StatusResponse{
		VersionName:        s.opts.VersionName,
		Protocol:           s.opts.ProtocolNumber,
		MaxPlayers:         s.opts.MaxPlayers,
		OnlinePlayers:      online,
		DescriptionText:    s.opts.MOTD,
		Favicon:            s.favicon,
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
	const maxFaviconBytes = 64 << 10 // vanilla rejects oversized icons
	if len(data) > maxFaviconBytes {
		return "", os.ErrInvalid
	}
	return "data:image/png;base64," + base64.StdEncoding.EncodeToString(data), nil
}
