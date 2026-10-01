package server

import (
	"bytes"
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// sendCommand submits a serverbound chat command (26.2 wire form: a bare
// string, no signature tail).
func sendCommand(b *botConn, line string) {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlaySBChatCommand)
	w.String(line)
	b.write(w.Bytes())
}

// readBossEvent decodes the shared UUID + operation prefix.
func readBossEvent(t *testing.T, rr *protocol.Reader) ([16]byte, int32) {
	t.Helper()
	var id [16]byte
	raw, err := rr.FixedBytes(16)
	if err != nil {
		t.Fatal(err)
	}
	copy(id[:], raw)
	op, err := rr.VarInt()
	if err != nil {
		t.Fatal(err)
	}
	return id, op
}

// TestTpsbarToggle walks /tpsbar on→off and checks the boss event ADD and
// REMOVE operations plus the chat feedback in between.
func TestTpsbarToggle(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Observer")

	var barID [16]byte
	sendCommand(b, "tpsbar")

	b.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	added, feedback := false, false
	for !(added && feedback) {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayBossEvent:
			gotID, op := readBossEvent(t, rr)
			if op != java.BossOpAdd {
				t.Fatalf("first boss event op %d, want ADD(%d)", op, java.BossOpAdd)
			}
			// Title NBT component, then progress/color/overlay/flags.
			root, err := rr.Byte()
			if err != nil || root != 0x0A {
				t.Fatalf("boss title NBT root 0x%x", root)
			}
			skipNbtPayload(t, rr, 0x0A)
			if _, err := rr.Float(); err != nil {
				t.Fatal(err)
			}
			color, _ := rr.VarInt()
			overlay, _ := rr.VarInt()
			if color != java.BossColorGreen {
				t.Fatalf("fresh server color %d, want green", color)
			}
			if overlay != java.BossOverlayNotched10 {
				t.Fatalf("overlay %d, want notched_10", overlay)
			}
			barID = gotID
			added = true
		case v776.PacketPlaySystemChat:
			root, _ := rr.Byte()
			if root != 0x0A {
				t.Fatalf("system chat root 0x%x", root)
			}
			skipNbtPayload(t, rr, 0x0A)
			if overlay, _ := rr.Bool(); overlay {
				t.Fatal("system chat overlay flag set")
			}
			feedback = true
		case v776.PacketPlayCBKeepAlive:
			continue
		default:
			t.Fatalf("unexpected packet 0x%x after /tpsbar", id)
		}
	}

	// Let the ticker refresh the bar at least once (1 Hz cadence): expect
	// UPDATE_NAME / UPDATE_PROGRESS / UPDATE_STYLE operations.
	deadline := time.Now().Add(3 * time.Second)
	ops := map[int32]bool{}
	b.nc.SetReadDeadline(deadline)
	for !(ops[java.BossOpUpdateName] && ops[java.BossOpUpdateProgress] && ops[java.BossOpUpdateStyle]) {
		if time.Now().After(deadline) {
			t.Fatal("no 1 Hz boss bar refresh within 3s")
		}
		id, rr := b.next()
		switch id {
		case v776.PacketPlayBossEvent:
			gotID, op := readBossEvent(t, rr)
			if gotID != barID {
				t.Fatalf("refresh bar id %v, want %v", gotID, barID)
			}
			switch op {
			case java.BossOpUpdateName:
				root, _ := rr.Byte()
				if root != 0x0A {
					t.Fatalf("refresh title root 0x%x", root)
				}
				skipNbtPayload(t, rr, 0x0A)
			case java.BossOpUpdateProgress:
				if _, err := rr.Float(); err != nil {
					t.Fatal(err)
				}
			case java.BossOpUpdateStyle:
				if _, err := rr.VarInt(); err != nil {
					t.Fatal(err)
				}
				if _, err := rr.VarInt(); err != nil {
					t.Fatal(err)
				}
			default:
				t.Fatalf("unexpected refresh op %d", op)
			}
			ops[op] = true
		case v776.PacketPlayCBKeepAlive:
			continue
		}
	}

	// Toggle off: REMOVE with the same bar UUID.
	sendCommand(b, "tpsbar")
	b.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayBossEvent:
			gotID, op := readBossEvent(t, rr)
			if op != java.BossOpRemove {
				t.Fatalf("second toggle op %d, want REMOVE(%d)", op, java.BossOpRemove)
			}
			if gotID != barID {
				t.Fatalf("remove id %v, want %v", gotID, barID)
			}
			return
		case v776.PacketPlayCBKeepAlive:
			continue
		}
	}
}

// TestTpsAndUnknownCommands checks the text readout and the unknown
// command feedback path.
func TestTpsAndUnknownCommands(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Watcher")

	var gotTPS, gotUnknown bool
	sendCommand(b, "tps")
	sendCommand(b, "definitely_not_a_command")

	b.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	for !(gotTPS && gotUnknown) {
		id, rr := b.next()
		switch id {
		case v776.PacketPlaySystemChat:
			root, _ := rr.Byte()
			if root != 0x0A {
				t.Fatalf("system chat root 0x%x", root)
			}
			// Pull the text payload out: TAG_String field = type byte,
			// u16-length name ("text"), u16-length value.
			et, _ := rr.Byte()
			if et != 0x08 {
				t.Fatalf("expected string field, got 0x%x", et)
			}
			nameLen, _ := rr.Uint16()
			if _, err := rr.FixedBytes(int(nameLen)); err != nil {
				t.Fatal(err)
			}
			n, _ := rr.Uint16()
			raw, _ := rr.FixedBytes(int(n))
			_, _ = rr.Bool()
			switch {
			case bytes.Contains(raw, []byte("TPS")):
				gotTPS = true
			case bytes.Contains(raw, []byte("未知命令")):
				gotUnknown = true
			}
		case v776.PacketPlayCBKeepAlive:
			continue
		}
	}
	if !gotTPS || !gotUnknown {
		t.Fatalf("tps=%v unknown=%v", gotTPS, gotUnknown)
	}
}

// TestTickStatsSanity records synthetic tick durations and checks the
// snapshot math (MSPT average, TPS capped at 20).
func TestTickStatsSanity(t *testing.T) {
	var st tickStats
	base := time.Now()
	for i := 0; i < 100; i++ {
		st.record(25*time.Millisecond, base.Add(time.Duration(i)*50*time.Millisecond))
	}
	mspt, t1, t5, t15 := st.snapshotAt(base.Add(5 * time.Second))
	if mspt < 24 || mspt > 26 {
		t.Fatalf("mspt %f, want ~25", mspt)
	}
	// 100 ticks over 5 seconds → ~20 TPS everywhere (partial window caps).
	if t1 < 19 || t1 > 20.01 || t5 < 19 || t15 < 19 {
		t.Fatalf("tps windows 1m=%f 5m=%f 15m=%f", t1, t5, t15)
	}

	// Lagged ticks drag the TPS down.
	var lag tickStats
	for i := 0; i < 400; i++ {
		lag.record(120*time.Millisecond, base.Add(time.Duration(i)*120*time.Millisecond))
	}
	_, l1, _, _ := lag.snapshotAt(base.Add(50 * time.Second))
	if l1 > 10 {
		t.Fatalf("laggy server tps1m %f, want <=10", l1)
	}
}

// TestBlockPlacement uses the starter hotbar: slot 0 (stone) against the
// grass surface top face → a stone block appears at (0,-60,0).
func TestBlockPlacement(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Builder")

	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayUseItemOn)
	w.VarInt(0) // main hand
	java.WriteBlockPos(w, 0, -61, 0)
	w.VarInt(1) // face: up
	w.Float(0.5).Float(1).Float(0.5)
	w.Bool(false) // not inside block
	w.Bool(false) // no world border hit
	w.VarInt(21)  // sequence
	b.write(w.Bytes())

	b.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	acked := false
	for {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayBlockChangedAck:
			got, _ := rr.VarInt()
			if got != 21 {
				t.Fatalf("ack %d, want 21", got)
			}
			acked = true
		case v776.PacketPlayBlockUpdate:
			x, y, z, _ := java.ReadBlockPos(rr)
			state, _ := rr.VarInt()
			if !acked {
				t.Fatal("block update before ack")
			}
			if x != 0 || y != -60 || z != 0 {
				t.Fatalf("placed at (%d,%d,%d)", x, y, z)
			}
			if want := defaultStateOf("minecraft:stone"); int(state) != want {
				t.Fatalf("state %d, want stone %d", state, want)
			}
			return
		case v776.PacketPlayCBKeepAlive:
			continue
		}
	}
}

// TestPlacementOccupied verifies no overwrite: pre-fill the target cell,
// click up onto the block below, and expect the ack but no Block Update
// and an unchanged world.
func TestPlacementOccupied(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Packer")

	cobble := int32(defaultStateOf("minecraft:cobblestone"))
	if got := s.world.getBlock(0, -60, 0); got != stateAir {
		t.Fatalf("pre: (0,-60,0) = %d", got)
	}
	s.world.setBlock(0, -60, 0, cobble)

	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayUseItemOn)
	w.VarInt(0)
	java.WriteBlockPos(w, 0, -61, 0)
	w.VarInt(1)
	w.Float(0.5).Float(1).Float(0.5)
	w.Bool(false)
	w.Bool(false)
	w.VarInt(5)
	b.write(w.Bytes())

	// Expect the ack, then prove no Block Update follows within a short
	// window and the world still holds the pre-filled cobblestone.
	b.nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		id, rr := b.next()
		if id == v776.PacketPlayBlockChangedAck {
			if _, err := rr.VarInt(); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	// Drain any trailing packets for a moment; a Block Update here is a
	// regression (placement must not overwrite occupied cells).
	b.nc.SetReadDeadline(time.Now().Add(700 * time.Millisecond))
	for {
		payload, err := b.fr.Next()
		if err != nil {
			break // timeout: no packet arrived, good
		}
		dec, err := b.comp.DecompressFrame(payload, protocol.DefaultMaxPacketLen)
		if err != nil {
			break
		}
		r := protocol.NewReader(dec)
		id, _ := r.VarInt()
		if id == v776.PacketPlayBlockUpdate {
			t.Fatal("placement must not overwrite an occupied cell")
		}
	}
	if got := s.world.getBlock(0, -60, 0); got != cobble {
		t.Fatalf("target changed to %d", got)
	}
}
