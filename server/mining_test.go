package server

import (
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// skipRegistryPayload consumes one ClientboundRegistryData packet body.
func skipRegistryPayload(t *testing.T, rr *protocol.Reader) {
	t.Helper()
	if _, err := rr.String(256); err != nil {
		t.Fatal(err)
	}
	entries, _ := rr.VarInt()
	for i := int32(0); i < entries; i++ {
		if _, err := rr.String(256); err != nil {
			t.Fatal(err)
		}
		present, _ := rr.VarInt()
		if present == 1 {
			if root, _ := rr.Byte(); root != 0x0A {
				t.Fatalf("NBT root 0x%x", root)
			}
			skipNbtPayload(t, rr, 0x0A)
		}
	}
}

// joinBotToPlay walks the offline login + configuration + play setup path
// and returns once the initial chunk batch has been streamed.
func joinBotToPlay(t *testing.T, s *Server, name string) *botConn {
	return joinBotToPlayVD(t, s, name, 2)
}

// joinBotToPlayVD joins with an explicit client view distance so latency
// tests can exercise the production chunk radius.
func joinBotToPlayVD(t *testing.T, s *Server, name string, viewDistance byte) *botConn {
	t.Helper()
	b := dialBot(t, s.Addr().String())

	sendHandshake(b, java.IntentLogin)
	w := protocol.NewWriter()
	w.VarInt(v776.PacketLSHello).String(name).UUID(protocol.OfflinePlayerUUID(name))
	b.write(w.Bytes())

	r := b.expect(v776.PacketLoginCompression)
	threshold, err := r.VarInt()
	if err != nil {
		t.Fatal(err)
	}
	b.comp = protocol.NewCompressionLayer(threshold)

	b.expect(v776.PacketLoginFinished)
	w.Reset()
	w.VarInt(v776.PacketLSAcknowledged)
	b.write(w.Bytes())

	for {
		id, rr := b.next()
		switch id {
		case v776.PacketCfgPayload:
			_, _ = rr.String(256)
			_, _ = rr.Bytes()
		case v776.PacketCfgFeatures:
			n, _ := rr.VarInt()
			for i := int32(0); i < n; i++ {
				_, _ = rr.String(128)
			}
		case v776.PacketCfgKnownPacks:
			n, _ := rr.VarInt()
			for i := int32(0); i < n; i++ {
				_, _ = rr.String(256)
				_, _ = rr.String(256)
				_, _ = rr.String(256)
			}
			cw := protocol.NewWriter()
			cw.VarInt(v776.PacketConfigClientInfo)
			cw.String("en_US").Byte(viewDistance).VarInt(0).Bool(true)
			cw.Byte(0).VarInt(1).Bool(false).Bool(false).VarInt(0)
			b.write(cw.Bytes())
			kw := protocol.NewWriter()
			kw.VarInt(v776.PacketConfigKnownPacks)
			kw.VarInt(1)
			kw.String("minecraft").String("core").String(v776.Name)
			b.write(kw.Bytes())
		case v776.PacketCfgRegistryData:
			skipRegistryPayload(t, rr)
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
		}
	}
configDone:
	// Switch into play, then announce the player loaded to trigger the
	// spawn chunk batch.
	paw := protocol.NewWriter()
	paw.VarInt(v776.PacketPlayConfigAcknowledged)
	b.write(paw.Bytes())
	pl := protocol.NewWriter()
	pl.VarInt(v776.PacketPlayPlayerLoaded)
	b.write(pl.Bytes())

	// Drain the spawn sequence + chunk batch until Batch Done.
	b.nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	for {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayCBChunkBatchDone:
			if _, err := rr.VarInt(); err != nil {
				t.Fatal(err)
			}
			return b
		case v776.PacketPlayCBKeepAlive, v776.PacketPlayLogin,
			v776.PacketPlayDifficulty, v776.PacketPlayAbilities,
			v776.PacketPlayHeldSlot, v776.PacketPlayPlayerInfo,
			v776.PacketPlayPlayerPosition, v776.PacketPlayCacheCenter,
			v776.PacketPlayCacheRadius, v776.PacketPlaySpawnPosition,
			v776.PacketPlaySetTime, v776.PacketPlayGameEvent,
			v776.PacketPlaySetPlayerInv, v776.PacketPlayContainerContent,
			v776.PacketPlayCommands,
			v776.PacketPlayContainerSetSlot, v776.PacketPlaySetCursorItem,
			v776.PacketPlayOpenScreen, v776.PacketPlayCBContainerClose,
			v776.PacketPlayLevelChunk, v776.PacketPlayChunkBatchStart,
			// M8 vitals, sent between the held slot and the
			// starter inventory during the spawn sequence.
			v776.PacketPlaySetHealth, v776.PacketPlaySetExperience,
			v776.PacketPlayUpdateAttributes,
			// M8 mobs stream as soon as the player is tracked; the drain
			// phase sees the starter herd's packets.
			v776.PacketPlayAddEntity, v776.PacketPlaySetEntityMotion,
			v776.PacketPlayMoveEntityPos, v776.PacketPlayMoveEntityPosRot,
			v776.PacketPlayRotateHead, v776.PacketPlayEntityEvent:
			continue
		default:
			t.Fatalf("unexpected packet 0x%x while joining", id)
		}
	}
}

// TestMiningFlow digs the grass surface and expects the sequence ack and
// the Block Update once vanilla's bare-hand damage formula completes.
func TestMiningFlow(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Miner")

	const seq = int32(7)
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayPlayerAction)
	w.VarInt(int32(java.ActionStartDestroy))
	java.WriteBlockPos(w, 0, -61, 0) // grass surface at spawn
	w.VarInt(0)                      // face: bottom
	w.VarInt(seq)
	b.write(w.Bytes())

	b.nc.SetReadDeadline(time.Now().Add(8 * time.Second))
	acked := false
	deadline := time.Now().Add(8 * time.Second)
	_ = deadline
	for {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayBlockChangedAck:
			got, err := rr.VarInt()
			if err != nil {
				t.Fatal(err)
			}
			if got != seq {
				t.Fatalf("ack sequence %d, want %d", got, seq)
			}
			acked = true
		case v776.PacketPlayCBKeepAlive:
			continue
		case v776.PacketPlayBlockUpdate:
			x, y, z, err := java.ReadBlockPos(rr)
			if err != nil {
				t.Fatal(err)
			}
			state, _ := rr.VarInt()
			if !acked {
				t.Fatal("block update arrived before the ack")
			}
			if x != 0 || y != -61 || z != 0 {
				t.Fatalf("block update at (%d,%d,%d)", x, y, z)
			}
			if state != stateAir {
				t.Fatalf("dug state %d, want air", state)
			}
			if got := s.world.getBlock(0, -61, 0); got != stateAir {
				t.Fatalf("world block after dig: %d", got)
			}
			return
		}
	}
}

// TestMiningUnbreakable verifies that bedrock never yields to digging.
func TestMiningUnbreakable(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "BedrockFan")

	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayPlayerAction)
	w.VarInt(int32(java.ActionStartDestroy))
	java.WriteBlockPos(w, 0, -64, 0)
	w.VarInt(0)
	w.VarInt(3)
	b.write(w.Bytes())

	// The ack must arrive; give the ticker time to (wrongly) break it.
	b.nc.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		id, rr := b.next()
		if id == v776.PacketPlayBlockChangedAck {
			if _, err := rr.VarInt(); err != nil {
				t.Fatal(err)
			}
			break
		}
		if id == v776.PacketPlayBlockUpdate {
			t.Fatal("bedrock must not break")
		}
	}
	time.Sleep(300 * time.Millisecond)
	if got := s.world.getBlock(0, -64, 0); got != stateBedrock {
		t.Fatalf("bedrock became state %d", got)
	}
}

// TestMoveStreamsAndForgetsChunks walks the bot across a chunk border and
// expects the forget + batch flow.
func TestMoveStreamsAndForgetsChunks(t *testing.T) {
	s := startTestServer(t)
	b := joinBotToPlay(t, s, "Walker")

	// Walk 16.5 blocks east: moves into chunk (1,0), so the column at
	// x=-2 (5 chunks, radius 2) falls out of the radius and 5 new chunks
	// at x=3 stream in.
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayMovePos)
	w.Double(16.5).Double(-60).Double(0.5)
	w.Bool(true)  // on ground
	w.Bool(false) // horizontal collision
	b.write(w.Bytes())

	sent, forgotten := 0, 0
	b.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	for forgotten < 5 || sent < 5 {
		id, rr := b.next()
		switch id {
		case v776.PacketPlayLevelChunk:
			sent++
		case v776.PacketPlayForgetChunk:
			// Z first, then X.
			cz, _ := rr.Int32()
			cx, _ := rr.Int32()
			if cx != -2 || cz < -2 || cz > 2 {
				t.Fatalf("forgotten chunk (%d,%d)", cx, cz)
			}
			forgotten++
		case v776.PacketPlayCBKeepAlive:
			continue
		}
	}
	if sent != 5 || forgotten != 5 {
		t.Fatalf("after 1-chunk walk: sent %d (want 5), forgotten %d (want 5)", sent, forgotten)
	}
}
