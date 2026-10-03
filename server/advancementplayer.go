package server

// M17 per-player advancement progress: the PlayerAdvancements port —
// progress maps, visibility tracking, dirty-flush batching, tab
// selection and the vanilla advancements/<uuid>.json persistence.

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// advProgress is one advancement's per-player state: criterion name ->
// completion epoch millis (0 = incomplete), plus the requirements
// snapshot used to decide done-ness.
type advProgress struct {
	criteria     map[string]int64
	requirements [][]string
}

// isDone evaluates the AND-of-OR requirement groups.
func (p *advProgress) isDone() bool {
	if len(p.requirements) == 0 {
		return false
	}
	for _, group := range p.requirements {
		anyDone := false
		for _, name := range group {
			if p.criteria[name] > 0 {
				anyDone = true
				break
			}
		}
		if !anyDone {
			return false
		}
	}
	return true
}

// hasProgress reports whether any criterion is complete (drives what is
// persisted, mirroring vanilla asData()).
func (p *advProgress) hasProgress() bool {
	for _, done := range p.criteria {
		if done > 0 {
			return true
		}
	}
	return false
}

// update re-binds the requirements and prunes/adds criterion slots to
// match them (vanilla AdvancementProgress.update).
func (p *advProgress) update(reqs [][]string) {
	names := make(map[string]bool)
	for _, group := range reqs {
		for _, name := range group {
			names[name] = true
		}
	}
	for name := range p.criteria {
		if !names[name] {
			delete(p.criteria, name)
		}
	}
	for name := range names {
		if _, ok := p.criteria[name]; !ok {
			p.criteria[name] = 0
		}
	}
	p.requirements = reqs
}

// playerAdvancements tracks one player's progress over the shared tree.
// All state is guarded by Server.mu (the same lock as the rest of the
// world model); only flushDirty sends packets, which mirrors the
// vanilla doTick cadence.
type playerAdvancements struct {
	tree            *advancementTree
	progress        map[string]*advProgress
	visible         map[string]bool
	progressChanged map[string]bool
	rootsToUpdate   map[string]bool
	firstPacket     bool
	lastSelectedTab string
	// listeners: trigger identifier -> ordered listener entries. Entries
	// are (advancement id, criterion name, conditions). Only triggers the
	// engine can fire get registered; unknown triggers stay permanently
	// incomplete exactly like on a vanilla server missing the feature.
	listeners map[string][]advListener
}

type advListener struct {
	adv        string
	criterion  string
	conditions json.RawMessage
}

func newPlayerAdvancements(tree *advancementTree) *playerAdvancements {
	return &playerAdvancements{
		tree:            tree,
		progress:        make(map[string]*advProgress),
		visible:         make(map[string]bool),
		progressChanged: make(map[string]bool),
		rootsToUpdate:   make(map[string]bool),
		firstPacket:     true,
		listeners:       make(map[string][]advListener),
	}
}

// getOrStartProgress returns (creating if needed) the progress record
// bound to the advancement's requirements.
func (pa *playerAdvancements) getOrStartProgress(id string) *advProgress {
	if p, ok := pa.progress[id]; ok {
		return p
	}
	p := &advProgress{criteria: make(map[string]int64)}
	if def := pa.tree.defs[id]; def != nil {
		p.update(def.requirements)
	}
	pa.progress[id] = p
	return p
}

// registerListeners hooks every incomplete criterion whose trigger the
// engine supports (vanilla registerListeners, filtered by supported
// trigger set).
func (pa *playerAdvancements) registerListeners(id string) {
	def := pa.tree.defs[id]
	if def == nil {
		return
	}
	progress := pa.getOrStartProgress(id)
	if progress.isDone() {
		return
	}
	for _, c := range def.criteria {
		if progress.criteria[c.name] > 0 {
			continue
		}
		if !advTriggerSupported(c.trigger) {
			continue
		}
		pa.listeners[c.trigger] = append(pa.listeners[c.trigger], advListener{
			adv:        id,
			criterion:  c.name,
			conditions: c.conditions,
		})
	}
}

// unregisterListeners drops every listener entry of the advancement
// (vanilla unregisterListeners: called when a criterion completes or
// the advancement is done).
func (pa *playerAdvancements) unregisterListeners(id string) {
	for trigger, entries := range pa.listeners {
		kept := entries[:0]
		for _, e := range entries {
			if e.adv != id {
				kept = append(kept, e)
			}
		}
		if len(kept) == 0 {
			delete(pa.listeners, trigger)
		} else {
			pa.listeners[trigger] = kept
		}
	}
}

// award grants one criterion; returns true when the criterion flipped
// from incomplete to complete. Completion grants rewards and re-evaluates
// visibility exactly like vanilla PlayerAdvancements.award (the chat
// announcement rides on the same wasDone/nowDone edge).
func (s *Server) advAward(pa *playerAdvancements, p *player, id, criterion string) bool {
	def := pa.tree.defs[id]
	if def == nil {
		return false
	}
	progress := pa.getOrStartProgress(id)
	if _, known := progress.criteria[criterion]; !known {
		return false // unknown criterion: vanilla grantProgress no-ops
	}
	if progress.criteria[criterion] > 0 {
		return false
	}
	wasDone := progress.isDone()
	progress.criteria[criterion] = time.Now().UnixMilli()
	pa.unregisterListeners(id)
	pa.progressChanged[id] = true
	if !wasDone && progress.isDone() {
		s.advGrantRewards(pa, p, def)
		if def.display != nil && def.display.announceChat {
			s.announceAdvancement(p, def)
		}
	}
	s.advMarkVisibility(pa, id)
	return true
}

// advRevoke revokes one criterion (commands); true when it flipped from
// complete to incomplete.
func (s *Server) advRevoke(pa *playerAdvancements, p *player, id, criterion string) bool {
	def := pa.tree.defs[id]
	if def == nil {
		return false
	}
	progress := pa.getOrStartProgress(id)
	if progress.criteria[criterion] == 0 {
		return false
	}
	wasDone := progress.isDone()
	progress.criteria[criterion] = 0
	pa.progressChanged[id] = true
	if wasDone && !progress.isDone() {
		pa.registerListeners(id)
	}
	s.advMarkVisibility(pa, id)
	return true
}

// advMarkVisibility queues the advancement's root for the next
// visibility re-evaluation (vanilla markForVisibilityUpdate).
func (s *Server) advMarkVisibility(pa *playerAdvancements, id string) {
	root := pa.tree.rootOf(id)
	if root != "" {
		pa.rootsToUpdate[root] = true
	}
}

// advGrantRewards applies the rewards block on completion: experience is
// granted directly, loot tables and datapack functions do not exist in
// this server yet (logged once), and recipe unlocks ride on the recipe
// advancement criteria even though the client recipe book sync is still
// a future milestone (vanilla awardRecipesByKey equivalent).
func (s *Server) advGrantRewards(pa *playerAdvancements, p *player, def *advDef) {
	if def.experience != 0 {
		p.giveExperiencePointsLocked(def.experience)
		p.sendExperience()
	}
	for _, recipe := range def.rewardRecipes {
		// Grant the matching recipe advancement's criteria (both the
		// recipe_unlocked criterion and, when the ingredient criterion is
		// already satisfied, the whole advancement).
		s.advUnlockRecipe(pa, p, recipe)
	}
	for _, loot := range def.rewardLoot {
		log.Printf(ui.Warn("! ")+"进度奖励 loot 表暂未实现: %s", loot)
	}
}

// advUnlockRecipe is the rewards.recipes hook. Recipe advancements are
// not part of the embedded tree and the client recipe book sync is a
// future milestone, so the reward is currently a no-op kept for the
// vanilla call shape.
func (s *Server) advUnlockRecipe(pa *playerAdvancements, p *player, recipe string) {
	_ = recipe
}

// advTrigger fires one event: every listener of the trigger type whose
// conditions match the event context gets awarded.
func (s *Server) advTrigger(pa *playerAdvancements, p *player, trigger string, ctx advContext) {
	entries := pa.listeners[trigger]
	if len(entries) == 0 {
		return
	}
	for _, e := range entries {
		def := pa.tree.defs[e.adv]
		if def == nil {
			continue
		}
		c := def.criterion(e.criterion)
		if c == nil {
			continue
		}
		if advConditionsMatch(c.conditions, ctx) {
			s.advAward(pa, p, e.adv, e.criterion)
		}
	}
}

// flushDirty builds and sends the incremental sync packet: the vanilla
// flushDirty batch of visibility transitions + progress changes. A tick
// cadence call — usually a no-op.
func (s *Server) advFlushDirty(pa *playerAdvancements, c *conn) {
	if !pa.firstPacket && len(pa.rootsToUpdate) == 0 && len(pa.progressChanged) == 0 {
		return
	}
	added := make([]string, 0)
	removed := make([]string, 0)
	for root := range pa.rootsToUpdate {
		pa.tree.updateTreeVisibility(root,
			func(id string) bool { return pa.getOrStartProgress(id).isDone() },
			func(id string) {
				if !pa.visible[id] {
					pa.visible[id] = true
					added = append(added, id)
					if _, ok := pa.progress[id]; ok {
						pa.progressChanged[id] = true
					}
				}
			},
			func(id string) {
				if pa.visible[id] {
					delete(pa.visible, id)
					removed = append(removed, id)
				}
			})
	}
	pa.rootsToUpdate = make(map[string]bool)

	progress := make([]string, 0)
	for id := range pa.progressChanged {
		if pa.visible[id] {
			progress = append(progress, id)
		}
	}
	pa.progressChanged = make(map[string]bool)

	if len(progress) == 0 && len(added) == 0 && len(removed) == 0 {
		pa.firstPacket = false
		return
	}

	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(progress)

	defs := make([]java.AdvancementDef, 0, len(added))
	for _, id := range added {
		defs = append(defs, pa.tree.wireDef(id))
	}
	prog := make([]java.AdvancementProgressWire, 0, len(progress))
	for _, id := range progress {
		p := pa.progress[id]
		if p == nil {
			continue
		}
		crit := make(map[string]java.CriterionProgressWire, len(p.criteria))
		for name, millis := range p.criteria {
			crit[name] = java.CriterionProgressWire{Obtained: millis}
		}
		prog = append(prog, java.AdvancementProgressWire{ID: id, Criteria: crit})
	}

	// 独立 Writer：flush 可能从 ticker 协程触发（tickSurvival），而
	// 连接协程同时用 c.wr 编码区块/移动包——tick 路径的发送一律
	// 自建缓冲（与 sendHealth 同一惯例）。
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayUpdateAdvancements)
	java.WriteUpdateAdvancements(w, pa.firstPacket, defs, removed, prog, true)
	_ = c.sendPacket(w.Bytes())
	pa.firstPacket = false
}

// wireDef converts a definition into its protocol-ready shape.
func (t *advancementTree) wireDef(id string) java.AdvancementDef {
	def := t.defs[id]
	w := java.AdvancementDef{
		ID:             def.id,
		Parent:         def.parent,
		Requirements:   def.requirements,
		SendsTelemetry: def.sendsTelemetry,
	}
	if def.display != nil {
		w.HasDisplay = true
		w.Title = def.display.title
		w.Desc = def.display.description
		w.IconItem = itemIDByName[def.display.icon]
		w.IconCount = def.display.iconCount
		w.Frame = frameOrdinal(def.display.frame)
		w.Background = def.display.background
		w.ShowToast = def.display.showToast
		w.Hidden = def.display.hidden
		w.X = def.display.x
		w.Y = def.display.y
	}
	return w
}

// frameOrdinal maps the AdvancementType names to wire ordinals.
func frameOrdinal(frame string) int32 {
	switch frame {
	case "challenge":
		return 1
	case "goal":
		return 2
	default:
		return 0
	}
}

// rootOf walks the parent chain to the definition's root id.
func (t *advancementTree) rootOf(id string) string {
	seen := 0
	for {
		def := t.defs[id]
		if def == nil || def.parent == "" {
			return id
		}
		id = def.parent
		seen++
		if seen > len(t.defs) {
			return "" // cycle guard; loadAdvancementTree rejects cycles
		}
	}
}

// advSetSelectedTab mirrors PlayerAdvancements.setSelectedTab: only
// display-bearing roots may be selected; a change pushes the selection
// packet so the client opens on the right tab.
func (s *Server) advSetSelectedTab(pa *playerAdvancements, c *conn, id string) {
	next := ""
	if def := pa.tree.defs[id]; def != nil && def.parent == "" && def.display != nil {
		next = def.id
	}
	if next == pa.lastSelectedTab {
		return
	}
	pa.lastSelectedTab = next
	c.wr.Reset()
	c.wr.VarInt(v776.PacketPlaySelectAdvancementsTab)
	java.WriteSelectAdvancementsTab(c.wr, next, next != "")
	_ = c.sendPacket(c.wr.Bytes())
}

// --- persistence ----------------------------------------------------------

// advSavePath returns <level>/advancements/<uuid>.json — the vanilla
// player advancement file location and layout.
func (s *Server) advSavePath(profileID [16]byte) string {
	return filepath.Join(s.opts.LevelName, "advancements", java.FormatUUID(profileID)+".json")
}

// advSave writes the player's progress file. Only advancements with at
// least one completed criterion are written (vanilla asData()).
func (s *Server) advSave(pa *playerAdvancements, profileID [16]byte) {
	if s.opts.LevelName == "" {
		return
	}
	doc := make(map[string]*advSavedProgress)
	for _, id := range pa.tree.order {
		p, ok := pa.progress[id]
		if !ok || !p.hasProgress() {
			continue
		}
		crit := make(map[string]string)
		for name, millis := range p.criteria {
			if millis > 0 {
				crit[name] = formatAdvTimestamp(millis)
			}
		}
		doc[id] = &advSavedProgress{
			Criteria: crit,
			Done:     p.isDone(),
		}
	}
	path := s.advSavePath(profileID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		log.Printf(ui.Error("X ")+"保存进度失败 %s: %v", path, err)
		return
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		log.Printf(ui.Error("X ")+"序列化进度失败: %v", err)
		return
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Printf(ui.Error("X ")+"写入进度失败 %s: %v", path, err)
	}
}

// advSavedProgress is the per-advancement JSON document shape, matching
// the vanilla PlayerAdvancements.Data codec (criteria dates in
// "yyyy-MM-dd HH:mm:ss Z" form plus the always-present done flag).
type advSavedProgress struct {
	Criteria map[string]string `json:"criteria"`
	Done     bool              `json:"done"`
}

// formatAdvTimestamp renders epoch millis the way the vanilla codec
// writes them ("yyyy-MM-dd HH:mm:ss Z", UTC).
func formatAdvTimestamp(millis int64) string {
	t := time.UnixMilli(millis).UTC()
	return t.Format("2006-01-02 15:04:05 +0000")
}

// parseAdvTimestamp reads back the same format; unparsable values leave
// the criterion incomplete rather than fabricating a date.
func parseAdvTimestamp(s string) int64 {
	if t, err := time.Parse("2006-01-02 15:04:05 -0700", s); err == nil {
		return t.UnixMilli()
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.UnixMilli()
	}
	return 0
}

// advLoad replays a saved progress file into the fresh per-player state
// (vanilla load/applyFrom): existing advancement ids restore their
// criteria, unknown ids are warned away, and everything restored joins
// the next flush.
func (s *Server) advLoad(pa *playerAdvancements, profileID [16]byte) {
	if s.opts.LevelName == "" {
		return
	}
	data, err := os.ReadFile(s.advSavePath(profileID))
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf(ui.Warn("! ")+"读取进度失败: %v", err)
		}
		return
	}
	var doc map[string]*advSavedProgress
	if err := json.Unmarshal(data, &doc); err != nil {
		log.Printf(ui.Warn("! ")+"解析进度存档失败: %v", err)
		return
	}
	ids := make([]string, 0, len(doc))
	for id := range doc {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		saved := doc[id]
		def := pa.tree.defs[id]
		if def == nil || saved == nil {
			log.Printf(ui.Warn("! ")+"忽略进度存档中的未知进度 %s", id)
			continue
		}
		p := pa.getOrStartProgress(id)
		for name, when := range saved.Criteria {
			if _, ok := p.criteria[name]; ok {
				p.criteria[name] = parseAdvTimestamp(when)
			}
		}
		pa.progressChanged[id] = true
		s.advMarkVisibility(pa, id)
	}
}

// advDispatch executes one trigger event for the player's listener set.
// Caller holds Server.mu.
func (s *Server) advDispatch(p *player, trigger string, ctx advContext) {
	if p == nil || p.adv == nil {
		return
	}
	s.advTrigger(p.adv, p, trigger, ctx)
}

// advTickTick awards the per-tick trigger (roots auto-complete) and
// flushes any accumulated dirty state — the vanilla doTick cadence.
// Caller holds no lock; takes the server lock for the model pass.
func (s *Server) advTickTick(p *player) {
	s.mu.Lock()
	s.advDispatch(p, "minecraft:tick", advContext{})
	s.advFlushDirty(p.adv, p.conn)
	s.mu.Unlock()
}

// strings helper shared by the command surface.
func advJoinIDs(ids []string) string {
	sort.Strings(ids)
	return strings.Join(ids, ", ")
}

var _ = fmt.Sprintf // keep fmt for debug paths

// announceAdvancement broadcasts the completion chat line to every
// player (vanilla PlayerList.broadcastSystemMessage of the frame-typed
// announcement; gamerule announcements default on).
// Caller holds Server.mu.
func (s *Server) announceAdvancement(p *player, def *advDef) {
	frame := "task"
	if def.display.frame != "" {
		frame = def.display.frame
	}
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySystemChat)
	java.WriteAdvancementAnnouncement(body, p.name, def.display.title, def.display.description, frame)
	body.Bool(false)
	payload := body.Bytes()
	for _, other := range s.players {
		_ = other.conn.sendPacket(payload)
	}
}

// saveAllAdvancements persists every online player's progress (shutdown
// path; logout saves individually in removePlayer).
func (s *Server) saveAllAdvancements() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		if p.adv != nil {
			s.advSave(p.adv, p.conn.profileID)
		}
	}
}
