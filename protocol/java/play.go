package java

import "github.com/masgzy/gopherite/protocol"

// Play-phase packets for milestone M2: initial spawn sequence, superflat
// chunks, position sync and keep-alive. Chunk encoding details live in the
// server package (world model), while the packet field order mirrors the
// vanilla STREAM_CODEC definitions.

// CommonSpawnInfo is the shared spawn descriptor inside the play Login
// packet (vanilla CommonPlayerSpawnInfo). DimensionType is the baked
// registry reference of the dimension type: VarInt(id+1) into the
// dimension_type registry established by the configuration-phase Registry
// Data packets (overworld is entry 0 in vanilla order, so 1). The string
// field is informational.
type CommonSpawnInfo struct {
	DimensionType    string // e.g. "minecraft:overworld"
	DimensionTypeID  int32  // baked holder reference: registry index + 1
	Dimension        string // level key, e.g. "minecraft:overworld"
	Seed             int64
	GameType         byte
	PreviousGameType byte // -1 for none
	IsDebug          bool
	IsFlat           bool
	HasDeathLocation bool
	DeathDimension   string
	DeathPos         [3]int64
	PortalCooldown   int32
	SeaLevel         int32 // new in 26.2
}

// WriteCommonSpawnInfo encodes the shared spawn info block.
func WriteCommonSpawnInfo(w *protocol.Writer, s CommonSpawnInfo) {
	w.VarInt(s.DimensionTypeID) // holder reference form: id+1, 0 = direct
	w.String(s.Dimension)
	w.Int64(s.Seed)
	w.Byte(s.GameType)
	w.Byte(s.PreviousGameType)
	w.Bool(s.IsDebug)
	w.Bool(s.IsFlat)
	if !s.HasDeathLocation {
		w.Bool(false)
	} else {
		w.Bool(true).String(s.DeathDimension)
		w.Int64(s.DeathPos[0]).Int64(s.DeathPos[1]).Int64(s.DeathPos[2])
	}
	w.VarInt(s.PortalCooldown)
	w.VarInt(s.SeaLevel)
}

// ClientboundLogin is the play-phase login packet.
type ClientboundLogin struct {
	EntityID           int32
	Hardcore           bool
	Levels             []string
	MaxPlayers         int32
	ChunkRadius        int32
	SimulationRadius   int32
	ReducedDebug       bool
	ShowDeathScreen    bool
	LimitedCrafting    bool
	Spawn              CommonSpawnInfo
	OnlineMode         bool
	EnforcesSecureChat bool
}

// WritePlayLogin encodes the play login packet.
func WritePlayLogin(w *protocol.Writer, p ClientboundLogin) {
	w.Int32(p.EntityID)
	w.Bool(p.Hardcore)
	w.VarInt(int32(len(p.Levels)))
	for _, l := range p.Levels {
		w.String(l)
	}
	w.VarInt(p.MaxPlayers)
	w.VarInt(p.ChunkRadius)
	w.VarInt(p.SimulationRadius)
	w.Bool(p.ReducedDebug)
	w.Bool(p.ShowDeathScreen)
	w.Bool(p.LimitedCrafting)
	WriteCommonSpawnInfo(w, p.Spawn)
	w.Bool(p.OnlineMode)
	w.Bool(p.EnforcesSecureChat)
}

// WritePlayDifficulty encodes the difficulty packet.
func WritePlayDifficulty(w *protocol.Writer, difficulty byte, locked bool) {
	w.Byte(difficulty)
	w.Bool(locked)
}

// WritePlayAbilities encodes the player abilities packet: a bitfield plus
// the flying and walking speeds.
func WritePlayAbilities(w *protocol.Writer, invulnerable, flying, canFly, instabuild bool, flySpeed, walkSpeed float32) {
	var bits byte
	if invulnerable {
		bits |= 1
	}
	if flying {
		bits |= 2
	}
	if canFly {
		bits |= 4
	}
	if instabuild {
		bits |= 8
	}
	w.Byte(bits)
	w.Float(flySpeed)
	w.Float(walkSpeed)
}

// WritePlayHeldSlot encodes the selected hotbar slot packet.
func WritePlayHeldSlot(w *protocol.Writer, slot int32) {
	w.VarInt(slot)
}

// WritePlayPlayerInfo encodes the tab-list initialisation packet with the
// full vanilla action set (ADD_PLAYER..UPDATE_LIST_ORDER, 8 actions in
// 26.2). Entries carry only the profile and static fields; the chat
// session and display name are sent as absent.
func WritePlayPlayerInfo(w *protocol.Writer, entries []PlayerInfoEntry) {
	const actionBits byte = 0xFF // all eight actions
	w.Byte(actionBits)
	w.VarInt(int32(len(entries)))
	for _, e := range entries {
		w.UUID(e.UUID)
		// ADD_PLAYER: name string first, then the profile properties
		// (ByteBufCodecs.PLAYER_NAME + GAME_PROFILE_PROPERTIES: count
		// VarInt followed by name/value/nullable-signature triples).
		w.String(e.Name)
		w.VarInt(int32(len(e.Properties)))
		for _, prop := range e.Properties {
			w.String(prop.Name)
			w.String(prop.Value)
			if prop.Signed {
				w.Bool(true).String(prop.Signature)
			} else {
				w.Bool(false)
			}
		}
		// ADD_PLAYER done; INITIALIZE_CHAT: nullable session -> null
		w.Bool(false)
		// UPDATE_GAME_MODE
		w.VarInt(e.GameMode)
		// UPDATE_LISTED
		w.Bool(e.Listed)
		// UPDATE_LATENCY
		w.VarInt(e.Latency)
		// UPDATE_DISPLAY_NAME: nullable component -> null
		w.Bool(false)
		// UPDATE_LIST_ORDER
		w.VarInt(0)
		// UPDATE_HAT
		w.Bool(true)
	}
}

// PlayerInfoEntry is one tab-list entry.
type PlayerInfoEntry struct {
	UUID       [16]byte
	Name       string
	Properties []ProfileProperty
	GameMode   int32
	Listed     bool
	Latency    int32
}

// ClientboundPlayerPosition teleports the player. Relatives is a bitmask
// over the vanilla Relative enum (X..ROTATE_DELTA); zero means absolute.
type ClientboundPlayerPosition struct {
	TeleportID             int32
	X, Y, Z                float64
	DeltaX, DeltaY, DeltaZ float64
	Yaw, Pitch             float32
	Relatives              int32
}

// WritePlayPlayerPosition encodes the position packet.
func WritePlayPlayerPosition(w *protocol.Writer, p ClientboundPlayerPosition) {
	w.VarInt(p.TeleportID)
	w.Double(p.X).Double(p.Y).Double(p.Z)
	w.Double(p.DeltaX).Double(p.DeltaY).Double(p.DeltaZ)
	w.Float(p.Yaw).Float(p.Pitch)
	w.Int32(p.Relatives)
}

// WritePlayCacheCenter encodes the chunk cache centre packet.
func WritePlayCacheCenter(w *protocol.Writer, chunkX, chunkZ int32) {
	w.VarInt(chunkX).VarInt(chunkZ)
}

// WritePlayCacheRadius encodes the chunk cache radius packet.
func WritePlayCacheRadius(w *protocol.Writer, radius int32) {
	w.VarInt(radius)
}

// WritePlaySpawnPosition encodes the default spawn position packet
// (dimension key + packed block position + yaw + pitch).
func WritePlaySpawnPosition(w *protocol.Writer, dimension string, packedPos int64, yaw, pitch float32) {
	w.String(dimension)
	w.Int64(packedPos)
	w.Float(yaw)
	w.Float(pitch)
}

// WritePlaySetTime encodes the time packet: game time plus per-clock state
// (26.2 replaces gameTime/dayTime with named clocks).
func WritePlaySetTime(w *protocol.Writer, gameTime int64, clocks []ClockState) {
	w.Int64(gameTime)
	w.VarInt(int32(len(clocks)))
	for _, c := range clocks {
		w.VarInt(c.ClockID)
		w.VarLong(c.TotalTicks)
		w.Float(c.PartialTick)
		w.Float(c.Rate)
	}
}

// ClockState is one world clock sync entry.
type ClockState struct {
	ClockID     int32
	TotalTicks  int64
	PartialTick float32
	Rate        float32
}

// WritePlayGameEvent encodes the game event packet (event id + float arg).
func WritePlayGameEvent(w *protocol.Writer, event byte, value float32) {
	w.Byte(event)
	w.Float(value)
}

// WritePlayKeepAlive encodes the keep-alive challenge.
func WritePlayKeepAlive(w *protocol.Writer, id int64) {
	w.Int64(id)
}

// ReadPlayKeepAlive decodes the keep-alive answer.
func ReadPlayKeepAlive(r *protocol.Reader) (int64, error) {
	return r.Int64()
}

// WritePlayDisconnect encodes the play-phase disconnect packet. reason is
// a translation key; the wire format is an NBT text component.
func WritePlayDisconnect(w *protocol.Writer, reason string) {
	WriteTranslateComponent(w, reason)
}

// ServerboundMovePlayer is the decoded player movement packet.
type ServerboundMovePlayer struct {
	X, Y, Z             float64
	Yaw, Pitch          float32
	OnGround            bool
	HorizontalCollision bool
	HasPos              bool
	HasRot              bool
}

// Play move packet ids, re-declared here for ReadPlayMove clarity.
const (
	MoveIDPos    = 0x1E
	MoveIDPosRot = 0x1F
	MoveIDRot    = 0x20
	MoveIDStatus = 0x21
)

// ReadPlayMove decodes any of the four move packet variants by packet id.
// Field order per vanilla: Pos = double*3 + flags byte; PosRot = double*3
// + float*2 + flags; Rot = float*2 + flags; StatusOnly = flags.
func ReadPlayMove(r *protocol.Reader, packetID int32) (ServerboundMovePlayer, error) {
	var m ServerboundMovePlayer
	switch packetID {
	case MoveIDPos:
		m.HasPos = true
	case MoveIDPosRot:
		m.HasPos, m.HasRot = true, true
	case MoveIDRot:
		m.HasRot = true
	case MoveIDStatus:
	default:
		return m, ErrMalformed
	}
	var err error
	if m.HasPos {
		if m.X, err = r.Double(); err != nil {
			return m, err
		}
		if m.Y, err = r.Double(); err != nil {
			return m, err
		}
		if m.Z, err = r.Double(); err != nil {
			return m, err
		}
	}
	if m.HasRot {
		if m.Yaw, err = r.Float(); err != nil {
			return m, err
		}
		if m.Pitch, err = r.Float(); err != nil {
			return m, err
		}
	}
	flags, err := r.Byte()
	if err != nil {
		return m, err
	}
	m.OnGround = flags&1 != 0
	m.HorizontalCollision = flags&2 != 0
	return m, nil
}

// ServerboundAcceptTeleport is the teleport acknowledgement.
func ReadPlayAcceptTeleport(r *protocol.Reader) (int32, error) {
	return r.VarInt()
}

// ReadPlayPlayerLoaded consumes the client's player-loaded notification.
func ReadPlayPlayerLoaded(r *protocol.Reader) error {
	return nil
}

// ReadPlayClientTickEnd consumes the client tick-end notification.
func ReadPlayClientTickEnd(r *protocol.Reader) error {
	return nil
}

// ReadPlayChunkBatchDone decodes the client's chunk throughput report.
func ReadPlayChunkBatchDone(r *protocol.Reader) (float32, error) {
	return r.Float()
}

// ReadPlayConfigAcknowledged consumes the configuration acknowledgement.
func ReadPlayConfigAcknowledged(r *protocol.Reader) error {
	return nil
}

// ---- block interaction (M3) ----

// PlayerAction identifies the serverbound player action packet variants.
type PlayerAction int32

// Vanilla player action statuses.
const (
	ActionStartDestroy PlayerAction = iota
	ActionAbortDestroy
	ActionStopDestroy
	ActionDropAllItem
	ActionDropItem
	ActionReleaseUseItem
	ActionSwapItemWithOffhand
)

// ServerboundPlayerAction decodes the dig/cancel/finish action packet:
// status, BlockPos, face, sequence.
type ServerboundPlayerAction struct {
	Action   PlayerAction
	X, Y, Z  int32
	Face     int32
	Sequence int32
}

// ReadPlayerAction decodes the player action packet.
func ReadPlayerAction(r *protocol.Reader) (ServerboundPlayerAction, error) {
	var a ServerboundPlayerAction
	action, err := r.VarInt()
	if err != nil {
		return a, err
	}
	a.Action = PlayerAction(action)
	if a.X, a.Y, a.Z, err = ReadBlockPos(r); err != nil {
		return a, err
	}
	if a.Face, err = r.VarInt(); err != nil {
		return a, err
	}
	a.Sequence, err = r.VarInt()
	return a, err
}

// ServerboundUseItemOn decodes the right-click-block packet: hand,
// BlockPos, face, cursor offsets, inside-block flag, world-border flag,
// sequence.
type ServerboundUseItemOn struct {
	Hand           int32
	X, Y, Z        int32
	Face           int32
	CursorX        float32
	CursorY        float32
	CursorZ        float32
	InsideBlock    bool
	WorldBorderHit bool
	Sequence       int32
}

// ReadUseItemOn decodes the use-item-on packet.
func ReadUseItemOn(r *protocol.Reader) (ServerboundUseItemOn, error) {
	var u ServerboundUseItemOn
	var err error
	if u.Hand, err = r.VarInt(); err != nil {
		return u, err
	}
	if u.X, u.Y, u.Z, err = ReadBlockPos(r); err != nil {
		return u, err
	}
	if u.Face, err = r.VarInt(); err != nil {
		return u, err
	}
	if u.CursorX, err = r.Float(); err != nil {
		return u, err
	}
	if u.CursorY, err = r.Float(); err != nil {
		return u, err
	}
	if u.CursorZ, err = r.Float(); err != nil {
		return u, err
	}
	if u.InsideBlock, err = r.Bool(); err != nil {
		return u, err
	}
	if u.WorldBorderHit, err = r.Bool(); err != nil {
		return u, err
	}
	u.Sequence, err = r.VarInt()
	return u, err
}

// ReadBlockPos decodes the packed long position: x (26 bits) | z (26
// bits) | y (12 bits, low).
func ReadBlockPos(r *protocol.Reader) (int32, int32, int32, error) {
	v, err := r.Int64()
	if err != nil {
		return 0, 0, 0, err
	}
	x := int32(v >> 38)
	y := int32(v << 52 >> 52) // 12-bit signed
	z := int32(v << 12 >> 38)
	return x, y, z, nil
}

// WriteBlockPos packs a position the same way.
func WriteBlockPos(w *protocol.Writer, x, y, z int32) {
	w.Int64(int64(uint64(x&0x3FFFFFF)<<38 | uint64(z&0x3FFFFFF)<<12 | uint64(y&0xFFF)))
}

// WriteBlockUpdate encodes the single block change packet.
func WriteBlockUpdate(w *protocol.Writer, x, y, z int32, stateID int32) {
	WriteBlockPos(w, x, y, z)
	w.VarInt(stateID)
}

// WriteBlockDestruction encodes the mining progress packet; stage ranges
// 0-9 and -1 clears the overlay.
func WriteBlockDestruction(w *protocol.Writer, entityID int32, x, y, z int32, stage int8) {
	w.VarInt(entityID)
	WriteBlockPos(w, x, y, z)
	w.Byte(byte(stage))
}

// WriteBlockChangedAck encodes the sequence acknowledgement the client
// needs to settle its local block predictions.
func WriteBlockChangedAck(w *protocol.Writer, sequence int32) {
	w.VarInt(sequence)
}

// WriteForgetChunk encodes the unload-chunk packet (note the wire order:
// Z first, then X).
func WriteForgetChunk(w *protocol.Writer, cx, cz int32) {
	w.Int32(cz)
	w.Int32(cx)
}

// ReadPlayPong decodes the play-phase ping pong answer.
func ReadPlayPong(r *protocol.Reader) (int32, error) {
	return r.Int32()
}

// ---- commands, boss bar and system chat (/tpsbar, M3.5) ----

// ReadChatCommand decodes the unsigned serverbound command packet. Since
// 26.2 it carries only the command string (no leading slash); signatures
// moved to the separate signed variant we do not accept yet.
func ReadChatCommand(r *protocol.Reader) (string, error) {
	return r.String(256)
}

// Boss bar operations (vanilla ClientboundBossEventPacket.OperationType
// ordinal order).
const (
	BossOpAdd = iota
	BossOpRemove
	BossOpUpdateProgress
	BossOpUpdateName
	BossOpUpdateStyle
	BossOpUpdateProperties
)

// Boss bar colors (BossEvent.BossBarColor ordinal order) and overlays.
const (
	BossColorPink = iota
	BossColorBlue
	BossColorRed
	BossColorGreen
	BossColorYellow
	BossColorPurple
	BossColorWhite
)

const (
	BossOverlayProgress = iota
	BossOverlayNotched6
	BossOverlayNotched10
	BossOverlayNotched12
	BossOverlayNotched20
)

// writeBossHeader writes the shared UUID + operation prefix.
func writeBossHeader(w *protocol.Writer, id [16]byte, op int32) {
	w.UUID(id)
	w.VarInt(op)
}

// WriteBossAdd encodes the ADD operation: title component, progress,
// color, overlay and property flags (darken=1, music=2, fog=4).
func WriteBossAdd(w *protocol.Writer, id [16]byte, title string, progress float32, color, overlay int32, flags byte) {
	writeBossHeader(w, id, BossOpAdd)
	WriteTextComponent(w, title)
	w.Float(progress)
	w.VarInt(color)
	w.VarInt(overlay)
	w.Byte(flags)
}

// WriteBossRemove encodes the REMOVE operation.
func WriteBossRemove(w *protocol.Writer, id [16]byte) {
	writeBossHeader(w, id, BossOpRemove)
}

// WriteBossProgress encodes the UPDATE_PROGRESS operation.
func WriteBossProgress(w *protocol.Writer, id [16]byte, progress float32) {
	writeBossHeader(w, id, BossOpUpdateProgress)
	w.Float(progress)
}

// WriteBossTitle encodes the UPDATE_NAME operation.
func WriteBossTitle(w *protocol.Writer, id [16]byte, title string) {
	writeBossHeader(w, id, BossOpUpdateName)
	WriteTextComponent(w, title)
}

// WriteBossStyle encodes the UPDATE_STYLE operation (color + overlay).
func WriteBossStyle(w *protocol.Writer, id [16]byte, color, overlay int32) {
	writeBossHeader(w, id, BossOpUpdateStyle)
	w.VarInt(color)
	w.VarInt(overlay)
}

// WriteSystemChat encodes the system chat packet: an NBT component plus
// the overlay flag (false = normal chat position).
func WriteSystemChat(w *protocol.Writer, text string) {
	WriteTextComponent(w, text)
	w.Bool(false)
}

// WriteSetPlayerInventory encodes the 26.2 per-slot inventory sync: slot
// VarInt then an optional ItemStack (count VarInt; when > 0: baked item
// holder id+1, empty component patch = two zero VarInts).
func WriteSetPlayerInventory(w *protocol.Writer, slot int32, itemID int32, count int32) {
	w.VarInt(slot)
	if count <= 0 {
		w.VarInt(0)
		return
	}
	w.VarInt(count)
	w.VarInt(itemID + 1) // baked holder reference: registry index + 1
	w.VarInt(0)          // component patch: no additions
	w.VarInt(0)          // ... and no removals
}

// ReadSetCarriedItem decodes the hotbar selection packet.
func ReadSetCarriedItem(r *protocol.Reader) (int32, error) {
	return r.VarInt()
}
