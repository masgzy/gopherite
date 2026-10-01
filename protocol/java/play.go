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

// WritePlayDisconnect encodes the play-phase disconnect packet.
func WritePlayDisconnect(w *protocol.Writer, reason string) {
	w.String(reason)
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

// ReadPlayPong decodes the play-phase ping pong answer.
func ReadPlayPong(r *protocol.Reader) (int32, error) {
	return r.Int32()
}
