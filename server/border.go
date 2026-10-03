package server

// M16 world border: the vanilla WorldBorder model (static/moving
// extents, distance math, out-of-border damage) plus its packets,
// commands and world_border.dat persistence. All state is guarded by
// Server.mu — mutators run from command handlers or the tick loop.
//
// Behaviour verified against the 26.2 decompiled WorldBorder,
// WorldBorderCommand, LivingEntity (out-of-border damage) and
// PlayerList.sendLevelInfo (join packet).

import (
	"log"
	"math"
	"os"
	"path/filepath"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// Vanilla WorldBorder constants.
const (
	borderMaxSize        = 59999997.0 // MAX_SIZE (5.999997E7)
	borderMaxCenter      = 29999984.0 // MAX_CENTER_COORDINATE clamp
	borderDefaultSize    = 59999997.0 // Settings.DEFAULT size
	borderDefaultDamage  = 0.2        // damage per block past the buffer
	borderDefaultSafe    = 5.0        // damage buffer (safeZone)
	borderDefaultWarning = 5          // red-screen distance in blocks
	borderDefaultWarnT   = 300        // 26.2 Settings.DEFAULT warning time (s)
	borderAbsoluteMax    = int32(29999984)
	// borderHurtInterval is the out-of-border damage cadence in ticks:
	// vanilla calls hurtServer every baseTick and the shared invulnerability
	// window (10 ticks) paces the real hits.
	borderHurtInterval = 10
)

// worldBorder is the overworld world border. The zero value is not valid
// — use newWorldBorder.
type worldBorder struct {
	centerX, centerZ float64
	damagePerBlock   float64
	safeZone         float64
	warningTime      int32 // seconds of red flashing while shrinking
	warningBlocks    int32 // distance at which the red vignette fades in

	// Extent. static: size holds the diameter. moving: size interpolates
	// from "from" to "to" across lerpElapsed/lerpDuration ticks (vanilla
	// MovingBorderExtent with the count flipped to elapsed).
	moving       bool
	size         float64
	from, to     float64
	lerpDuration int64
	lerpElapsed  int64
	absoluteMax  int32
}

// newWorldBorder returns the vanilla default border: centered on the
// origin with the max world size (effectively invisible).
func newWorldBorder() *worldBorder {
	return &worldBorder{
		centerX:        0,
		centerZ:        0,
		damagePerBlock: borderDefaultDamage,
		safeZone:       borderDefaultSafe,
		warningTime:    borderDefaultWarnT,
		warningBlocks:  borderDefaultWarning,
		size:           borderDefaultSize,
		absoluteMax:    borderAbsoluteMax,
	}
}

// ---- geometry (callers hold s.mu) ------------------------------------------

func (b *worldBorder) minX() float64 {
	return clampD(b.centerX-b.size/2, -float64(b.absoluteMax), float64(b.absoluteMax))
}
func (b *worldBorder) maxX() float64 {
	return clampD(b.centerX+b.size/2, -float64(b.absoluteMax), float64(b.absoluteMax))
}
func (b *worldBorder) minZ() float64 {
	return clampD(b.centerZ-b.size/2, -float64(b.absoluteMax), float64(b.absoluteMax))
}
func (b *worldBorder) maxZ() float64 {
	return clampD(b.centerZ+b.size/2, -float64(b.absoluteMax), float64(b.absoluteMax))
}

func clampD(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// distanceTo is the vanilla getDistanceToBorder: the smallest gap from a
// point to any of the four edges (negative when outside).
func (b *worldBorder) distanceTo(x, z float64) float64 {
	fromNorth := z - b.minZ()
	fromSouth := b.maxZ() - z
	fromWest := x - b.minX()
	fromEast := b.maxX() - x
	m := math.Min(fromWest, fromEast)
	m = math.Min(m, fromNorth)
	return math.Min(m, fromSouth)
}

// isWithin mirrors isWithinBounds(x, z): inside the border box.
func (b *worldBorder) isWithin(x, z float64) bool {
	return x >= b.minX() && x < b.maxX() && z >= b.minZ() && z < b.maxZ()
}

// clampPoint mirrors clampVec3ToBound for the horizontal axes (max edge
// pulled in by 1e-5, matching vanilla).
func (b *worldBorder) clampPoint(x, z float64) (float64, float64) {
	return clampD(x, b.minX(), b.maxX()-1.0e-5), clampD(z, b.minZ(), b.maxZ()-1.0e-5)
}

// ---- mutation & sync (callers hold s.mu) -----------------------------------

// syncSizeLocked pushes the current diameter (vanilla
// onSetSize → SET_BORDER_SIZE with the lerp target).
func (s *Server) syncSizeLocked() {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetBorderSize)
	java.WritePlaySetBorderSize(body, s.border.to)
	s.broadcastPacketLocked(body)
}

// syncCenterLocked pushes the center change.
func (s *Server) syncCenterLocked() {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetBorderCenter)
	java.WritePlaySetBorderCenter(body, s.border.centerX, s.border.centerZ)
	s.broadcastPacketLocked(body)
}

// syncLerpLocked pushes an animated resize; the client interpolates by
// itself over lerpTime ticks.
func (s *Server) syncLerpLocked() {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetBorderLerpSize)
	java.WritePlaySetBorderLerpSize(body, s.border.from, s.border.to, s.border.lerpDuration)
	s.broadcastPacketLocked(body)
}

// syncWarningLocked pushes both warning parameters.
func (s *Server) syncWarningLocked() {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetBorderWarningDist)
	java.WritePlaySetBorderWarningDistance(body, s.border.warningBlocks)
	s.broadcastPacketLocked(body)
	body = protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetBorderWarningDelay)
	java.WritePlaySetBorderWarningDelay(body, s.border.warningTime)
	s.broadcastPacketLocked(body)
}

// sendBorderInit hands a joining player the full-state snapshot (vanilla
// sendLevelInfo starts with ClientboundInitializeBorderPacket).
func (s *Server) sendBorderInit(c *conn) {
	b := s.border
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlayInitializeBorder)
	java.WritePlayInitializeBorder(body, b.centerX, b.centerZ, b.size, b.to, b.lerpDuration, b.absoluteMax, b.warningBlocks, b.warningTime)
	_ = c.sendPacket(body.Bytes())
}

// setBorderSizeLocked applies an immediate or animated resize, mirroring
// the vanilla WorldBorderCommand.setSize validation and messages.
func (s *Server) setBorderSizeLocked(distance float64, timeTicks int64) error {
	b := s.border
	current := b.size
	if current == distance {
		return errBorder("边界大小已经是该值")
	}
	if distance < 1.0 {
		return errBorder("边界大小不能小于 1.0")
	}
	if distance > borderMaxSize {
		return errBorder("边界大小不能超过 59999997")
	}
	b.from = current
	b.to = distance
	if timeTicks > 0 {
		b.moving = true
		b.lerpDuration = timeTicks
		b.lerpElapsed = 0
		b.size = current
		s.syncLerpLocked()
	} else {
		b.moving = false
		b.size = distance
		s.syncSizeLocked()
	}
	return nil
}

// setBorderCenterLocked moves the center after a range check.
func (s *Server) setBorderCenterLocked(x, z float64) error {
	b := s.border
	if b.centerX == x && b.centerZ == z {
		return errBorder("边界中心已经是该位置")
	}
	if math.Abs(x) > borderMaxCenter || math.Abs(z) > borderMaxCenter {
		return errBorder("中心坐标越界（最大 ±29999984）")
	}
	b.centerX, b.centerZ = x, z
	s.syncCenterLocked()
	return nil
}

// ---- ticking ----------------------------------------------------------------

// tickBorder advances a moving extent by one tick, then applies the
// out-of-border damage to players (vanilla LivingEntity.baseTick paced by
// the invulnerability window).
func (s *Server) tickBorder() {
	b := s.border
	if b.moving {
		b.lerpElapsed++
		progress := float64(b.lerpElapsed) / float64(b.lerpDuration)
		if progress >= 1.0 {
			b.size = b.to
			b.moving = false
		} else {
			b.size = b.from + (b.to-b.from)*progress
		}
	}
	if s.tickCount%borderHurtInterval != 0 {
		return
	}
	for _, p := range s.players {
		if p.dead || p.gameMode != 0 {
			continue
		}
		if b.isWithin(p.x, p.z) {
			continue
		}
		// Vanilla: dist = distance + safeZone; damage when fully past it.
		dist := b.distanceTo(p.x, p.z) + b.safeZone
		if dist < 0 && b.damagePerBlock > 0 {
			amount := float32(math.Max(1, math.Floor(-dist*b.damagePerBlock)))
			s.damagePlayerLocked(p, amount, v776.DamageTypeOutsideBorder, -1, -1)
		}
	}
}

// clampMoveLocked keeps player movement inside the border, mirroring the
// vanilla collision shape: a player inside cannot walk out, one outside
// may walk back in (no snapping).
func (s *Server) clampMoveLocked(p *player, newX, newZ float64) (float64, float64) {
	b := s.border
	if b.isWithin(p.x, p.z) {
		// Inside (or on the edge): clamp the target into the box.
		return b.clampPoint(newX, newZ)
	}
	// Outside: allow everything (walking back in works; going further
	// out is dealt with by the damage tick).
	return newX, newZ
}

// ---- persistence (world/data/world_border.dat) -------------------------------

// nbtBorderName is the vanilla SavedData file name.
const nbtBorderName = "world_border"

// saveLocked serialises the border into the vanilla WorldBorder.Settings
// field layout. Caller holds s.mu.
func (s *Server) saveBorderLocked(dir string) error {
	b := s.border
	root := java.NewNbtComp()
	data := java.NewNbtComp().
		Set("BorderCenterX", java.NbtDouble(b.centerX)).
		Set("BorderCenterZ", java.NbtDouble(b.centerZ)).
		Set("BorderDamagePerBlock", java.NbtDouble(b.damagePerBlock)).
		Set("BorderSafeZone", java.NbtDouble(b.safeZone)).
		Set("BorderWarningBlocks", java.NbtInt(int64(b.warningBlocks))).
		Set("BorderWarningTime", java.NbtInt(int64(b.warningTime))).
		Set("BorderSize", java.NbtDouble(b.size)).
		Set("BorderSizeLerpTarget", java.NbtDouble(b.to)).
		Set("BorderSizeLerpTime", java.NbtLong(b.lerpDuration))
	root.Set("Data", java.NbtAny{Type: java.TagCompound, Comp: data})

	var buf protocol.Writer
	java.WriteNbtFile(&buf, root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, nbtBorderName+".dat"), zlibEncode(buf.Bytes()), 0o644)
}

// loadBorder replays world/data/world_border.dat at startup; a missing
// file leaves the vanilla default.
func (s *Server) loadBorder(dir string) {
	path := filepath.Join(dir, nbtBorderName+".dat")
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	payload, err := zlibDecode(raw)
	if err != nil {
		log.Printf(ui.Warn("警告")+" world_border.dat 解压失败: %v", err)
		return
	}
	root, err := java.ReadNbtFile(protocol.NewReader(payload))
	if err != nil {
		log.Printf(ui.Warn("警告")+" world_border.dat 解析失败: %v", err)
		return
	}
	data := root.Get("Data").Comp
	if data == nil {
		return
	}
	b := s.border
	if v := data.Get("BorderCenterX"); v.Type == java.TagDouble {
		b.centerX = v.Float
	}
	if v := data.Get("BorderCenterZ"); v.Type == java.TagDouble {
		b.centerZ = v.Float
	}
	if v := data.Get("BorderDamagePerBlock"); v.Type == java.TagDouble {
		b.damagePerBlock = v.Float
	}
	if v := data.Get("BorderSafeZone"); v.Type == java.TagDouble {
		b.safeZone = v.Float
	}
	if v := data.Get("BorderWarningBlocks"); v.Type == java.TagInt {
		b.warningBlocks = int32(v.Num)
	}
	if v := data.Get("BorderWarningTime"); v.Type == java.TagInt {
		b.warningTime = int32(v.Num)
	}
	if v := data.Get("BorderSize"); v.Type == java.TagDouble {
		b.size = v.Float
	}
	if v := data.Get("BorderSizeLerpTarget"); v.Type == java.TagDouble {
		b.to = v.Float
	}
	if v := data.Get("BorderSizeLerpTime"); v.Type == java.TagLong {
		b.lerpDuration = v.Num
	}
	// A saved moving extent resumes as a fresh lerp of the same length.
	b.from = b.size
	b.lerpElapsed = 0
	b.moving = b.lerpDuration > 0 && b.size != b.to
}

// errBorder wraps command validation failures.
func errBorder(msg string) error { return &borderError{msg} }

type borderError struct{ msg string }

func (e *borderError) Error() string { return e.msg }
