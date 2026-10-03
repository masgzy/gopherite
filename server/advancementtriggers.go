package server

// M17 trigger engine: the criterion condition evaluator (the loot
// condition subset the vanilla 26.2 advancement tree actually uses) and
// the event entry points the milestone hooks into mining, combat,
// survival, brewing, beds and effects.

import (
	"encoding/json"
	"math"
)

// advContext carries the event data a condition set may reference.
// Missing fields simply never match conditions that need them, mirroring
// a vanilla server whose world lacks the feature (criterion stays
// incomplete).
type advContext struct {
	player *player
	// entity event data (kill / hurt triggers).
	entityType string // "minecraft:zombie"
	// damage source data (player_hurt_entity / killed_by_arrow).
	damageProjectile   bool
	damageDirectEntity string // "minecraft:arrow" or ""
	// item event data (consume / inventory_changed): the changed item and
	// a snapshot of the player's inventory item counts.
	itemID    int32
	itemCount int32
	inventory map[string]int32 // item identifier -> total count
	// block event data (placed_block).
	blockName string
	// potion event data (brewed_potion): registry name.
	potion string
	// fall/levitation geometry: the reference start position and the
	// current player position.
	startY, currentY float64
	hasStart         bool
	// effects snapshot (effects_changed): effect registry name -> amplifier.
	effectNames map[string]int32
	// biome is constant in this server (single superflat biome).
	biome string
}

// advTriggerSupported reports whether the engine can fire the trigger.
// Everything else is skipped at registration — those criteria stay
// permanently incomplete exactly like on a vanilla server missing the
// underlying feature (end/nether/beacon/villagers/...).
func advTriggerSupported(trigger string) bool {
	switch trigger {
	case "minecraft:tick",
		"minecraft:inventory_changed",
		"minecraft:player_killed_entity",
		"minecraft:entity_killed_player",
		"minecraft:consume_item",
		"minecraft:placed_block",
		"minecraft:effects_changed",
		"minecraft:levitation",
		"minecraft:slept_in_bed",
		"minecraft:fall_from_height",
		"minecraft:player_hurt_entity",
		"minecraft:killed_by_arrow",
		"minecraft:brewed_potion":
		return true
	}
	return false
}

// advConditionsMatch evaluates one criterion's raw conditions JSON
// against the event context. Unknown condition shapes fail closed: a
// criterion the engine cannot fully evaluate simply never completes,
// which is the same outcome a vanilla server with a missing feature
// produces.
func advConditionsMatch(raw json.RawMessage, ctx advContext) bool {
	if len(raw) == 0 {
		return true
	}
	var conds map[string]json.RawMessage
	if err := json.Unmarshal(raw, &conds); err != nil {
		return false
	}
	// Empty conditions always match.
	if len(conds) == 0 {
		return true
	}
	for key, value := range conds {
		var ok bool
		switch key {
		case "entity":
			ok = advEntityConditionList(value, ctx, ctx.entityType, ctx.entityType != "")
		case "player":
			ok = advPlayerConditionList(value, ctx)
		case "damage":
			ok = advDamageMatches(value, ctx)
		case "item":
			ok = advItemPredicate(value, ctx.itemID, ctx.itemCount)
		case "items":
			ok = advInventoryPredicate(value, ctx.inventory)
		case "block":
			ok = advStringOrList(value, ctx.blockName)
		case "potion":
			ok = advStringOrList(value, ctx.potion)
		case "effects":
			ok = advEffectsMatch(value, ctx)
		case "distance":
			ok = advDistanceMatch(value, ctx)
		case "start_position":
			// fall_from_height: entity condition list evaluated against
			// the position the fall started from.
			ok = advStartPositionList(value, ctx)
		default:
			// Unknown parameter (levels, recipe, locations, ...): fail
			// closed.
			ok = false
		}
		if !ok {
			return false
		}
	}
	return true
}

// advEntityConditionList evaluates a loot condition list whose
// predicates target the event entity (kill/hurt triggers).
func advEntityConditionList(raw json.RawMessage, ctx advContext, entityType string, hasEntity bool) bool {
	var list []advEntityCondition
	if err := json.Unmarshal(raw, &list); err != nil {
		// The 26.2 tree always uses the list form; a bare object form is
		// tolerated by evaluating it directly.
		var single advEntityCondition
		if err := json.Unmarshal(raw, &single); err != nil {
			return false
		}
		return advEntityConditionMatches(single, ctx, entityType, hasEntity)
	}
	for _, c := range list {
		if !advEntityConditionMatches(c, ctx, entityType, hasEntity) {
			return false
		}
	}
	return true
}

type advEntityCondition struct {
	Condition string `json:"condition"`
	Entity    string `json:"entity"`
	Predicate struct {
		EntityType string `json:"minecraft:entity_type"`
		Location   struct {
			Biomes   json.RawMessage `json:"biomes"`
			Position json.RawMessage `json:"position"`
		} `json:"minecraft:location"`
	} `json:"predicate"`
}

// advEntityConditionMatches evaluates one entity_properties condition.
func advEntityConditionMatches(c advEntityCondition, ctx advContext, entityType string, hasEntity bool) bool {
	// Only "this" (the event entity) selectors appear in the tree.
	if c.Condition != "minecraft:entity_properties" {
		return false
	}
	if c.Predicate.EntityType != "" {
		if !hasEntity || !advEntityIDMatches(c.Predicate.EntityType, entityType) {
			return false
		}
	}
	return true
}

// advEntityIDMatches compares a predicate pattern (plain name or
// #tag) with the concrete entity type.
func advEntityIDMatches(pattern, entityType string) bool {
	if pattern == entityType {
		return true
	}
	if members, ok := entityTagMembers[pattern]; ok {
		for _, m := range members {
			if m == entityType {
				return true
			}
		}
	}
	return false
}

// entityTagMembers covers the entity tags the vanilla tree references.
var entityTagMembers = map[string][]string{
	"#minecraft:arrows": {"minecraft:arrow", "minecraft:spectral_arrow", "minecraft:trident"},
}

// advPlayerConditionList evaluates the "player" condition list: entity
// properties over the acting player (location biome/position only).
func advPlayerConditionList(raw json.RawMessage, ctx advContext) bool {
	if ctx.player == nil {
		return false
	}
	var list []advEntityCondition
	if err := json.Unmarshal(raw, &list); err != nil {
		return false
	}
	for _, c := range list {
		if c.Condition != "minecraft:entity_properties" {
			return false
		}
		loc := c.Predicate.Location
		if len(loc.Biomes) > 0 {
			if !advStringOrList(loc.Biomes, ctx.biome) {
				return false
			}
		}
		if len(loc.Position) > 0 {
			if !advPositionMatch(loc.Position, ctx.player.y) {
				return false
			}
		}
	}
	return true
}

// advStartPositionList evaluates the fall start position conditions
// (same shape as "player" but against the recorded start Y).
func advStartPositionList(raw json.RawMessage, ctx advContext) bool {
	if !ctx.hasStart {
		return false
	}
	var list []advEntityCondition
	if err := json.Unmarshal(raw, &list); err != nil {
		return false
	}
	for _, c := range list {
		if c.Condition != "minecraft:entity_properties" {
			return false
		}
		loc := c.Predicate.Location
		if len(loc.Position) > 0 {
			if !advPositionMatch(loc.Position, ctx.startY) {
				return false
			}
		}
		if len(loc.Biomes) > 0 {
			if !advStringOrList(loc.Biomes, ctx.biome) {
				return false
			}
		}
	}
	return true
}

// advPositionMatch evaluates {"x": {...}, "y": {...}, "z": {...}} ranges
// against one coordinate (only y ranges appear in the tree).
func advPositionMatch(raw json.RawMessage, y float64) bool {
	var pos struct {
		X *advRange `json:"x"`
		Y *advRange `json:"y"`
		Z *advRange `json:"z"`
	}
	if err := json.Unmarshal(raw, &pos); err != nil {
		return false
	}
	if pos.Y != nil && !pos.Y.contains(y) {
		return false
	}
	return true
}

// advRange is a vanilla MinMaxBounds.Double: {"min": f, "max": f}.
type advRange struct {
	Min *float64 `json:"min"`
	Max *float64 `json:"max"`
}

func (r *advRange) contains(v float64) bool {
	if r.Min != nil && v < *r.Min {
		return false
	}
	if r.Max != nil && v > *r.Max {
		return false
	}
	return true
}

// advDamageMatches evaluates the damage-source predicate
// {"type": {"tags": [{"id": ..., "expected": ...}], "direct_entity": {...}}}.
func advDamageMatches(raw json.RawMessage, ctx advContext) bool {
	var dmg struct {
		Type *struct {
			Tags []struct {
				ID       string `json:"id"`
				Expected bool   `json:"expected"`
			} `json:"tags"`
			DirectEntity struct {
				EntityType string `json:"minecraft:entity_type"`
			} `json:"direct_entity"`
		} `json:"type"`
	}
	if err := json.Unmarshal(raw, &dmg); err != nil {
		return false
	}
	if dmg.Type == nil {
		return true
	}
	for _, tag := range dmg.Type.Tags {
		isProjectileTag := tag.ID == "minecraft:is_projectile"
		if isProjectileTag && tag.Expected != ctx.damageProjectile {
			return false
		}
	}
	if de := dmg.Type.DirectEntity.EntityType; de != "" {
		if !advEntityIDMatches(de, ctx.damageDirectEntity) {
			return false
		}
	}
	return true
}

// advItemPredicate evaluates {"items": pattern, "count": range} against
// one item event (consume_item).
func advItemPredicate(raw json.RawMessage, itemID, count int32) bool {
	var pred struct {
		Items json.RawMessage `json:"items"`
		Count *advRange       `json:"count"`
	}
	if err := json.Unmarshal(raw, &pred); err != nil {
		return false
	}
	name := itemNameOf(itemID)
	if name == "" {
		return false
	}
	if len(pred.Items) > 0 && !advStringOrList(pred.Items, name) {
		return false
	}
	if pred.Count != nil && !pred.Count.contains(float64(count)) {
		return false
	}
	return true
}

// advInventoryPredicate evaluates the inventory_changed "items" list:
// every entry must be satisfied by the combined inventory counts.
func advInventoryPredicate(raw json.RawMessage, inventory map[string]int32) bool {
	var list []struct {
		Items json.RawMessage `json:"items"`
		Count *advRange       `json:"count"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		return false
	}
	for _, entry := range list {
		total := int32(0)
		if len(entry.Items) > 0 {
			for name, count := range inventory {
				if advStringOrList(entry.Items, name) {
					total += count
				}
			}
		}
		want := int32(1)
		if entry.Count != nil && entry.Count.Min != nil {
			want = int32(*entry.Count.Min)
		}
		if total < want {
			return false
		}
	}
	return true
}

// advEffectsMatch evaluates the effects_changed "effects" map: every
// named effect must be active with the amplifier in range (empty
// predicate objects only demand presence).
func advEffectsMatch(raw json.RawMessage, ctx advContext) bool {
	var effects map[string]struct {
		Amplifier *advRange `json:"amplifier"`
		Duration  *advRange `json:"duration"`
	}
	if err := json.Unmarshal(raw, &effects); err != nil {
		return false
	}
	for name, pred := range effects {
		amp, active := ctx.effectNames[name]
		if !active {
			return false
		}
		if pred.Amplifier != nil && !pred.Amplifier.contains(float64(amp)) {
			return false
		}
	}
	return true
}

// advDistanceMatch evaluates the levitation/fall distance predicate:
// per-axis (y) ranges between the start and current position.
func advDistanceMatch(raw json.RawMessage, ctx advContext) bool {
	var dist struct {
		X        *advRange `json:"x"`
		Y        *advRange `json:"y"`
		Z        *advRange `json:"z"`
		Absolute *advRange `json:"absolute"`
	}
	if err := json.Unmarshal(raw, &dist); err != nil {
		return false
	}
	if !ctx.hasStart {
		return false
	}
	dy := math.Abs(ctx.currentY - ctx.startY)
	if dist.Y != nil && !dist.Y.contains(dy) {
		return false
	}
	if dist.Absolute != nil && !dist.Absolute.contains(dy) {
		return false
	}
	return true
}

// advStringOrList matches "name" | ["a","b"] | "#tag" patterns. Item
// tags resolve through the recipe tag tables.
func advStringOrList(raw json.RawMessage, value string) bool {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == value {
			return true
		}
		if len(s) > 0 && s[0] == '#' {
			for _, m := range tagMembers(s) {
				if m == value {
					return true
				}
			}
		}
		return false
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		for _, e := range list {
			if e == value {
				return true
			}
		}
	}
	return false
}

// --- event entry points (hooked from the feature files) -------------------

// advPlayerInventorySnapshot builds the name->count map used by
// inventory_changed. Caller holds Server.mu.
func advPlayerInventorySnapshot(p *player) map[string]int32 {
	inv := make(map[string]int32, 8)
	for _, s := range p.slots {
		if s.count <= 0 {
			continue
		}
		if name := itemNameOf(s.item); name != "" {
			inv[name] += s.count
		}
	}
	return inv
}

// advPlayerEffectsSnapshot builds the effect name->amplifier map used by
// effects_changed. Caller holds Server.mu.
func advPlayerEffectsSnapshot(p *player) map[string]int32 {
	out := make(map[string]int32, len(p.effects))
	for id, st := range p.effects {
		if name := effectNameOf(id); name != "" {
			out[name] = st.amp
		}
	}
	return out
}

// effectNameOf resolves an effect registry id to its name.
func effectNameOf(id int32) string {
	for i := range effectDefs {
		if effectDefs[i].id == id {
			return effectDefs[i].name
		}
	}
	return ""
}

// advEventItem fires the item-carrying triggers after an inventory
// change (pickup, craft take): inventory_changed.
// Caller holds Server.mu.
func (s *Server) advEventItemChanged(p *player, itemID int32) {
	if p == nil || p.adv == nil {
		return
	}
	ctx := advContext{
		player:    p,
		itemID:    itemID,
		itemCount: 1,
		inventory: advPlayerInventorySnapshot(p),
		biome:     "minecraft:plains",
	}
	s.advTrigger(p.adv, p, "minecraft:inventory_changed", ctx)
}

// advEventKill fires the kill triggers when a player kills an entity.
// projectile marks an arrow kill (killed_by_arrow listens separately
// from player_killed_entity).
// Caller holds Server.mu.
func (s *Server) advEventKill(killer *player, entityType string, projectile bool) {
	if killer == nil || killer.adv == nil {
		return
	}
	effects := advPlayerEffectsSnapshot(killer)
	s.advTrigger(killer.adv, killer, "minecraft:player_killed_entity", advContext{
		player:      killer,
		entityType:  entityType,
		biome:       "minecraft:plains",
		effectNames: effects,
	})
	if projectile {
		s.advTrigger(killer.adv, killer, "minecraft:killed_by_arrow", advContext{
			player:             killer,
			entityType:         entityType,
			damageProjectile:   true,
			damageDirectEntity: "minecraft:arrow",
			biome:              "minecraft:plains",
			effectNames:        effects,
		})
	}
}

// advEventHurtEntity fires player_hurt_entity for player-caused damage.
// Caller holds Server.mu.
func (s *Server) advEventHurtEntity(attacker *player, entityType string, projectile bool, directEntity string) {
	if attacker == nil || attacker.adv == nil {
		return
	}
	s.advTrigger(attacker.adv, attacker, "minecraft:player_hurt_entity", advContext{
		player:             attacker,
		entityType:         entityType,
		damageProjectile:   projectile,
		damageDirectEntity: directEntity,
		biome:              "minecraft:plains",
		effectNames:        advPlayerEffectsSnapshot(attacker),
	})
}

// advEventPlayerHurt fires entity_hurt_player when a player takes
// entity-caused damage.
// Caller holds Server.mu.
func (s *Server) advEventPlayerHurt(victim *player, entityType string) {
	if victim == nil || victim.adv == nil {
		return
	}
	s.advTrigger(victim.adv, victim, "minecraft:entity_hurt_player", advContext{
		player:      victim,
		entityType:  entityType,
		biome:       "minecraft:plains",
		effectNames: advPlayerEffectsSnapshot(victim),
	})
}

// advEventPlayerKilled fires entity_killed_player when a player dies to
// an entity.
// Caller holds Server.mu.
func (s *Server) advEventPlayerKilled(victim *player, entityType string) {
	if victim == nil || victim.adv == nil {
		return
	}
	s.advTrigger(victim.adv, victim, "minecraft:entity_killed_player", advContext{
		player:      victim,
		entityType:  entityType,
		biome:       "minecraft:plains",
		effectNames: advPlayerEffectsSnapshot(victim),
	})
}

// advEventConsume fires consume_item when the player finishes eating.
// Caller holds Server.mu.
func (s *Server) advEventConsume(p *player, itemID int32) {
	if p == nil || p.adv == nil {
		return
	}
	s.advTrigger(p.adv, p, "minecraft:consume_item", advContext{
		player:      p,
		itemID:      itemID,
		itemCount:   1,
		biome:       "minecraft:plains",
		effectNames: advPlayerEffectsSnapshot(p),
	})
}

// advEventPlace fires placed_block after a successful placement.
// Caller holds Server.mu.
func (s *Server) advEventPlace(p *player, blockName string) {
	if p == nil || p.adv == nil {
		return
	}
	s.advTrigger(p.adv, p, "minecraft:placed_block", advContext{
		player:      p,
		blockName:   blockName,
		biome:       "minecraft:plains",
		effectNames: advPlayerEffectsSnapshot(p),
	})
}

// advEventBrewed fires brewed_potion when the player takes a potion out
// of a brewing stand.
// Caller holds Server.mu.
func (s *Server) advEventBrewed(p *player, potionName string) {
	if p == nil || p.adv == nil {
		return
	}
	s.advTrigger(p.adv, p, "minecraft:brewed_potion", advContext{
		player:      p,
		potion:      potionName,
		biome:       "minecraft:plains",
		effectNames: advPlayerEffectsSnapshot(p),
	})
}

// advEventSlept fires slept_in_bed after a successful sleep.
// Caller holds Server.mu.
func (s *Server) advEventSlept(p *player) {
	if p == nil || p.adv == nil {
		return
	}
	s.advTrigger(p.adv, p, "minecraft:slept_in_bed", advContext{
		player:      p,
		biome:       "minecraft:plains",
		effectNames: advPlayerEffectsSnapshot(p),
	})
}

// advEventFall fires fall_from_height when a fall lands.
// Caller holds Server.mu.
func (s *Server) advEventFall(p *player, startY float64, hasStart bool) {
	if p == nil || p.adv == nil {
		return
	}
	s.advTrigger(p.adv, p, "minecraft:fall_from_height", advContext{
		player:      p,
		startY:      startY,
		hasStart:    hasStart,
		currentY:    p.y,
		biome:       "minecraft:plains",
		effectNames: advPlayerEffectsSnapshot(p),
	})
}

// advEventLevitation fires levitation while the effect lifts the player.
// Caller holds Server.mu.
func (s *Server) advEventLevitation(p *player, startY float64, hasStart bool) {
	if p == nil || p.adv == nil {
		return
	}
	s.advTrigger(p.adv, p, "minecraft:levitation", advContext{
		player:      p,
		startY:      startY,
		hasStart:    hasStart,
		currentY:    p.y,
		biome:       "minecraft:plains",
		effectNames: advPlayerEffectsSnapshot(p),
	})
}

// advEventEffectsChanged fires effects_changed after any effect change.
// Caller holds Server.mu.
func (s *Server) advEventEffectsChanged(p *player) {
	if p == nil || p.adv == nil {
		return
	}
	s.advTrigger(p.adv, p, "minecraft:effects_changed", advContext{
		player:      p,
		biome:       "minecraft:plains",
		effectNames: advPlayerEffectsSnapshot(p),
	})
}

// advEntityNameOfID resolves a damage cause/direct entity id to the
// "minecraft:<name>" type identifier for mob-attributed damage; empty
// for players, items and unknown ids. Caller holds Server.mu.
func advEntityNameOfID(s *Server, id int32) string {
	if id <= 0 {
		return ""
	}
	e, ok := s.entities[id]
	if !ok {
		return ""
	}
	if m, isMob := e.(*mobEntity); isMob {
		return "minecraft:" + m.def.name
	}
	return ""
}
