package server

// M16 scoreboard: objectives, teams, per-owner scores and the 19 display
// slots, mirroring vanilla Scoreboard/ServerScoreboard. All model state
// is guarded by Server.mu (every mutator here is a *Locked method).
//
// Wire behaviour follows the 26.2 ServerScoreboard: objectives are only
// broadcast while "tracked" (referenced by a display slot), teams always
// broadcast, and a joining player receives the full snapshot
// (updateEntireScoreboard: every team, then every displayed objective
// with its scores).

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// sbObjective is one scoreboard objective.
type sbObjective struct {
	Name        string
	Criteria    string // vanilla criteria name ("dummy", "health", ...)
	DisplayName string
	RenderType  int32 // 0 = integer, 1 = hearts
	ReadOnly    bool  // vanilla read-only criteria reject players set/add
}

// sbTeam is one player team (PlayerTeam subset).
type sbTeam struct {
	Name         string
	DisplayName  string
	Prefix       string
	Suffix       string
	Color        int32 // TeamColor id, meaningful when ColorSet
	ColorSet     bool
	NameTagVis   int32 // java.TeamVisibility id
	Collision    int32 // java.TeamCollision id
	DeathVis     int32 // java.TeamVisibility id (saved, not on the wire)
	FriendlyFire bool
	SeeInvis     bool
	Members      []string // insertion-ordered member names
}

// displaySlotCount is the number of display slots (list + sidebar +
// below_name + 16 team colours).
const displaySlotCount = 19

// displaySlotNames are the vanilla saved-data keys for the slots, in id
// order (DisplaySlot.getSerializedName).
var displaySlotNames = [displaySlotCount]string{
	"list", "sidebar", "below_name",
	"sidebar.team.black", "sidebar.team.dark_blue", "sidebar.team.dark_green",
	"sidebar.team.dark_aqua", "sidebar.team.dark_red", "sidebar.team.dark_purple",
	"sidebar.team.gold", "sidebar.team.gray", "sidebar.team.dark_gray",
	"sidebar.team.blue", "sidebar.team.green", "sidebar.team.aqua",
	"sidebar.team.red", "sidebar.team.light_purple", "sidebar.team.yellow",
	"sidebar.team.white",
}

// Scoreboard is the server-side scoreboard model.
type Scoreboard struct {
	s *Server

	objectives map[string]*sbObjective
	teams      map[string]*sbTeam
	// scores[owner][objectiveName] = value. Owner names are player names,
	// entity names or fake "#hidden" holders (vanilla ScoreHolder rules).
	scores map[string]map[string]int32
	// display maps a slot id to the displayed objective name ("" = clear).
	display [displaySlotCount]string
	// tracked objectives currently broadcast to clients.
	tracked map[string]bool
}

// newScoreboard builds an empty scoreboard attached to the server.
func newScoreboard(s *Server) *Scoreboard {
	return &Scoreboard{
		s:          s,
		objectives: make(map[string]*sbObjective),
		teams:      make(map[string]*sbTeam),
		scores:     make(map[string]map[string]int32),
		tracked:    make(map[string]bool),
	}
}

// ---- wire helpers (callers hold s.mu) -------------------------------------

// broadcastPacketLocked sends one framed packet to every player. Caller
// holds s.mu.
func (s *Server) broadcastPacketLocked(body *protocol.Writer) {
	payload := body.Bytes()
	for _, p := range s.players {
		_ = p.conn.sendPacket(payload)
	}
}

// broadcastSystemChatLocked sends one chat line to every player.
// Caller holds s.mu.
func (s *Server) broadcastSystemChatLocked(text string) {
	for _, p := range s.players {
		_ = p.conn.sendSystemChat(text)
	}
}

// startTrackingLocked sends the objective add packet plus every display
// slot and score it currently owns, then marks it tracked (vanilla
// startTrackingObjective). Caller holds s.mu.
func (sb *Scoreboard) startTrackingLocked(obj *sbObjective) {
	sb.startTrackingConnLocked(obj, nil)
}

// sendOrBroadcastLocked ships one framed packet to a single connection
// (join path) or every player. Caller holds s.mu.
func (sb *Scoreboard) sendOrBroadcastLocked(body *protocol.Writer, target *conn) {
	if target != nil {
		_ = target.sendPacket(body.Bytes())
	} else {
		sb.s.broadcastPacketLocked(body)
	}
}

// startTrackingConnLocked is startTrackingLocked restricted to one
// connection when target != nil (join path). Every packet is framed
// separately — vanilla getStartTrackingPackets returns a packet list.
// Caller holds s.mu.
func (sb *Scoreboard) startTrackingConnLocked(obj *sbObjective, target *conn) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetObjective)
	java.WritePlaySetObjective(body, obj.Name, java.ScoreObjectiveAdd, obj.DisplayName, obj.RenderType)
	sb.sendOrBroadcastLocked(body, target)
	for slot, name := range sb.display {
		if name == obj.Name {
			b := protocol.NewWriter()
			b.VarInt(v776.PacketPlaySetDisplayObjective)
			java.WritePlaySetDisplayObjective(b, int32(slot), obj.Name)
			sb.sendOrBroadcastLocked(b, target)
		}
	}
	for _, owner := range sb.sortedOwners(obj.Name) {
		b := protocol.NewWriter()
		b.VarInt(v776.PacketPlaySetScore)
		java.WritePlaySetScore(b, owner, obj.Name, sb.scores[owner][obj.Name])
		sb.sendOrBroadcastLocked(b, target)
	}
	sb.tracked[obj.Name] = true
}

// stopTrackingLocked drops the objective from the clients (method 1)
// and unmarks it. Caller holds s.mu.
func (sb *Scoreboard) stopTrackingLocked(obj *sbObjective) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetObjective)
	java.WritePlaySetObjective(body, obj.Name, java.ScoreObjectiveRemove, "", 0)
	sb.s.broadcastPacketLocked(body)
	delete(sb.tracked, obj.Name)
}

// displayCountLocked counts the slots referencing the objective.
func (sb *Scoreboard) displayCountLocked(name string) int {
	n := 0
	for _, d := range sb.display {
		if d == name {
			n++
		}
	}
	return n
}

// sortedOwners returns the score owners of an objective in stable order
// (vanilla iterates its playerScores map unordered; a sorted walk keeps
// tests deterministic without observable gameplay difference).
func (sb *Scoreboard) sortedOwners(objName string) []string {
	var owners []string
	for owner, m := range sb.scores {
		if _, ok := m[objName]; ok {
			owners = append(owners, owner)
		}
	}
	sort.Strings(owners)
	return owners
}

// ---- objectives ------------------------------------------------------------

// addObjectiveLocked registers a new objective. No packet is sent:
// vanilla only starts tracking once the objective enters a display slot.
func (sb *Scoreboard) addObjectiveLocked(name, criteria, displayName string, renderType int32) *sbObjective {
	obj := &sbObjective{
		Name:        name,
		Criteria:    criteria,
		DisplayName: displayName,
		RenderType:  renderType,
		ReadOnly:    criteriaReadOnly(criteria),
	}
	sb.objectives[name] = obj
	return obj
}

// removeObjectiveLocked deletes the objective, its scores and every
// display reference, broadcasting the removal when tracked.
func (sb *Scoreboard) removeObjectiveLocked(name string) bool {
	obj, ok := sb.objectives[name]
	if !ok {
		return false
	}
	for slot := range sb.display {
		if sb.display[slot] == name {
			sb.display[slot] = ""
		}
	}
	for owner := range sb.scores {
		delete(sb.scores[owner], name)
	}
	if sb.tracked[name] {
		sb.stopTrackingLocked(obj)
	}
	delete(sb.objectives, name)
	return true
}

// criteriaReadOnly reports whether the vanilla criteria is a read-only
// statistic (players set/add/reset/enable reject it).
func criteriaReadOnly(criteria string) bool {
	switch criteria {
	case "health", "food", "air", "armor", "xp", "level":
		return true
	}
	return false
}

// validCriteria reports whether the name is a known vanilla criteria.
func validCriteria(criteria string) bool {
	switch criteria {
	case "dummy", "trigger", "deathCount", "playerKillCount", "totalKillCount",
		"health", "food", "air", "armor", "xp", "level":
		return true
	}
	// teamkill.<color> / killedByTeam.<color>.
	for _, prefix := range []string{"teamkill.", "killedByTeam."} {
		if strings.HasPrefix(criteria, prefix) {
			return teamColorByName(criteria[len(prefix):]) >= 0
		}
	}
	return false
}

// teamColorByName resolves a TeamColor serialised name, -1 when unknown.
func teamColorByName(name string) int32 {
	for i, n := range teamColorNames {
		if n == name {
			return int32(i)
		}
	}
	return -1
}

// teamColorNames are the TeamColor serialised names in id order.
var teamColorNames = [16]string{
	"black", "dark_blue", "dark_green", "dark_aqua", "dark_red", "dark_purple",
	"gold", "gray", "dark_gray", "blue", "green", "aqua", "red",
	"light_purple", "yellow", "white",
}

// setDisplayLocked points a slot at an objective ("" clears), mirroring
// the vanilla setDisplayObjective broadcast dance. Caller holds s.mu.
func (sb *Scoreboard) setDisplayLocked(slot int32, objName string) {
	if slot < 0 || slot >= displaySlotCount {
		return
	}
	old := sb.display[slot]
	sb.display[slot] = objName
	if old != objName && old != "" {
		if sb.displayCountLocked(old) > 0 {
			body := protocol.NewWriter()
			body.VarInt(v776.PacketPlaySetDisplayObjective)
			java.WritePlaySetDisplayObjective(body, slot, objName)
			sb.s.broadcastPacketLocked(body)
		} else if o, ok := sb.objectives[old]; ok {
			sb.stopTrackingLocked(o)
		}
	}
	if objName == "" {
		return
	}
	if obj, ok := sb.objectives[objName]; ok {
		if sb.tracked[objName] {
			body := protocol.NewWriter()
			body.VarInt(v776.PacketPlaySetDisplayObjective)
			java.WritePlaySetDisplayObjective(body, slot, objName)
			sb.s.broadcastPacketLocked(body)
		} else {
			sb.startTrackingLocked(obj)
		}
	}
}

// ---- scores ----------------------------------------------------------------

// setScoreLocked upserts one owner score, broadcasting when tracked.
func (sb *Scoreboard) setScoreLocked(owner, objName string, value int32) {
	m, ok := sb.scores[owner]
	if !ok {
		m = make(map[string]int32)
		sb.scores[owner] = m
	}
	m[objName] = value
	if sb.tracked[objName] {
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlaySetScore)
		java.WritePlaySetScore(body, owner, objName, value)
		sb.s.broadcastPacketLocked(body)
	}
}

// addScoreLocked adjusts a score by delta (creating it at 0 first) and
// broadcasts the new value.
func (sb *Scoreboard) addScoreLocked(owner, objName string, delta int32) int32 {
	cur := int32(0)
	if m, ok := sb.scores[owner]; ok {
		cur = m[objName]
	}
	sb.setScoreLocked(owner, objName, cur+delta)
	return cur + delta
}

// resetScoreLocked clears one owner/objective pair (objName "" = every
// objective of the owner), broadcasting per vanilla onPlayerRemoved /
// onPlayerScoreRemoved.
func (sb *Scoreboard) resetScoreLocked(owner, objName string) bool {
	if objName == "" {
		if _, ok := sb.scores[owner]; !ok {
			return false
		}
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlayResetScore)
		java.WritePlayResetScore(body, owner, "", false)
		sb.s.broadcastPacketLocked(body)
		delete(sb.scores, owner)
		return true
	}
	m, ok := sb.scores[owner]
	if !ok {
		return false
	}
	if _, ok := m[objName]; !ok {
		return false
	}
	delete(m, objName)
	if sb.tracked[objName] {
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlayResetScore)
		java.WritePlayResetScore(body, owner, objName, true)
		sb.s.broadcastPacketLocked(body)
	}
	if len(m) == 0 {
		delete(sb.scores, owner)
	}
	return true
}

// bumpCriteriaLocked adds delta to owner under every objective with the
// given criteria (vanilla forAllObjectives). Used by the death/kill
// hooks.
func (sb *Scoreboard) bumpCriteriaLocked(owner, criteria string, delta int32) {
	for _, obj := range sb.objectives {
		if obj.Criteria == criteria {
			sb.addScoreLocked(owner, obj.Name, delta)
		}
	}
}

// ---- teams -----------------------------------------------------------------

// teamParams renders the wire parameter block of a team.
func teamParams(t *sbTeam) *java.TeamParams {
	return &java.TeamParams{
		DisplayName:  t.DisplayName,
		PlayerPrefix: t.Prefix,
		PlayerSuffix: t.Suffix,
		NameTagVis:   t.NameTagVis,
		Collision:    t.Collision,
		ColorPresent: t.ColorSet,
		Color:        t.Color,
		Options:      teamOptions(t),
	}
}

// teamOptions packs the vanilla option bitmask.
func teamOptions(t *sbTeam) byte {
	var b byte
	if t.FriendlyFire {
		b |= java.TeamOptionFriendlyFire
	}
	if t.SeeInvis {
		b |= java.TeamOptionSeeInvisibleTeam
	}
	return b
}

// addTeamLocked registers a team and broadcasts the create packet.
func (sb *Scoreboard) addTeamLocked(name, displayName string) (*sbTeam, error) {
	if _, ok := sb.teams[name]; ok {
		return nil, fmt.Errorf("已存在名为 %s 的队伍", name)
	}
	t := &sbTeam{
		Name:        name,
		DisplayName: displayName,
		NameTagVis:  java.TeamVisAlways,
		Collision:   java.TeamCollisionAlways,
	}
	sb.teams[name] = t
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetPlayerTeam)
	java.WritePlaySetPlayerTeam(body, t.Name, java.ScoreTeamAdd, teamParams(t), t.Members)
	sb.s.broadcastPacketLocked(body)
	return t, nil
}

// removeTeamLocked deletes the team and broadcasts the removal.
func (sb *Scoreboard) removeTeamLocked(name string) bool {
	t, ok := sb.teams[name]
	if !ok {
		return false
	}
	sb.teamsLocked(t)
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetPlayerTeam)
	java.WritePlaySetPlayerTeam(body, t.Name, java.ScoreTeamRemove, nil, nil)
	sb.s.broadcastPacketLocked(body)
	return true
}

// teamsLocked detaches every member (internal helper for removal).
func (sb *Scoreboard) teamsLocked(t *sbTeam) {
	delete(sb.teams, t.Name)
}

// changeTeamLocked broadcasts the parameter refresh (method 2).
func (sb *Scoreboard) changeTeamLocked(t *sbTeam) {
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetPlayerTeam)
	java.WritePlaySetPlayerTeam(body, t.Name, java.ScoreTeamChange, teamParams(t), nil)
	sb.s.broadcastPacketLocked(body)
}

// joinTeamLocked adds a member, broadcasting the single-player packet.
func (sb *Scoreboard) joinTeamLocked(t *sbTeam, member string) bool {
	for _, m := range t.Members {
		if m == member {
			return false
		}
	}
	t.Members = append(t.Members, member)
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySetPlayerTeam)
	java.WritePlaySetPlayerTeam(body, t.Name, java.ScoreTeamJoin, nil, []string{member})
	sb.s.broadcastPacketLocked(body)
	return true
}

// leaveTeamLocked removes a member, broadcasting the packet.
func (sb *Scoreboard) leaveTeamLocked(member string) bool {
	for _, t := range sb.teams {
		for i, m := range t.Members {
			if m == member {
				t.Members = append(t.Members[:i], t.Members[i+1:]...)
				body := protocol.NewWriter()
				body.VarInt(v776.PacketPlaySetPlayerTeam)
				java.WritePlaySetPlayerTeam(body, t.Name, java.ScoreTeamLeave, nil, []string{member})
				sb.s.broadcastPacketLocked(body)
				return true
			}
		}
	}
	return false
}

// teamOfLocked finds the team a member belongs to.
func (sb *Scoreboard) teamOfLocked(member string) *sbTeam {
	for _, t := range sb.teams {
		for _, m := range t.Members {
			if m == member {
				return t
			}
		}
	}
	return nil
}

// ---- join / leave ----------------------------------------------------------

// sendFullScoreboard hands a joining player the vanilla
// updateEntireScoreboard snapshot: every team, then every displayed
// objective (deduplicated) with its display slots and scores.
func (sb *Scoreboard) sendFullScoreboard(c *conn) {
	// Each packet gets its own frame, mirroring the vanilla sequence.
	for _, t := range sb.sortedTeams() {
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlaySetPlayerTeam)
		java.WritePlaySetPlayerTeam(body, t.Name, java.ScoreTeamAdd, teamParams(t), t.Members)
		_ = c.sendPacket(body.Bytes())
	}
	sent := make(map[string]bool)
	for slot, name := range sb.display {
		if name == "" || sent[name] {
			continue
		}
		obj, ok := sb.objectives[name]
		if !ok {
			continue
		}
		sent[name] = true
		body := protocol.NewWriter()
		body.VarInt(v776.PacketPlaySetObjective)
		java.WritePlaySetObjective(body, obj.Name, java.ScoreObjectiveAdd, obj.DisplayName, obj.RenderType)
		_ = c.sendPacket(body.Bytes())
		body = protocol.NewWriter()
		body.VarInt(v776.PacketPlaySetDisplayObjective)
		java.WritePlaySetDisplayObjective(body, int32(slot), obj.Name)
		_ = c.sendPacket(body.Bytes())
		for _, owner := range sb.sortedOwners(obj.Name) {
			b := protocol.NewWriter()
			b.VarInt(v776.PacketPlaySetScore)
			java.WritePlaySetScore(b, owner, obj.Name, sb.scores[owner][obj.Name])
			_ = c.sendPacket(b.Bytes())
		}
	}
}

// sortedTeams returns the teams in stable name order (vanilla iterates
// its map unordered; determinism keeps tests reproducible).
func (sb *Scoreboard) sortedTeams() []*sbTeam {
	names := make([]string, 0, len(sb.teams))
	for n := range sb.teams {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*sbTeam, 0, len(names))
	for _, n := range names {
		out = append(out, sb.teams[n])
	}
	return out
}

// playerLeaving resets the leaving player's personal scores (vanilla
// onPlayerRemoved → RESET_SCORE broadcast) — triggered from removePlayer.
func (sb *Scoreboard) playerLeaving(name string) {
	sb.resetScoreLocked(name, "")
}

// ---- persistence (world/data/scoreboard.dat) --------------------------------

// nbtScoreboardName is the vanilla SavedData file name.
const nbtScoreboardName = "scoreboard"

// saveLocked serialises the scoreboard into the vanilla
// ScoreboardSaveData shape under <level>/data/scoreboard.dat. Caller
// holds s.mu.
func (sb *Scoreboard) saveLocked(dir string) error {
	root := java.NewNbtComp()
	data := java.NewNbtComp()

	// Objectives.
	var objs []java.NbtAny
	for _, o := range sb.sortedObjectives() {
		objs = append(objs, java.NbtAny{Type: java.TagCompound, Comp: java.NewNbtComp().
			Set("Name", java.NbtString(o.Name)).
			Set("Criteria", java.NbtString(o.Criteria)).
			Set("DisplayName", java.NbtString(componentJSON(o.DisplayName))).
			Set("RenderType", java.NbtString(renderTypeName(o.RenderType))),
		})
	}
	data.Set("Objectives", java.NbtListOf(java.TagCompound, objs))

	// Player scores, stable owner order.
	var entries []java.NbtAny
	for _, owner := range sb.sortedOwnersAll() {
		for _, objName := range sb.sortedObjectiveNamesIn(owner) {
			entries = append(entries, java.NbtAny{Type: java.TagCompound, Comp: java.NewNbtComp().
				Set("Name", java.NbtString(owner)).
				Set("Objective", java.NbtString(objName)).
				Set("Score", java.NbtInt(int64(sb.scores[owner][objName]))),
			})
		}
	}
	data.Set("PlayerScores", java.NbtListOf(java.TagCompound, entries))

	// Display slots (only the occupied ones, vanilla EnumMap semantics).
	slots := java.NewNbtComp()
	for i, name := range sb.display {
		if name != "" {
			slots.Set(displaySlotNames[i], java.NbtString(name))
		}
	}
	data.Set("DisplaySlots", java.NbtAny{Type: java.TagCompound, Comp: slots})

	// Teams.
	var teamList []java.NbtAny
	for _, t := range sb.sortedTeams() {
		var members []java.NbtAny
		for _, m := range t.Members {
			members = append(members, java.NbtString(m))
		}
		tc := "reset"
		if t.ColorSet {
			tc = teamColorNames[t.Color]
		}
		teamList = append(teamList, java.NbtAny{Type: java.TagCompound, Comp: java.NewNbtComp().
			Set("Name", java.NbtString(t.Name)).
			Set("DisplayName", java.NbtString(componentJSON(t.DisplayName))).
			Set("MemberNamePrefix", java.NbtString(t.Prefix)).
			Set("MemberNameSuffix", java.NbtString(t.Suffix)).
			Set("AllowFriendlyFire", java.NbtByte(boolByte(t.FriendlyFire))).
			Set("SeeFriendlyInvisibles", java.NbtByte(boolByte(t.SeeInvis))).
			Set("NameTagVisibility", java.NbtString(visibilityName(t.NameTagVis))).
			Set("DeathMessageVisibility", java.NbtString(visibilityName(t.DeathVis))).
			Set("CollisionRule", java.NbtString(collisionName(t.Collision))).
			Set("Color", java.NbtString(tc)).
			Set("Members", java.NbtListOf(java.TagString, members)),
		})
	}
	data.Set("Teams", java.NbtListOf(java.TagCompound, teamList))

	root.Set("Data", java.NbtAny{Type: java.TagCompound, Comp: data})

	var buf protocol.Writer
	java.WriteNbtFile(&buf, root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, nbtScoreboardName+".dat"), zlibEncode(buf.Bytes()), 0o644)
}

// loadScoreboard replays world/data/scoreboard.dat at startup. A missing
// or damaged file just leaves the scoreboard empty.
func (sb *Scoreboard) loadScoreboard(dir string) {
	path := filepath.Join(dir, nbtScoreboardName+".dat")
	raw, err := os.ReadFile(path)
	if err != nil {
		return
	}
	payload, err := zlibDecode(raw)
	if err != nil {
		log.Printf(ui.Warn("警告")+" scoreboard.dat 解压失败: %v", err)
		return
	}
	root, err := java.ReadNbtFile(protocol.NewReader(payload))
	if err != nil {
		log.Printf(ui.Warn("警告")+" scoreboard.dat 解析失败: %v", err)
		return
	}
	dataNbt := root.Get("Data").Comp
	if dataNbt == nil {
		return
	}

	// Objectives.
	for _, e := range dataNbt.Get("Objectives").List {
		c := e.Comp
		if c == nil {
			continue
		}
		name := c.Get("Name").Str
		criteria := c.Get("Criteria").Str
		if criteria == "" {
			criteria = "dummy"
		}
		displayName := componentName(c.Get("DisplayName").Str)
		renderType := renderTypeID(c.Get("RenderType").Str)
		if _, exists := sb.objectives[name]; !exists {
			sb.addObjectiveLocked(name, criteria, displayName, renderType)
		}
	}

	// Scores.
	for _, e := range dataNbt.Get("PlayerScores").List {
		c := e.Comp
		if c == nil {
			continue
		}
		owner, objName := c.Get("Name").Str, c.Get("Objective").Str
		if _, ok := sb.objectives[objName]; !ok {
			continue
		}
		m, ok := sb.scores[owner]
		if !ok {
			m = make(map[string]int32)
			sb.scores[owner] = m
		}
		m[objName] = int32(c.Get("Score").Num)
	}

	// Display slots.
	if slots := dataNbt.Get("DisplaySlots").Comp; slots != nil {
		for i, key := range displaySlotNames {
			if name := slots.Get(key).Str; name != "" {
				if _, ok := sb.objectives[name]; ok {
					sb.display[i] = name
					sb.tracked[name] = true
				}
			}
		}
	}

	// Teams.
	for _, e := range dataNbt.Get("Teams").List {
		c := e.Comp
		if c == nil {
			continue
		}
		name := c.Get("Name").Str
		if name == "" || sb.teams[name] != nil {
			continue
		}
		color := teamColorByName(c.Get("Color").Str)
		t := &sbTeam{
			Name:         name,
			DisplayName:  componentName(c.Get("DisplayName").Str),
			Prefix:       c.Get("MemberNamePrefix").Str,
			Suffix:       c.Get("MemberNameSuffix").Str,
			NameTagVis:   visibilityID(c.Get("NameTagVisibility").Str),
			DeathVis:     visibilityID(c.Get("DeathMessageVisibility").Str),
			Collision:    collisionID(c.Get("CollisionRule").Str),
			FriendlyFire: c.Get("AllowFriendlyFire").Num != 0,
			SeeInvis:     c.Get("SeeFriendlyInvisibles").Num != 0,
			ColorSet:     color >= 0 && c.Get("Color").Str != "reset",
			Color:        max(color, int32(0)),
		}
		for _, m := range c.Get("Members").List {
			if m.Str != "" {
				t.Members = append(t.Members, m.Str)
			}
		}
		sb.teams[name] = t
	}
}

// sortedObjectives orders the objectives by name (stable persistence).
func (sb *Scoreboard) sortedObjectives() []*sbObjective {
	names := make([]string, 0, len(sb.objectives))
	for n := range sb.objectives {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]*sbObjective, 0, len(names))
	for _, n := range names {
		out = append(out, sb.objectives[n])
	}
	return out
}

// sortedOwnersAll lists every owner with at least one score.
func (sb *Scoreboard) sortedOwnersAll() []string {
	owners := make([]string, 0, len(sb.scores))
	for o := range sb.scores {
		owners = append(owners, o)
	}
	sort.Strings(owners)
	return owners
}

// sortedObjectiveNamesIn orders one owner's objectives by name.
func (sb *Scoreboard) sortedObjectiveNamesIn(owner string) []string {
	names := make([]string, 0, len(sb.scores[owner]))
	for n := range sb.scores[owner] {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// ---- small helpers -----------------------------------------------------------

// boolByte encodes a NBT boolean.
func boolByte(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// renderTypeName maps the render type id to its saved name.
func renderTypeName(id int32) string {
	if id == 1 {
		return "hearts"
	}
	return "integer"
}

// renderTypeID parses the saved render type name.
func renderTypeID(name string) int32 {
	if name == "hearts" {
		return 1
	}
	return 0
}

// visibilityName maps a TeamVisibility id to its serialised name.
func visibilityName(id int32) string {
	switch id {
	case java.TeamVisNever:
		return "never"
	case java.TeamVisHideForOtherTeams:
		return "hideForOtherTeams"
	case java.TeamVisHideForOwnTeam:
		return "hideForOwnTeam"
	}
	return "always"
}

// visibilityID parses a TeamVisibility serialised name.
func visibilityID(name string) int32 {
	switch name {
	case "never":
		return java.TeamVisNever
	case "hideForOtherTeams":
		return java.TeamVisHideForOtherTeams
	case "hideForOwnTeam":
		return java.TeamVisHideForOwnTeam
	}
	return java.TeamVisAlways
}

// collisionName maps a TeamCollision id to its serialised name.
func collisionName(id int32) string {
	switch id {
	case java.TeamCollisionNever:
		return "never"
	case java.TeamCollisionPushOtherTeams:
		return "pushOtherTeams"
	case java.TeamCollisionPushOwnTeam:
		return "pushOwnTeam"
	}
	return "always"
}

// visibilityIDStrict parses a TeamVisibility name, rejecting unknowns
// (command validation).
func visibilityIDStrict(name string) (int32, bool) {
	switch name {
	case "always", "never", "hideForOtherTeams", "hideForOwnTeam":
		return visibilityID(name), true
	}
	return 0, false
}

// collisionIDStrict parses a TeamCollision name, rejecting unknowns.
func collisionIDStrict(name string) (int32, bool) {
	switch name {
	case "always", "never", "pushOtherTeams", "pushOwnTeam":
		return collisionID(name), true
	}
	return 0, false
}

// collisionID parses a TeamCollision serialised name.
func collisionID(name string) int32 {
	switch name {
	case "never":
		return java.TeamCollisionNever
	case "pushOtherTeams":
		return java.TeamCollisionPushOtherTeams
	case "pushOwnTeam":
		return java.TeamCollisionPushOwnTeam
	}
	return java.TeamCollisionAlways
}

// componentJSON renders a plain text string as the saved component form
// ({"text":"..."} JSON, vanilla component codec).
func componentJSON(text string) string {
	if text == "" {
		return ""
	}
	return `{"text":` + strconv.Quote(text) + `}`
}

// componentName extracts the plain text out of a saved component string,
// tolerating plain strings, {"text":...} component JSON and empty
// payloads. Scoreboard load runs once per boot, so the tiny JSON decode
// is acceptable here.
func componentName(s string) string {
	if s == "" {
		return ""
	}
	if !strings.HasPrefix(s, "{") {
		return s
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return s
	}
	if text, ok := m["text"].(string); ok {
		return text
	}
	return s
}
