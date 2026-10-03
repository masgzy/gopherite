package java

import "github.com/masgzy/gopherite/protocol"

// M16 scoreboard packets, verified against the 26.2 decompiled
// ClientboundSetObjectivePacket / ClientboundSetDisplayObjectivePacket /
// ClientboundSetScorePacket / ClientboundResetScorePacket /
// ClientboundSetPlayerTeamPacket STREAM_CODECs.
//
// Components use the rootless network-NBT form (see component.go). All
// optional payloads are a leading boolean followed by the value only
// when present; NumberFormat types live in the NUMBER_FORMAT_TYPE
// registry (blank=0, styled=1, fixed=2) — this milestone never sends a
// custom format, so every optional encodes as a lone false byte.

// Scoreboard objective methods (METHOD_ADD/REMOVE/CHANGE in the vanilla
// packet).
const (
	ScoreObjectiveAdd    = 0
	ScoreObjectiveRemove = 1
	ScoreObjectiveChange = 2
)

// Scoreboard team methods (create/modify payloads differ per method in
// the vanilla packet: 0 and 2 carry the parameter block, 0/3/4 carry the
// player list).
const (
	ScoreTeamAdd    = 0
	ScoreTeamRemove = 1
	ScoreTeamChange = 2
	ScoreTeamJoin   = 3
	ScoreTeamLeave  = 4
)

// Team.Visibility ids (Team.Visibility idMapper order).
const (
	TeamVisAlways            = 0
	TeamVisNever             = 1
	TeamVisHideForOtherTeams = 2
	TeamVisHideForOwnTeam    = 3
)

// Team.CollisionRule ids (Team.CollisionRule idMapper order).
const (
	TeamCollisionAlways         = 0
	TeamCollisionNever          = 1
	TeamCollisionPushOtherTeams = 2
	TeamCollisionPushOwnTeam    = 3
)

// TeamColor ids (TeamColor enum declaration order, matches the
// DisplaySlot.TEAM_* ids).
const (
	TeamColorBlack       = 0
	TeamColorDarkBlue    = 1
	TeamColorDarkGreen   = 2
	TeamColorDarkAqua    = 3
	TeamColorDarkRed     = 4
	TeamColorDarkPurple  = 5
	TeamColorGold        = 6
	TeamColorGray        = 7
	TeamColorDarkGray    = 8
	TeamColorBlue        = 9
	TeamColorGreen       = 10
	TeamColorAqua        = 11
	TeamColorRed         = 12
	TeamColorLightPurple = 13
	TeamColorYellow      = 14
	TeamColorWhite       = 15
)

// DisplaySlot ids (DisplaySlot enum declaration order; the slot field of
// SET_DISPLAY_OBJECTIVE uses writeById → VarInt).
const (
	DisplaySlotList      = 0
	DisplaySlotSidebar   = 1
	DisplaySlotBelowName = 2
	// 3..18 are the sidebar.team.* slots (black..white in TeamColor order);
	// see DisplaySlotTeamSlot.
)

// DisplaySlotTeamSlot maps a TeamColor id (0-15) onto its
// sidebar.team.<color> display slot id (3-18).
func DisplaySlotTeamSlot(color int32) int32 { return 3 + color }

// TeamOptions is the packed option bitmask of the team parameter block:
// bit 0 = friendly fire, bit 1 = see friendly invisibles (PlayerTeam
// packOptions).
const (
	TeamOptionFriendlyFire     = 0x01
	TeamOptionSeeInvisibleTeam = 0x02
)

// WritePlaySetObjective emits 0x6A SET_OBJECTIVE: utf name, byte method,
// and for add/change the display component, render type (VarInt enum:
// integer=0, hearts=1) and an optional number format (always absent).
func WritePlaySetObjective(w *protocol.Writer, name string, method int32, displayName string, renderType int32) {
	w.String(name)
	w.Byte(byte(method))
	if method == ScoreObjectiveAdd || method == ScoreObjectiveChange {
		WriteTextComponent(w, displayName)
		w.VarInt(renderType)
		w.Bool(false) // Optional<NumberFormat>: absent
	}
}

// WritePlaySetDisplayObjective emits 0x62 SET_DISPLAY_OBJECTIVE: VarInt
// slot id + utf objective name ("" clears the slot).
func WritePlaySetDisplayObjective(w *protocol.Writer, slot int32, objectiveName string) {
	w.VarInt(slot)
	w.String(objectiveName)
}

// WritePlaySetScore emits 0x6E SET_SCORE: utf owner, utf objective,
// VarInt score, optional display component (absent) and optional number
// format (absent).
func WritePlaySetScore(w *protocol.Writer, owner, objectiveName string, score int32) {
	w.String(owner)
	w.String(objectiveName)
	w.VarInt(score)
	w.Bool(false) // Optional<Component> display
	w.Bool(false) // Optional<NumberFormat>
}

// WritePlayResetScore emits 0x4F RESET_SCORE: utf owner + optional utf
// objective (absent clears every objective for the owner).
func WritePlayResetScore(w *protocol.Writer, owner string, objectiveName string, hasObjective bool) {
	w.String(owner)
	w.OptionalString(objectiveName, hasObjective)
}

// TeamParams carries the SET_PLAYER_TEAM parameter block (method 0/2).
type TeamParams struct {
	DisplayName  string
	PlayerPrefix string
	PlayerSuffix string
	NameTagVis   int32 // TeamVisibility id
	Collision    int32 // TeamCollision id
	ColorPresent bool
	Color        int32 // TeamColor id when ColorPresent
	Options      byte  // bit0 friendly fire, bit1 see invisible teammates
}

// WritePlaySetPlayerTeam emits 0x6D SET_PLAYER_TEAM: utf name, byte
// method, the parameter block for 0/2 and the member list for 0/3/4.
func WritePlaySetPlayerTeam(w *protocol.Writer, name string, method int32, params *TeamParams, members []string) {
	w.String(name)
	w.Byte(byte(method))
	if method == ScoreTeamAdd || method == ScoreTeamChange {
		p := *params
		WriteTextComponent(w, p.DisplayName)
		WriteTextComponent(w, p.PlayerPrefix)
		WriteTextComponent(w, p.PlayerSuffix)
		w.VarInt(p.NameTagVis)
		w.VarInt(p.Collision)
		if p.ColorPresent {
			w.Bool(true)
			w.VarInt(p.Color)
		} else {
			w.Bool(false)
		}
		w.Byte(p.Options)
	}
	if method == ScoreTeamAdd || method == ScoreTeamJoin || method == ScoreTeamLeave {
		w.VarInt(int32(len(members)))
		for _, m := range members {
			w.String(m)
		}
	}
}
