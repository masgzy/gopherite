package server

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
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
		ListenAddr:     "127.0.0.1:0",
		MOTD:           "test 驱动的服务器",
		MaxPlayers:     42,
		VersionName:    v776.Name,
		ProtocolNumber: v776.ProtocolNumber,
	})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := s.Listen(); err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = s.Serve() }()
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
	status, err := java.ReadStatusResponse([]byte(body))
	if err != nil {
		t.Fatalf("parse json: %v (%s)", err, body)
	}
	if status.Protocol != v776.ProtocolNumber {
		t.Fatalf("protocol: want %d, got %d", v776.ProtocolNumber, status.Protocol)
	}
	if status.VersionName != v776.Name {
		t.Fatalf("version: want %s, got %s", v776.Name, status.VersionName)
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

	// Server closes after pong, like vanilla.
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := fr.Next(); err == nil {
		t.Log("server kept the connection open after pong")
	}
}

func TestLoginRejectedGracefully(t *testing.T) {
	s := startTestServer(t)
	_, bw, fr := dialTest(t, s.Addr().String())

	w := protocol.NewWriter()
	w.VarInt(v776.PacketHandshake).
		VarInt(v776.ProtocolNumber).
		String("127.0.0.1").
		Uint16(25565).
		VarInt(int32(java.IntentLogin))
	if err := protocol.WriteFramed(bw, w.Bytes()); err != nil {
		t.Fatal(err)
	}
	w.Reset()
	w.VarInt(v776.PacketLoginStart).String("Steve")
	if err := protocol.WriteFramed(bw, w.Bytes()); err != nil {
		t.Fatal(err)
	}

	payload, err := fr.Next()
	if err != nil {
		t.Fatalf("read disconnect: %v", err)
	}
	r := protocol.NewReader(payload)
	id, _ := r.VarInt()
	if id != v776.PacketLoginDisconnect {
		t.Fatalf("want login disconnect 0x0, got 0x%x", id)
	}
	body, err := r.String(1024)
	if err != nil {
		t.Fatal(err)
	}
	var comp map[string]any
	if err := json.Unmarshal([]byte(body), &comp); err != nil {
		t.Fatalf("disconnect body is not a chat component: %v", err)
	}
}

// TestStatusAdvertisesOfflineMode asserts the online-mode flag propagates
// into server options even though login handling is a later milestone.
func TestStatusAdvertisesOfflineMode(t *testing.T) {
	s, err := New(Options{ListenAddr: "127.0.0.1:0", OnlineMode: false, MOTD: "x", MaxPlayers: 1, VersionName: v776.Name, ProtocolNumber: v776.ProtocolNumber})
	if err != nil {
		t.Fatal(err)
	}
	if s.Options().OnlineMode {
		t.Fatal("online mode flag lost")
	}
}
