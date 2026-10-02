package server

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/masgzy/gopherite/protocol/java"
)

// tinyPNG is a valid 1x1 PNG (a single red pixel).
var tinyPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==")

// bigPNG builds a valid but large PNG: 128x128 seeded random RGBA pixels
// compress poorly, so the encoded file comfortably exceeds the favicon
// budget.
func bigPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 128, 128))
	rng := rand.New(rand.NewSource(262))
	for i := range img.Pix {
		img.Pix[i] = byte(rng.Intn(256))
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func writeTempFile(t *testing.T, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestLoadFaviconValidation(t *testing.T) {
	if uri, err := loadFaviconDataURI(""); err != nil || uri != "" {
		t.Fatalf("empty path: uri=%q err=%v", uri, err)
	}

	// A small valid PNG is accepted and rendered as a data URI.
	path := writeTempFile(t, "icon.png", tinyPNG)
	uri, err := loadFaviconDataURI(path)
	if err != nil {
		t.Fatalf("valid png rejected: %v", err)
	}
	if !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("bad prefix: %q", uri[:40])
	}

	// Non-PNG content (e.g. a saved HTML error page) must be refused.
	path = writeTempFile(t, "fake.png", []byte("<html>404 not found</html>"))
	if uri, err := loadFaviconDataURI(path); err == nil {
		t.Fatalf("non-png accepted: %q", uri[:40])
	}

	// A valid but oversized PNG must be refused: base64-expanding it would
	// push the status document past the client's 32767-char string limit
	// and kill the MOTD ping entirely.
	path = writeTempFile(t, "huge.png", bigPNG(t))
	if _, err := loadFaviconDataURI(path); err == nil {
		t.Fatal("oversized png accepted")
	}
}

// TestStatusDocumentFitsClientStringLimit replays the failure mode seen on
// real clients: the server list decodes the whole document as one
// ≤32767-char string (26.2 Utf8String.read via lenientJson) and aborts the
// ping with a DecoderException when it is longer — the MOTD then never
// loads at all.
func TestStatusDocumentFitsClientStringLimit(t *testing.T) {
	s, err := New(Options{
		ListenAddr:     "127.0.0.1:0",
		MOTD:           "正常公告",
		MaxPlayers:     20,
		VersionName:    "26.2",
		ProtocolNumber: 776,
	})
	if err != nil {
		t.Fatal(err)
	}

	doc := s.statusDocument()
	if len(doc) > java.MaxStatusJSONLen {
		t.Fatalf("plain document exceeds limit: %d chars", len(doc))
	}

	// A giant favicon (as produced by an oversized-but-accepted icon) must
	// be shed, not shipped: the document has to stay decodable.
	s.favicon = "data:image/png;base64," + strings.Repeat("QUJD", 20000) // ~80k chars
	doc = s.statusDocument()
	if len(doc) > java.MaxStatusJSONLen {
		t.Fatalf("document with favicon exceeds limit: %d chars", len(doc))
	}
	st, err := java.ReadStatusResponse([]byte(doc))
	if err != nil {
		t.Fatalf("shed document invalid: %v", err)
	}
	if st.Favicon != "" {
		t.Fatal("oversized favicon not shed")
	}
	if st.DescriptionText != "正常公告" {
		t.Fatalf("motd lost while shedding favicon: %q", st.DescriptionText)
	}

	// A pathological MOTD alone must degrade by truncation, never break
	// the ping.
	s.favicon = ""
	s.opts.MOTD = strings.Repeat("公告", 20000)
	doc = s.statusDocument()
	if len(doc) > java.MaxStatusJSONLen {
		t.Fatalf("document with giant motd exceeds limit: %d chars", len(doc))
	}
	if _, err := java.ReadStatusResponse([]byte(doc)); err != nil {
		t.Fatalf("truncated document invalid: %v", err)
	}
}

func TestStatusColorsSurvive(t *testing.T) {
	s, err := New(Options{ListenAddr: "127.0.0.1:0", MOTD: "§a绿色§r公告", MaxPlayers: 1, VersionName: "26.2", ProtocolNumber: 776})
	if err != nil {
		t.Fatal(err)
	}
	doc := s.statusDocument()
	st, err := java.ReadStatusResponse([]byte(doc))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if st.DescriptionText != "§a绿色§r公告" {
		t.Fatalf("legacy codes mangled: %q", st.DescriptionText)
	}
}
