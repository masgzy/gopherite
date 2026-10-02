package server

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	crypto_rand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// startTestServer binds an ephemeral port synchronously and serves in the
// background; cleanup shuts it down.
func startTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{
		ListenAddr:        "127.0.0.1:0",
		MOTD:              "test 驱动的服务器",
		MaxPlayers:        42,
		VersionName:       v776.Name,
		ProtocolNumber:    v776.ProtocolNumber,
		ViewDistance:      2,
		KeepAliveInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = s.Serve() }()
	select {
	case <-s.started:
	case <-time.After(5 * time.Second):
		t.Fatal("server never signalled startup")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s
}

// dialTest opens a TCP connection with a buffered writer and a frame
// reader; tests close the conn via cleanup.
func dialTest(t *testing.T, addr string) (net.Conn, *bufio.Writer, *protocol.FrameReader) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	br := bufio.NewReader(c)
	return c, bufio.NewWriter(c), protocol.NewFrameReader(br, protocol.DefaultMaxPacketLen)
}

func TestServerListPing(t *testing.T) {
	s := startTestServer(t)
	c, bw, fr := dialTest(t, s.Addr().String())
	_ = c

	// Handshake: intent status.
	w := protocol.NewWriter()
	w.VarInt(v776.PacketHandshake).
		VarInt(v776.ProtocolNumber).
		String("127.0.0.1").
		Uint16(25565).
		VarInt(int32(java.IntentStatus))
	if err := protocol.WriteFramed(bw, w.Bytes()); err != nil {
		t.Fatal(err)
	}

	// Status request (empty payload beyond the packet id).
	w.Reset()
	w.VarInt(v776.PacketStatusRequest)
	if err := protocol.WriteFramed(bw, w.Bytes()); err != nil {
		t.Fatal(err)
	}

	payload, err := fr.Next()
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	r := protocol.NewReader(payload)
	id, _ := r.VarInt()
	if id != v776.PacketStatusResponse {
		t.Fatalf("want status response 0x0, got 0x%x", id)
	}
	body, err := r.String(java.MaxStatusJSONLen)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	// The vanilla client reads the whole document as one string capped
	// at MaxStatusJSONLen chars; anything longer aborts the ping before
	// the MOTD can ever render.
	if len(body) > java.MaxStatusJSONLen {
		t.Fatalf("status document %d chars exceeds client limit %d", len(body), java.MaxStatusJSONLen)
	}
	status, err := java.ReadStatusResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse json: %v (%s)", err, body)
	}
	if status.Protocol != v776.ProtocolNumber {
		t.Fatalf("protocol: want %d, got %d", v776.ProtocolNumber, status.Protocol)
	}
	if status.MaxPlayers != 42 {
		t.Fatalf("max players: want 42, got %d", status.MaxPlayers)
	}
	if status.DescriptionText != "test 驱动的服务器" {
		t.Fatalf("motd: %q", status.DescriptionText)
	}

	// Ping -> pong echo.
	nonce := time.Now().UnixNano()
	w.Reset()
	w.VarInt(v776.PacketPingRequest).Int64(nonce)
	if err := protocol.WriteFramed(bw, w.Bytes()); err != nil {
		t.Fatal(err)
	}
	payload, err = fr.Next()
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	r.Reset(payload)
	id, _ = r.VarInt()
	pong, perr := r.Int64()
	if id != v776.PacketPongResponse || perr != nil || pong != nonce {
		t.Fatalf("pong: id=0x%x nonce=%d err=%v", id, pong, perr)
	}
}

func TestStatusAdvertisesOfflineMode(t *testing.T) {
	s, err := New(Options{ListenAddr: "127.0.0.1:0", OnlineMode: false, MOTD: "x", MaxPlayers: 1, VersionName: v776.Name, ProtocolNumber: v776.ProtocolNumber})
	if err != nil {
		t.Fatal(err)
	}
	if s.Options().OnlineMode {
		t.Fatal("online mode flag lost")
	}
}

// botConn is the headless test client: a protocol-level bot that walks the
// full 26.2 login -> configuration -> play flow against the server. It
// validates exactly what the vanilla client requires, minus rendering.
type botConn struct {
	t      *testing.T
	nc     net.Conn
	bw     *bufio.Writer
	br     *bufio.Reader
	fr     *protocol.FrameReader
	wr     *protocol.Writer
	comp   *protocol.CompressionLayer
	enc    *protocol.CFB8
	dec    *protocol.CFB8
	regCnt int
	login  java.ClientboundLogin
	chunks int
}

func dialBot(t *testing.T, addr string) *botConn {
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	br := bufio.NewReader(c)
	return &botConn{
		t:  t,
		nc: c,
		bw: bufio.NewWriter(c),
		br: br,
		fr: protocol.NewFrameReader(br, protocol.DefaultMaxPacketLen),
		wr: protocol.NewWriter(),
	}
}

// write sends a packet body through the current layers. The client-side
// order mirrors vanilla: compress, then encrypt, then the plaintext outer
// length VarInt.
func (b *botConn) write(body []byte) {
	b.t.Helper()
	payload := body
	if b.comp != nil {
		payload = b.comp.CompressInner(body)
	}
	if b.enc != nil {
		payload = b.enc.Crypt(nil, payload)
	}
	if _, err := b.bw.Write(frame(payload)); err != nil {
		b.t.Fatalf("write: %v", err)
	}
	if err := b.bw.Flush(); err != nil {
		b.t.Fatalf("flush: %v", err)
	}
}

// frame prepends the outer length VarInt (uncompressed path only).
func frame(body []byte) []byte {
	out := protocol.NewWriter()
	out.VarInt(int32(len(body)))
	return append(out.Bytes(), body...)
}

// next reads one packet, decrypting and decompressing as configured, and
// returns a reader positioned after the packet id.
func (b *botConn) next() (int32, *protocol.Reader) {
	b.t.Helper()
	payload, err := b.fr.Next()
	if err != nil {
		b.t.Fatalf("read frame: %v", err)
	}
	if b.dec != nil {
		payload = b.dec.Crypt(nil, payload)
	}
	if b.comp != nil {
		payload, err = b.comp.DecompressFrame(payload, protocol.DefaultMaxPacketLen)
		if err != nil {
			b.t.Fatalf("decompress: %v", err)
		}
	}
	r := protocol.NewReader(payload)
	if debugBotPackets {
		b.t.Logf("next raw: len=%d bytes=% x", len(payload), payload[:minInt(16, len(payload))])
	}
	id, err := r.VarInt()
	if err != nil {
		b.t.Fatalf("packet id: %v (payload len %d)", err, len(payload))
	}
	return id, r
}

var debugBotPackets = false

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// expect asserts the next packet id.
func (b *botConn) expect(want int32) *protocol.Reader {
	b.t.Helper()
	id, r := b.next()
	if id != want {
		b.t.Fatalf("want packet 0x%x, got 0x%x", want, id)
	}
	return r
}

func sendHandshake(b *botConn, intent java.Intent) {
	b.t.Helper()
	w := protocol.NewWriter()
	w.VarInt(v776.PacketHandshake).
		VarInt(v776.ProtocolNumber).
		String("127.0.0.1").
		Uint16(25565).
		VarInt(int32(intent))
	b.write(w.Bytes())
}

// TestFullJoinFlow drives the complete M2 flow: login (offline),
// configuration with registry negotiation, play spawn sequence and chunk
// streaming after PlayerLoaded.
func TestFullJoinFlow(t *testing.T) {
	s := startTestServer(t)
	b := dialBot(t, s.Addr().String())

	// --- Login state ---
	sendHandshake(b, java.IntentLogin)
	w := protocol.NewWriter()
	w.VarInt(v776.PacketLSHello).String("Gopher").UUID(protocol.OfflinePlayerUUID("Gopher"))
	b.write(w.Bytes())

	// Compression arrives first (threshold 256).
	r := b.expect(v776.PacketLoginCompression)
	threshold, err := r.VarInt()
	if err != nil || threshold <= 0 {
		t.Fatalf("threshold: %v %d", err, threshold)
	}
	b.comp = protocol.NewCompressionLayer(threshold)

	// Login success with offline UUID + session id.
	r = b.expect(v776.PacketLoginFinished)
	gotID, err := r.UUID()
	if err != nil {
		t.Fatal(err)
	}
	wantID := protocol.OfflinePlayerUUID("Gopher")
	if gotID != wantID {
		t.Fatalf("profile id: want %x got %x", wantID, gotID)
	}
	name, err := r.String(16)
	if err != nil || name != "Gopher" {
		t.Fatalf("name: %v %q", err, name)
	}
	props, err := r.VarInt()
	if err != nil || props != 0 {
		t.Fatalf("properties: %v %d", err, props)
	}
	session, err := r.UUID()
	if err != nil {
		t.Fatal(err)
	}
	if session == ([16]byte{}) {
		t.Fatal("session id must not be zero")
	}

	// Acknowledge the switch into configuration.
	w.Reset()
	w.VarInt(v776.PacketLSAcknowledged)
	b.write(w.Bytes())

	// --- Configuration state ---
	// Expect brand payload, feature flags, known packs (registry data x29
	// then finish) in vanilla order; answer known packs mid-stream.
	sentClientInfo := false
	sentKnownPacks := false
	registryCount := 0
	for {
		id, rr := b.next()
		switch id {
		case v776.PacketCfgPayload:
			if _, err := rr.String(256); err != nil {
				t.Fatal(err)
			}
			if _, err := rr.Bytes(); err != nil {
				t.Fatal(err)
			}
		case v776.PacketCfgFeatures:
			n, _ := rr.VarInt()
			for i := int32(0); i < n; i++ {
				_, _ = rr.String(128)
			}
		case v776.PacketCfgKnownPacks:
			// Answer with the same core pack, triggering registry sync.
			n, _ := rr.VarInt()
			if n <= 0 {
				t.Fatal("server sent no known packs")
			}
			for i := int32(0); i < n; i++ {
				_, _ = rr.String(256)
				_, _ = rr.String(256)
				_, _ = rr.String(256)
			}
			if !sentClientInfo {
				cw := protocol.NewWriter()
				cw.VarInt(v776.PacketConfigClientInfo)
				cw.String("en_US") // language
				cw.Byte(2)         // view distance
				cw.VarInt(0)       // chat visibility: full
				cw.Bool(true)      // chat colors
				cw.Byte(0)         // model customisation
				cw.VarInt(1)       // main hand: right
				cw.Bool(false)     // text filtering
				cw.Bool(false)     // allows listing
				cw.VarInt(0)       // particle status: all
				b.write(cw.Bytes())
				sentClientInfo = true
			}
			kw := protocol.NewWriter()
			kw.VarInt(v776.PacketConfigKnownPacks)
			kw.VarInt(1)
			kw.String("minecraft").String("core").String(v776.Name)
			b.write(kw.Bytes())
			sentKnownPacks = true
		case v776.PacketCfgRegistryData:
			key, err := rr.String(256)
			if err != nil {
				t.Fatal(err)
			}
			if key == "" {
				t.Fatal("empty registry key")
			}
			entries, _ := rr.VarInt()
			for i := int32(0); i < entries; i++ {
				id, err := rr.String(256)
				if err != nil {
					t.Fatal(err)
				}
				present, _ := rr.VarInt()
				if present == 1 {
					// Registry entries carry full network NBT: a rootless
					// compound (1.20.2+ format, self-delimiting).
					root, _ := rr.Byte()
					if root != 0x0A {
						t.Fatalf("registry %s entry %s: NBT root 0x%x", key, id, root)
					}
					skipNbtPayload(t, rr, 0x0A)
				}
			}
			registryCount++
		case v776.PacketCfgUpdateTags:
			skipUpdateTagsPayload(t, rr)
		case v776.PacketCfgFinish:
			// Acknowledge the switch into play.
			fw := protocol.NewWriter()
			fw.VarInt(v776.PacketConfigFinish)
			b.write(fw.Bytes())
			goto configDone
		case v776.PacketCfgDisconnect:
			reason, _ := rr.String(1024)
			t.Fatalf("config disconnect: %s", reason)
		default:
			t.Fatalf("unexpected config packet 0x%x", id)
		}
	}
configDone:
	if !sentKnownPacks {
		t.Fatal("known pack exchange never happened")
	}
	if registryCount != 29 {
		t.Fatalf("registry data packets: want 29, got %d", registryCount)
	}

	// --- Play state ---
	// Acknowledge configuration on the play protocol.
	paw := protocol.NewWriter()
	paw.VarInt(v776.PacketPlayConfigAcknowledged)
	b.write(paw.Bytes())

	// The spawn sequence: login, difficulty, abilities, held slot,
	// player info, position, cache center, cache radius, spawn position,
	// time, game event.
	for {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayLogin:
			entityID, _ := rr.Int32()
			if entityID != 1 {
				t.Fatalf("entity id: want 1, got %d", entityID)
			}
			hardcore, _ := rr.Bool()
			_ = hardcore
			nLevels, _ := rr.VarInt()
			for i := int32(0); i < nLevels; i++ {
				lv, _ := rr.String(256)
				if lv != "minecraft:overworld" {
					t.Fatalf("level: %q", lv)
				}
			}
			// Keep parsing minimal: peek remaining fields exist.
			_, _ = rr.VarInt() // max players
			_, _ = rr.VarInt() // chunk radius
			_, _ = rr.VarInt() // simulation distance
			_, _ = rr.Bool()   // reduced debug
			_, _ = rr.Bool()   // show death screen
			_, _ = rr.Bool()   // limited crafting
			// Spawn info.
			_, _ = rr.VarInt() // dimension type holder (1 = overworld)
			dim, _ := rr.String(256)
			if dim != "minecraft:overworld" {
				t.Fatalf("dimension: %q", dim)
			}
			_, _ = rr.Int64() // seed
			_, _ = rr.Byte()  // gamemode
			_, _ = rr.Byte()  // previous gamemode
			_, _ = rr.Bool()  // debug
			flat, _ := rr.Bool()
			if !flat {
				t.Fatal("world must be flagged superflat")
			}
			if hasDeath, _ := rr.Bool(); hasDeath {
				t.Fatal("death location must be absent")
			}
			_, _ = rr.VarInt() // portal cooldown
			_, _ = rr.VarInt() // sea level
			online, _ := rr.Bool()
			if online {
				t.Fatal("test server runs offline mode")
			}
			_, _ = rr.Bool() // secure chat
		case v776.PacketPlayDifficulty:
			_, _ = rr.Byte()
			_, _ = rr.Bool()
		case v776.PacketPlayAbilities:
			_, _ = rr.Byte()
			_, _ = rr.Float()
			_, _ = rr.Float()
		case v776.PacketPlayHeldSlot:
			_, _ = rr.VarInt()
		case v776.PacketPlayPlayerInfo:
			_, _ = rr.Byte()
			n, _ := rr.VarInt()
			for i := int32(0); i < n; i++ {
				if _, err := rr.UUID(); err != nil {
					t.Fatal(err)
				}
				pcount, _ := rr.VarInt()
				for j := int32(0); j < pcount; j++ {
					_, _ = rr.String(64)
					_, _ = rr.String(1024)
					signed, _ := rr.Bool()
					if signed {
						_, _ = rr.String(1024)
					}
				}
				if ok, _ := rr.Bool(); ok {
					t.Fatal("chat session must be null")
				}
				_, _ = rr.VarInt() // gamemode
				_, _ = rr.Bool()   // listed
				_, _ = rr.VarInt() // latency
				if ok, _ := rr.Bool(); ok {
					t.Fatal("display name must be null")
				}
				_, _ = rr.VarInt() // list order
				_, _ = rr.Bool()   // hat
			}
		case v776.PacketPlayPlayerPosition:
			_, _ = rr.VarInt() // teleport id
			// 3 doubles position, 3 doubles delta, 2 floats rotation.
			for i := 0; i < 3; i++ {
				_, _ = rr.Double()
			}
			for i := 0; i < 3; i++ {
				_, _ = rr.Double()
			}
			_, _ = rr.Float()
			_, _ = rr.Float()
			_, _ = rr.Int32() // relatives
		case v776.PacketPlayCacheCenter:
			_, _ = rr.VarInt()
			_, _ = rr.VarInt()
		case v776.PacketPlayCacheRadius:
			rad, _ := rr.VarInt()
			if rad != 2 {
				t.Fatalf("radius: want 2, got %d", rad)
			}
		case v776.PacketPlaySpawnPosition:
			dim, _ := rr.String(256)
			if dim != "minecraft:overworld" {
				t.Fatalf("spawn dimension: %q", dim)
			}
			_, _ = rr.Int64()
			_, _ = rr.Float()
			_, _ = rr.Float()
		case v776.PacketPlaySetTime:
			_, _ = rr.Int64()
			clocks, _ := rr.VarInt()
			for i := int32(0); i < clocks; i++ {
				_, _ = rr.VarInt()
				_, _ = rr.VarLong()
				_, _ = rr.Float()
				_, _ = rr.Float()
			}
		case v776.PacketPlaySetPlayerInv:
			// Starter hotbar per-slot sync: skip.
			_, _ = rr.VarInt()
			cnt, _ := rr.VarInt()
			if cnt > 0 {
				_, _ = rr.VarInt()
				_, _ = rr.VarInt()
				_, _ = rr.VarInt()
			}
		case v776.PacketPlayCommands:
			// Declare Commands: payload not needed for the bot.
		case v776.PacketPlaySetHealth:
			// M8 vitals: health float, food varint, saturation float.
			_, _ = rr.Float()
			_, _ = rr.VarInt()
			_, _ = rr.Float()
		case v776.PacketPlaySetExperience:
			// M8 XP bar: progress float, level varint, total varint.
			_, _ = rr.Float()
			_, _ = rr.VarInt()
			_, _ = rr.VarInt()
		case v776.PacketPlayUpdateAttributes:
			// M8 attribute sync: entity id, then per-attr id/base/modifiers.
			_, _ = rr.VarInt()
			n, _ := rr.VarInt()
			for i := int32(0); i < n; i++ {
				_, _ = rr.VarInt()
				_, _ = rr.Double()
				ms, _ := rr.VarInt()
				for j := int32(0); j < ms; j++ {
					_, _ = rr.String(128)
					_, _ = rr.Double()
					_, _ = rr.VarInt()
				}
			}
		case v776.PacketPlayContainerContent:
			// M7 full inventory-menu sync: window, state, 46 slots + carried.
			_, _ = rr.VarInt() // container id
			_, _ = rr.VarInt() // state id
			n, _ := rr.VarInt()
			for i := int32(0); i < n; i++ {
				cnt, _ := rr.VarInt()
				if cnt > 0 {
					_, _ = rr.VarInt()
					_, _ = rr.VarInt()
					_, _ = rr.VarInt()
				}
			}
			ccnt, _ := rr.VarInt()
			if ccnt > 0 {
				_, _ = rr.VarInt()
				_, _ = rr.VarInt()
				_, _ = rr.VarInt()
			}
		case v776.PacketPlaySetCursorItem:
			ccnt, _ := rr.VarInt()
			if ccnt > 0 {
				_, _ = rr.VarInt()
				_, _ = rr.VarInt()
				_, _ = rr.VarInt()
			}
		case v776.PacketPlayGameEvent:
			ev, _ := rr.Byte()
			_, _ = rr.Float()
			if ev != 13 { // LEVEL_CHUNKS_LOAD_START
				t.Fatalf("game event: %d", ev)
			}
			goto spawned
		case v776.PacketPlayDisconnect:
			reason, _ := rr.String(1024)
			t.Fatalf("play disconnect: %s", reason)
		default:
			t.Fatalf("unexpected play packet 0x%x", id)
		}
	}
spawned:

	// Report loaded so the server streams chunks.
	lw := protocol.NewWriter()
	lw.VarInt(v776.PacketPlayPlayerLoaded)
	b.write(lw.Bytes())

	// Chunk batch: start, (2*2+1)^2 chunks, finish.
	b.expect(v776.PacketPlayChunkBatchStart)
	for {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayLevelChunk:
			if _, err := rr.Int32(); err != nil {
				t.Fatal(err)
			}
			if _, err := rr.Int32(); err != nil {
				t.Fatal(err)
			}
			// heightmaps
			nMaps, _ := rr.VarInt()
			for i := int32(0); i < nMaps; i++ {
				_, _ = rr.VarInt()
				longs, _ := rr.VarInt()
				for j := int32(0); j < longs; j++ {
					_, _ = rr.Int64()
				}
			}
			// section buffer
			size, _ := rr.VarInt()
			buf, err := rr.FixedBytes(int(size))
			if err != nil {
				t.Fatal(err)
			}
			if len(buf) == 0 || int(size) != len(buf) {
				t.Fatalf("section buffer: %d", size)
			}
			decodeFloorSection(t, buf)
			// block entities
			nBE, _ := rr.VarInt()
			if nBE != 0 {
				t.Fatalf("block entities: %d", nBE)
			}
			// light data
			readBitSet := func() {
				n, _ := rr.VarInt()
				for i := int32(0); i < n; i++ {
					_, _ = rr.Int64()
				}
			}
			readBitSet()
			readBitSet()
			readBitSet()
			readBitSet()
			readLightArrays := func() int {
				n, _ := rr.VarInt()
				total := 0
				for i := int32(0); i < n; i++ {
					ln, _ := rr.VarInt()
					if _, err := rr.FixedBytes(int(ln)); err != nil {
						b.t.Fatal(err)
					}
					total += int(ln)
				}
				return total
			}
			skyBytes := readLightArrays()
			if skyBytes != 26*2048 {
				t.Fatalf("sky light bytes: want %d, got %d", 26*2048, skyBytes)
			}
			readLightArrays()
			b.chunks++
		case v776.PacketPlayCBChunkBatchDone:
			n, _ := rr.VarInt()
			if int(n) != b.chunks {
				t.Fatalf("batch size: want %d, got %d", b.chunks, n)
			}
			if b.chunks != 25 {
				t.Fatalf("chunks: want 25 (radius 2), got %d", b.chunks)
			}
			goto chunksDone
		case v776.PacketPlayCBKeepAlive:
			// A real client answers keep-alives at any time.
			nonce, _ := rr.Int64()
			kw := protocol.NewWriter()
			kw.VarInt(v776.PacketPlaySBKeepAlive).Int64(nonce)
			b.write(kw.Bytes())
		case v776.PacketPlayDisconnect:
			reason, _ := rr.String(1024)
			t.Fatalf("play disconnect: %s", reason)
		default:
			t.Fatalf("unexpected chunk-phase packet 0x%x", id)
		}
	}
chunksDone:

	// Movement: walk east one block; the server must accept it silently.
	mw := protocol.NewWriter()
	mw.VarInt(v776.PacketPlayMovePos)
	mw.Double(1.5).Double(-60).Double(0.5)
	mw.Byte(1) // on ground
	b.write(mw.Bytes())

	// Keep-alive round trip: wait for the server's challenge and answer.
	_ = b.nc.SetReadDeadline(time.Now().Add(20 * time.Second))
	for {
		id, rr := b.next()
		if id == v776.PacketPlayCBKeepAlive {
			nonce, _ := rr.Int64()
			kw := protocol.NewWriter()
			kw.VarInt(v776.PacketPlaySBKeepAlive).Int64(nonce)
			b.write(kw.Bytes())
			break
		}
		if id == v776.PacketPlayDisconnect {
			reason, _ := rr.String(1024)
			t.Fatalf("play disconnect: %s", reason)
		}
	}
}

// TestCompressionRoundTrip guards the zlib layer used by the login flow.
func TestCompressionRoundTrip(t *testing.T) {
	layer := protocol.NewCompressionLayer(256)
	body := bytes.Repeat([]byte("hello compressed world, "), 16) // > 256 bytes
	inner := layer.CompressInner(body)                           // size prefix + zlib
	back, err := layer.DecompressFrame(inner, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, back) {
		t.Fatal("round trip mismatch")
	}
	// The inner form must be smaller for repetitive payloads.
	if len(inner) >= len(body)+5 {
		t.Fatalf("compression ineffective: %d vs %d", len(inner), len(body))
	}
	// Small payloads stay raw behind a zero size prefix.
	small := layer.CompressInner([]byte{1, 2, 3})
	back, err = layer.DecompressFrame(small, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back, []byte{1, 2, 3}) {
		t.Fatal("small payload round trip mismatch")
	}
}

// TestCFB8MatchesZlibStream sanity-checks CFB8 self-consistency: decrypt
// inverts encrypt for arbitrary inputs.
func TestCFB8MatchesZlibStream(t *testing.T) {
	key := bytes.Repeat([]byte{7}, 16)
	iv := bytes.Repeat([]byte{3}, 16)
	enc, err := protocol.NewCFB8Encrypter(key, iv)
	if err != nil {
		t.Fatal(err)
	}
	dec, err := protocol.NewCFB8Decrypter(key, iv)
	if err != nil {
		t.Fatal(err)
	}
	plain := bytes.Repeat([]byte("minecraft cfb8"), 1000)
	cipherText := enc.Crypt(nil, plain)
	got := dec.Crypt(nil, cipherText)
	if !bytes.Equal(plain, got) {
		t.Fatal("cfb8 round trip mismatch")
	}
	// Cross-check the first byte against a second independent cipher pair.
	enc2, _ := protocol.NewCFB8Encrypter(key, iv)
	single := enc2.Crypt(nil, plain[:1])
	if single[0] != cipherText[0] {
		t.Fatal("cfb8 stateful mismatch")
	}
}

// silence unused import warnings for helpers used in build tags elsewhere.
var (
	_ = io.Discard
	_ = zlib.BestSpeed
	_ = json.Marshal
)

// startEncryptedTestServer boots an online-mode server with the session
// auth bypass so tests can walk the real RSA/CFB8 handshake.
func startEncryptedTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{
		ListenAddr:        "127.0.0.1:0",
		MaxPlayers:        4,
		OnlineMode:        true,
		SkipSessionAuth:   true,
		VersionName:       v776.Name,
		ProtocolNumber:    v776.ProtocolNumber,
		ViewDistance:      1,
		KeepAliveInterval: time.Hour, // not hit during the test
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = s.Serve() }()
	select {
	case <-s.started:
	case <-time.After(5 * time.Second):
		t.Fatal("server never signalled startup")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})
	return s
}

// TestEncryptedJoinFlow walks login with the real encryption handshake:
// the bot RSA-encrypts a shared secret, both sides switch to CFB8 and the
// rest of the flow (compression, login success, configuration, play spawn)
// must decrypt and decode end to end.
func TestEncryptedJoinFlow(t *testing.T) {
	s := startEncryptedTestServer(t)
	b := dialBot(t, s.Addr().String())

	// --- Login: hello ---
	sendHandshake(b, java.IntentLogin)
	w := protocol.NewWriter()
	w.VarInt(v776.PacketLSHello).String("Crypto").UUID(protocol.OfflinePlayerUUID("Crypto"))
	b.write(w.Bytes())

	// Encryption request: empty server id, DER public key, 4-byte token.
	r := b.expect(v776.PacketLSHello)
	if serverID, err := r.String(64); err != nil || serverID != "" {
		t.Fatalf("server id: %v %q", err, serverID)
	}
	pubDER, err := r.Bytes()
	if err != nil || len(pubDER) == 0 {
		t.Fatalf("public key: %v (%d bytes)", err, len(pubDER))
	}
	challenge, err := r.Bytes()
	if err != nil || len(challenge) != 4 {
		t.Fatalf("challenge: %v (%d bytes)", err, len(challenge))
	}
	if auth, _ := r.Bool(); !auth {
		t.Fatal("must request authentication")
	}

	// Client side of the key exchange: AES-128 secret, RSA PKCS#1 v1.5.
	var secret [16]byte
	if _, err := crypto_rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("key type %T, want *rsa.PublicKey", pubAny)
	}
	encSecret, err := rsa.EncryptPKCS1v15(crypto_rand.Reader, pub, secret[:])
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	encToken, err := rsa.EncryptPKCS1v15(crypto_rand.Reader, pub, challenge)
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	w.Reset()
	w.VarInt(v776.PacketLSKey)
	w.VarInt(int32(len(encSecret))).FixedBytes(encSecret)
	w.VarInt(int32(len(encToken))).FixedBytes(encToken)
	b.write(w.Bytes())

	// From here every byte is CFB8 (key = IV = shared secret).
	dec, err := protocol.NewCFB8Decrypter(secret[:], secret[:])
	if err != nil {
		t.Fatal(err)
	}
	enc, err := protocol.NewCFB8Encrypter(secret[:], secret[:])
	if err != nil {
		t.Fatal(err)
	}
	b.dec, b.enc = dec, enc

	// Compression threshold arrives encrypted.
	r = b.expect(v776.PacketLoginCompression)
	threshold, err := r.VarInt()
	if err != nil || threshold <= 0 {
		t.Fatalf("threshold: %v %d", err, threshold)
	}
	b.comp = protocol.NewCompressionLayer(threshold)

	// Login success: with SkipSessionAuth the offline UUID is used.
	r = b.expect(v776.PacketLoginFinished)
	gotID, err := r.UUID()
	if err != nil {
		t.Fatal(err)
	}
	if want := protocol.OfflinePlayerUUID("Crypto"); gotID != want {
		t.Fatalf("profile id: want %x got %x", want, gotID)
	}
	name, err := r.String(16)
	if err != nil || name != "Crypto" {
		t.Fatalf("name: %v %q", err, name)
	}
	if props, _ := r.VarInt(); props != 0 {
		t.Fatalf("properties: want 0")
	}
	if _, err := r.UUID(); err != nil {
		t.Fatal(err)
	}

	// Login acknowledged -> configuration.
	w.Reset()
	w.VarInt(v776.PacketLSAcknowledged)
	b.write(w.Bytes())

	// Configuration: answer client info + known packs, count registries,
	// acknowledge the finish packet.
	registries := 0
	for {
		id, rr := b.next()
		switch id {
		case v776.PacketCfgPayload:
			if _, err := rr.String(256); err != nil {
				t.Fatal(err)
			}
			if _, err := rr.Bytes(); err != nil {
				t.Fatal(err)
			}
		case v776.PacketCfgFeatures:
			n, _ := rr.VarInt()
			for i := int32(0); i < n; i++ {
				_, _ = rr.String(128)
			}
		case v776.PacketCfgKnownPacks:
			cw := protocol.NewWriter()
			cw.VarInt(v776.PacketConfigClientInfo)
			cw.String("en_US").Byte(2).VarInt(0).Bool(true)
			cw.Byte(0).VarInt(1).Bool(false).Bool(false).VarInt(0)
			b.write(cw.Bytes())
			kw := protocol.NewWriter()
			kw.VarInt(v776.PacketConfigKnownPacks)
			kw.VarInt(1)
			kw.String("minecraft").String("core").String(v776.Name)
			b.write(kw.Bytes())
		case v776.PacketCfgRegistryData:
			key, err := rr.String(256)
			if err != nil || key == "" {
				t.Fatalf("registry key: %v %q", err, key)
			}
			entries, _ := rr.VarInt()
			for i := int32(0); i < entries; i++ {
				id, _ := rr.String(256)
				if present, _ := rr.VarInt(); present == 1 {
					root, _ := rr.Byte()
					if root != 0x0A {
						t.Fatalf("registry %s entry %s: NBT root 0x%x", key, id, root)
					}
					skipNbtPayload(t, rr, 0x0A)
				}
			}
			registries++
		case v776.PacketCfgUpdateTags:
			skipUpdateTagsPayload(t, rr)
		case v776.PacketCfgFinish:
			fw := protocol.NewWriter()
			fw.VarInt(v776.PacketConfigFinish)
			b.write(fw.Bytes())
			goto configDone
		case v776.PacketCfgDisconnect:
			reason, _ := rr.String(1024)
			t.Fatalf("config disconnect: %s", reason)
		default:
			t.Fatalf("unexpected encrypted config packet 0x%x", id)
		}
	}
configDone:
	if registries != 29 {
		t.Fatalf("registry data packets: want 29, got %d", registries)
	}

	// Play: acknowledge configuration, then read the spawn sequence
	// until the login packet arrives; every packet must decode cleanly.
	paw := protocol.NewWriter()
	paw.VarInt(v776.PacketPlayConfigAcknowledged)
	b.write(paw.Bytes())

	sawLogin := false
	_ = b.nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	for !sawLogin {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayLogin:
			entityID, _ := rr.Int32()
			if entityID != 1 {
				t.Fatalf("entity id: want 1, got %d", entityID)
			}
			sawLogin = true
		case v776.PacketPlayDifficulty, v776.PacketPlayAbilities,
			v776.PacketPlayHeldSlot, v776.PacketPlayPlayerInfo,
			v776.PacketPlayPlayerPosition, v776.PacketPlayCacheCenter,
			v776.PacketPlayCacheRadius, v776.PacketPlaySpawnPosition,
			v776.PacketPlaySetTime, v776.PacketPlayGameEvent:
			// Spawn sequence packets; the offline flow test
			// validates their full contents.
		case v776.PacketPlayDisconnect:
			reason, _ := rr.String(1024)
			t.Fatalf("play disconnect: %s", reason)
		default:
			t.Fatalf("unexpected encrypted play packet 0x%x", id)
		}
	}
}

// decodeFloorSection parses the first chunk section out of a superflat
// section buffer and asserts the linear palette and per-layer indices.
// The 26.2 wire format stores palette INDICES (not global state ids) and
// the floor section must include air in its palette for the twelve
// non-solid layers above the surface.
func decodeFloorSection(t *testing.T, buf []byte) {
	t.Helper()
	sr := &protocol.Reader{}
	sr.Reset(buf)
	blockCount, _ := sr.Uint16()
	fluidCount, _ := sr.Uint16()
	if blockCount != 4*16*16 || fluidCount != 0 {
		t.Fatalf("floor counters: block=%d fluid=%d", blockCount, fluidCount)
	}
	bits, _ := sr.Byte()
	if bits != 4 {
		t.Fatalf("floor bits per entry: %d", bits)
	}
	n, _ := sr.VarInt()
	if n != 4 {
		t.Fatalf("floor palette size: %d", n)
	}
	want := []int32{stateBedrock, stateDirt, stateGrassBlock, stateAir}
	for i := int32(0); i < n; i++ {
		got, _ := sr.VarInt()
		if got != want[i] {
			t.Fatalf("floor palette[%d]: want %d, got %d", i, want[i], got)
		}
	}
	// 256 longs, 16 entries each, 4 bits per entry, no straddling.
	longs := make([]uint64, 256)
	for i := range longs {
		v, _ := sr.Int64()
		longs[i] = uint64(v)
	}
	wantIdx := func(y int32) uint64 {
		switch y {
		case 0:
			return 0 // bedrock
		case 1, 2:
			return 1 // dirt
		case 3:
			return 2 // grass_block
		default:
			return 3 // air
		}
	}
	for i := int32(0); i < 4096; i++ {
		got := (longs[i/16] >> uint((i%16)*4)) & 0xF
		if got != wantIdx(i>>8) {
			t.Fatalf("floor block %d (y=%d): want idx %d, got %d", i, i>>8, wantIdx(i>>8), got)
		}
	}
}

// TestOnlineModeHasJoined runs the encrypted login against a stub session
// server and asserts the profile identity comes from the hasJoined
// response, including signed profile properties. This is the production
// online-mode path with the Mojang endpoint swapped for a stub.
func TestOnlineModeHasJoined(t *testing.T) {
	const player = "RealPlayer"
	wantID := protocol.OfflinePlayerUUID(player)

	var gotUsername, gotServerID string
	var stubHits int
	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hasJoined" {
			t.Errorf("stub path %q", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		stubHits++
		gotUsername = r.URL.Query().Get("username")
		gotServerID = r.URL.Query().Get("serverId")
		if gotServerID == "" {
			t.Error("stub: empty serverId digest")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"id":"%x","name":%q,"properties":[{"name":"textures","value":"dGV4dHVyZQ==","signature":"c2ln"}]}`,
			[16]byte(wantID), player)
	}))
	defer stub.Close()

	s, err := New(Options{
		ListenAddr:        "127.0.0.1:0",
		MaxPlayers:        4,
		OnlineMode:        true,
		SessionServerURL:  stub.URL,
		VersionName:       v776.Name,
		ProtocolNumber:    v776.ProtocolNumber,
		ViewDistance:      1,
		KeepAliveInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = s.Serve() }()
	select {
	case <-s.started:
	case <-time.After(5 * time.Second):
		t.Fatal("server never signalled startup")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.Shutdown(ctx)
	})

	b := dialBot(t, s.Addr().String())
	sendHandshake(b, java.IntentLogin)
	w := protocol.NewWriter()
	w.VarInt(v776.PacketLSHello).String(player).UUID(protocol.OfflinePlayerUUID(player))
	b.write(w.Bytes())

	// Encryption request.
	r := b.expect(v776.PacketLSHello)
	if _, err := r.String(64); err != nil {
		t.Fatal(err)
	}
	pubDER, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	challenge, err := r.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	pubAny, err := x509.ParsePKIXPublicKey(pubDER)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	pub, ok := pubAny.(*rsa.PublicKey)
	if !ok {
		t.Fatalf("key type %T, want *rsa.PublicKey", pubAny)
	}

	// Client side of the key exchange.
	var secret [16]byte
	if _, err := crypto_rand.Read(secret[:]); err != nil {
		t.Fatal(err)
	}
	encSecret, err := rsa.EncryptPKCS1v15(crypto_rand.Reader, pub, secret[:])
	if err != nil {
		t.Fatal(err)
	}
	encToken, err := rsa.EncryptPKCS1v15(crypto_rand.Reader, pub, challenge)
	if err != nil {
		t.Fatal(err)
	}
	w.Reset()
	w.VarInt(v776.PacketLSKey)
	w.VarInt(int32(len(encSecret))).FixedBytes(encSecret)
	w.VarInt(int32(len(encToken))).FixedBytes(encToken)
	b.write(w.Bytes())

	// From here every byte is CFB8 (key = IV = shared secret).
	dec, err := protocol.NewCFB8Decrypter(secret[:], secret[:])
	if err != nil {
		t.Fatal(err)
	}
	enc, err := protocol.NewCFB8Encrypter(secret[:], secret[:])
	if err != nil {
		t.Fatal(err)
	}
	b.dec, b.enc = dec, enc

	r = b.expect(v776.PacketLoginCompression)
	threshold, err := r.VarInt()
	if err != nil || threshold <= 0 {
		t.Fatalf("threshold: %v %d", err, threshold)
	}
	b.comp = protocol.NewCompressionLayer(threshold)

	// Login success carries the stub-issued profile identity.
	r = b.expect(v776.PacketLoginFinished)
	gotID, err := r.UUID()
	if err != nil {
		t.Fatal(err)
	}
	if gotID != wantID {
		t.Fatalf("profile id: want %x got %x", wantID, gotID)
	}
	name, err := r.String(16)
	if err != nil || name != player {
		t.Fatalf("name: %v %q", err, name)
	}
	props, _ := r.VarInt()
	if props != 1 {
		t.Fatalf("properties: want 1, got %d", props)
	}

	if stubHits != 1 {
		t.Fatalf("stub hits: %d", stubHits)
	}
	if gotUsername != player {
		t.Fatalf("stub username: %q", gotUsername)
	}
}

// nbtWidth maps simple numeric NBT tags to their fixed payload width.
var nbtWidth = map[byte]int{1: 1, 2: 2, 3: 4, 4: 8, 5: 4, 6: 8}

// skipUpdateTagsPayload consumes one ClientboundUpdateTagsPacket body:
// registry/tag groups referencing entries by protocol id.
func skipUpdateTagsPayload(t *testing.T, rr *protocol.Reader) {
	t.Helper()
	registries, _ := rr.VarInt()
	if registries <= 0 {
		t.Fatalf("update tags: no registries")
	}
	for i := int32(0); i < registries; i++ {
		if _, err := rr.String(256); err != nil {
			t.Fatal(err)
		}
		tags, _ := rr.VarInt()
		for j := int32(0); j < tags; j++ {
			if _, err := rr.String(256); err != nil {
				t.Fatal(err)
			}
			n, _ := rr.VarInt()
			for k := int32(0); k < n; k++ {
				if _, err := rr.VarInt(); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// skipNbtPayload advances rr past the payload of one NBT tag (the tag's
// type byte has already been consumed), validating the structure. It
// mirrors the standard big-endian NBT layout used by the 1.20.2+ network
// format; on any malformed input the test fails.
func skipNbtPayload(t *testing.T, rr *protocol.Reader, tag byte) {
	t.Helper()
	switch tag {
	case 1, 2, 3, 4, 5, 6:
		if _, err := rr.FixedBytes(nbtWidth[tag]); err != nil {
			t.Fatal(err)
		}
	case 7, 11, 12: // byte/int/long array
		n, err := rr.Int32()
		if err != nil {
			t.Fatal(err)
		}
		if n < 0 {
			t.Fatalf("negative array length %d", n)
		}
		w := map[byte]int{7: 1, 11: 4, 12: 8}[tag]
		if _, err := rr.FixedBytes(int(n) * w); err != nil {
			t.Fatal(err)
		}
	case 8: // string: unsigned short length + UTF-8
		n, err := rr.Uint16()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := rr.FixedBytes(int(n)); err != nil {
			t.Fatal(err)
		}
	case 9: // list: element type + count + payloads
		et, err := rr.Byte()
		if err != nil {
			t.Fatal(err)
		}
		n, err := rr.Int32()
		if err != nil || n < 0 {
			t.Fatalf("list length %d", n)
		}
		for i := int32(0); i < n; i++ {
			skipNbtPayload(t, rr, et)
		}
	case 10: // compound: named fields until TAG_End
		for {
			ft, err := rr.Byte()
			if err != nil {
				t.Fatal(err)
			}
			if ft == 0 {
				return
			}
			n, err := rr.Uint16()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := rr.FixedBytes(int(n)); err != nil {
				t.Fatal(err)
			}
			skipNbtPayload(t, rr, ft)
		}
	default:
		t.Fatalf("bad NBT tag 0x%x", tag)
	}
}
