package server

// M17 advancement tree: the embedded vanilla 26.2 advancement
// definitions (server/assets/advancements, extracted from the official
// data pack), the client-facing tree layout (exact TreeNodePosition
// port) and the server-side visibility rules
// (AdvancementVisibilityEvaluator port).

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
)

//go:embed assets/advancements
var advancementFS embed.FS

// advCriterion is one named criterion of an advancement: its trigger
// identifier plus the raw conditions document (lazily interpreted by the
// trigger engine).
type advCriterion struct {
	name       string
	trigger    string
	conditions json.RawMessage
}

// advDisplay mirrors the vanilla DisplayInfo JSON fields the server
// forwards to the client.
type advDisplay struct {
	title        string
	description  string
	icon         string // item identifier, "minecraft:..."
	iconCount    int32
	frame        string // task | challenge | goal
	background   string // "" = none
	showToast    bool
	announceChat bool
	hidden       bool
	x, y         float32 // filled by the layout pass
}

// advDef is one advancement definition (vanilla advancement JSON).
type advDef struct {
	id             string
	parent         string
	display        *advDisplay
	experience     int
	rewardLoot     []string
	rewardRecipes  []string
	criteria       []advCriterion
	requirements   [][]string // AND-of-OR groups; computed when absent
	sendsTelemetry bool
}

// criterion returns the named criterion.
func (a *advDef) criterion(name string) *advCriterion {
	for i := range a.criteria {
		if a.criteria[i].name == name {
			return &a.criteria[i]
		}
	}
	return nil
}

// advancementTree is the parsed, parent-resolved tree of all embedded
// definitions with display-bearing nodes laid out like the vanilla
// server does.
type advancementTree struct {
	defs     map[string]*advDef
	order    []string // deterministic definition order (sorted paths)
	children map[string][]string
	roots    []string // parentless ids, definition order
}

// loadAdvancementTree parses every embedded JSON, resolves parents and
// computes the display layout. Missing parents or cycles abort startup:
// a broken tree would render wrong on every client.
func loadAdvancementTree() (*advancementTree, error) {
	t := &advancementTree{
		defs:     make(map[string]*advDef),
		children: make(map[string][]string),
	}
	paths, err := fs.Glob(advancementFS, "assets/advancements/**/*.json")
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("advancement: no embedded definitions")
	}
	sort.Strings(paths)

	// The id is the datapack path without the advancement/ prefix:
	// assets/advancements/story/root.json -> minecraft:story/root.
	for _, p := range paths {
		data, err := advancementFS.ReadFile(p)
		if err != nil {
			return nil, fmt.Errorf("advancement: read %s: %w", p, err)
		}
		def, err := parseAdvancementJSON(data)
		if err != nil {
			return nil, fmt.Errorf("advancement: %s: %w", p, err)
		}
		rel := trimAdvancementPath(p)
		def.id = "minecraft:" + rel
		if def.requirements == nil {
			// Vanilla default: one AND group per criterion.
			for _, c := range def.criteria {
				def.requirements = append(def.requirements, []string{c.name})
			}
		} else {
			def.requirements = normaliseRequirements(def.requirements, def.criteria)
		}
		t.defs[def.id] = def
		t.order = append(t.order, def.id)
	}

	// Parent/child links and root discovery, in definition order.
	for _, id := range t.order {
		def := t.defs[id]
		if def.parent == "" {
			t.roots = append(t.roots, id)
			continue
		}
		parent, ok := t.defs[def.parent]
		if !ok {
			return nil, fmt.Errorf("advancement: %s references unknown parent %s", id, def.parent)
		}
		_ = parent
		t.children[def.parent] = append(t.children[def.parent], id)
	}
	// Cycle guard: every node must be reachable from a root.
	reachable := make(map[string]bool, len(t.order))
	var walk func(id string)
	walk = func(id string) {
		if reachable[id] {
			return
		}
		reachable[id] = true
		for _, ch := range t.children[id] {
			walk(ch)
		}
	}
	for _, r := range t.roots {
		walk(r)
	}
	if len(reachable) != len(t.order) {
		return nil, fmt.Errorf("advancement: %d definitions form a cycle or an orphan chain", len(t.order)-len(reachable))
	}

	t.layout()
	return t, nil
}

// parseAdvancementJSON decodes one vanilla advancement document: the
// subset of fields the server needs (display, criteria, requirements,
// rewards).
func parseAdvancementJSON(data []byte) (*advDef, error) {
	var raw struct {
		Parent  string `json:"parent"`
		Display *struct {
			Icon struct {
				ID    string `json:"id"`
				Count int32  `json:"count"`
			} `json:"icon"`
			// Vanilla titles/descriptions are {"translate": key} objects;
			// keep them raw and extract the key below.
			Title       json.RawMessage `json:"title"`
			Description json.RawMessage `json:"description"`
			Frame       string          `json:"frame"`
			Background  string          `json:"background"`
			ShowToast   *bool           `json:"show_toast"`
			Announce    *bool           `json:"announce_to_chat"`
			Hidden      bool            `json:"hidden"`
		} `json:"display"`
		Rewards *struct {
			Experience int      `json:"experience"`
			Loot       []string `json:"loot"`
			Recipes    []string `json:"recipes"`
			Function   string   `json:"function"`
		} `json:"rewards"`
		Criteria map[string]struct {
			Trigger    string          `json:"trigger"`
			Conditions json.RawMessage `json:"conditions"`
		} `json:"criteria"`
		Requirements   [][]string `json:"requirements"`
		SendsTelemetry bool       `json:"sends_telemetry_event"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	def := &advDef{
		parent:         raw.Parent,
		sendsTelemetry: raw.SendsTelemetry,
	}
	// Criterion map -> deterministic slice (JSON object order is lost by
	// encoding/json; the vanilla server keeps datapack order but the wire
	// only carries requirements, so sorted names are equivalent).
	names := make([]string, 0, len(raw.Criteria))
	for name := range raw.Criteria {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		c := raw.Criteria[name]
		conditions := c.Conditions
		if len(conditions) == 0 {
			conditions = json.RawMessage("{}")
		}
		def.criteria = append(def.criteria, advCriterion{
			name:       name,
			trigger:    c.Trigger,
			conditions: conditions,
		})
	}
	def.requirements = raw.Requirements
	if raw.Rewards != nil {
		def.experience = raw.Rewards.Experience
		def.rewardLoot = raw.Rewards.Loot
		def.rewardRecipes = raw.Rewards.Recipes
	}
	if raw.Display != nil {
		d := raw.Display
		title := translateKeyOf(d.Title)
		desc := translateKeyOf(d.Description)
		if title == "" || desc == "" {
			return nil, fmt.Errorf("display without resolvable title/description")
		}
		frame := d.Frame
		if frame == "" {
			frame = "task"
		}
		count := d.Icon.Count
		if count == 0 {
			count = 1
		}
		showToast := true
		announce := true
		if d.ShowToast != nil {
			showToast = *d.ShowToast
		}
		if d.Announce != nil {
			announce = *d.Announce
		}
		def.display = &advDisplay{
			title:        title,
			description:  desc,
			icon:         d.Icon.ID,
			iconCount:    count,
			frame:        frame,
			background:   d.Background,
			showToast:    showToast,
			announceChat: announce,
			hidden:       d.Hidden,
		}
	}
	return def, nil
}

// translateKeyOf extracts {"translate": ...} from a raw component JSON
// value, falling back to a plain {"text": ...} or bare string.
func translateKeyOf(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return ""
	}
	if t, ok := obj["translate"]; ok {
		var key string
		if json.Unmarshal(t, &key) == nil {
			return key
		}
	}
	if t, ok := obj["text"]; ok {
		var key string
		if json.Unmarshal(t, &key) == nil {
			return key
		}
	}
	return ""
}

// trimAdvancementPath strips the assets/advancements prefix and .json
// suffix from an embedded path.
func trimAdvancementPath(p string) string {
	const prefix = "assets/advancements/"
	const suffix = ".json"
	s := p
	if len(s) >= len(prefix) && s[:len(prefix)] == prefix {
		s = s[len(prefix):]
	}
	if len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix {
		s = s[:len(s)-len(suffix)]
	}
	return s
}

// normaliseRequirements validates the requirements groups against the
// criteria set (vanilla AdvancementRequirements.validate: the referenced
// names must equal the criteria set exactly).
func normaliseRequirements(reqs [][]string, criteria []advCriterion) [][]string {
	set := make(map[string]bool, len(criteria))
	for _, c := range criteria {
		set[c.name] = true
	}
	for _, group := range reqs {
		for _, name := range group {
			if !set[name] {
				// Malformed datapack: drop back to the AND default rather
				// than reference criteria the client cannot see progress
				// for. Unreachable for the embedded vanilla tree.
				out := make([][]string, 0, len(criteria))
				for _, c := range criteria {
					out = append(out, []string{c.name})
				}
				return out
			}
		}
	}
	return reqs
}

// layout ports AdvancementTree + TreeNodePosition.run: the classic
// Reingold-Tilford walk the vanilla server runs per root, producing the
// (column, row) coordinates every client renders.
func (t *advancementTree) layout() {
	for _, rootID := range t.roots {
		rootDef := t.defs[rootID]
		if rootDef.display == nil {
			continue // invisible root: nothing to position
		}
		root := t.buildLayoutNode(rootID, nil, nil, 1, 0)
		if root == nil {
			continue
		}
		root.firstWalk()
		min := root.secondWalk(0, 0, root.y)
		if min < 0 {
			root.thirdWalk(-min)
		}
		root.finalizePosition()
	}
}

// layoutNode mirrors one TreeNodePosition instance.
type layoutNode struct {
	advID           string
	parent          *layoutNode
	previousSibling *layoutNode
	childIndex      int
	children        []*layoutNode
	ancestor        *layoutNode
	thread          *layoutNode
	x               int
	y               float32
	mod             float32
	change          float32
	shift           float32
	tree            *advancementTree
}

// buildLayoutNode mirrors the TreeNodePosition constructor: only
// display-bearing nodes join the layout; invisible descendants are
// lifted to their nearest visible ancestor.
func (t *advancementTree) buildLayoutNode(id string, parent, prevSibling *layoutNode, childIndex, depth int) *layoutNode {
	def := t.defs[id]
	if def == nil || def.display == nil {
		return nil
	}
	n := &layoutNode{
		advID:           id,
		parent:          parent,
		previousSibling: prevSibling,
		childIndex:      childIndex,
		ancestor:        nil, // set to self below like vanilla
		x:               depth,
		y:               -1.0,
		tree:            t,
	}
	n.ancestor = n
	var previous *layoutNode
	for _, childID := range t.children[id] {
		previous = t.addChild(n, childID, previous)
	}
	return n
}

func (t *advancementTree) addChild(parent *layoutNode, childID string, previous *layoutNode) *layoutNode {
	def := t.defs[childID]
	if def != nil && def.display != nil {
		previous = t.buildLayoutNode(childID, parent, previous, len(parent.children)+1, parent.x+1)
		if previous != nil {
			parent.children = append(parent.children, previous)
		}
		return previous
	}
	// Invisible node: recurse into its grandchildren.
	for _, grandchild := range t.children[childID] {
		previous = t.addChild(parent, grandchild, previous)
	}
	return previous
}

func (n *layoutNode) firstWalk() {
	if len(n.children) == 0 {
		if n.previousSibling != nil {
			n.y = n.previousSibling.y + 1.0
		} else {
			n.y = 0.0
		}
		return
	}
	defaultAncestor := (*layoutNode)(nil)
	for _, child := range n.children {
		child.firstWalk()
		if defaultAncestor == nil {
			defaultAncestor = child.apportion(child)
		} else {
			defaultAncestor = child.apportion(defaultAncestor)
		}
	}
	n.executeShifts()
	midpoint := (n.children[0].y + n.children[len(n.children)-1].y) / 2.0
	if n.previousSibling != nil {
		n.y = n.previousSibling.y + 1.0
		n.mod = n.y - midpoint
	} else {
		n.y = midpoint
	}
}

func (n *layoutNode) secondWalk(modSum float32, depth int, min float32) float32 {
	n.y += modSum
	n.x = depth
	if n.y < min {
		min = n.y
	}
	for _, child := range n.children {
		min = child.secondWalk(modSum+n.mod, depth+1, min)
	}
	return min
}

func (n *layoutNode) thirdWalk(offset float32) {
	n.y += offset
	for _, child := range n.children {
		child.thirdWalk(offset)
	}
}

func (n *layoutNode) executeShifts() {
	shift := float32(0)
	change := float32(0)
	for i := len(n.children) - 1; i >= 0; i-- {
		child := n.children[i]
		child.y += shift
		child.mod += shift
		change += child.change
		shift += child.shift + change
	}
}

func (n *layoutNode) previousOrThread() *layoutNode {
	if n.thread != nil {
		return n.thread
	}
	if len(n.children) > 0 {
		return n.children[0]
	}
	return nil
}

func (n *layoutNode) nextOrThread() *layoutNode {
	if n.thread != nil {
		return n.thread
	}
	if len(n.children) > 0 {
		return n.children[len(n.children)-1]
	}
	return nil
}

func (n *layoutNode) apportion(defaultAncestor *layoutNode) *layoutNode {
	if n.previousSibling == nil || n.parent == nil {
		return defaultAncestor
	}
	vir := n
	vor := n
	vil := n.previousSibling
	vol := n.parent.children[0]
	sir := n.mod
	sor := n.mod
	sil := vil.mod
	sol := vol.mod
	for vil.nextOrThread() != nil && vir.previousOrThread() != nil {
		vil = vil.nextOrThread()
		vir = vir.previousOrThread()
		vol = vol.previousOrThread()
		vor = vor.nextOrThread()
		vor.ancestor = n
		shift := vil.y + sil - (vir.y + sir) + 1.0
		if shift > 0.0 {
			vil.getAncestor(n, defaultAncestor).moveSubtree(n, shift)
			sir += shift
			sor += shift
		}
		sil += vil.mod
		sir += vir.mod
		sol += vol.mod
		// The vanilla loop's post statement: sor += vor.mod (current vor).
		sor += vor.mod
	}
	if vil.nextOrThread() != nil && vor.nextOrThread() == nil {
		vor.thread = vil.nextOrThread()
		vor.mod += sil - sor
	} else {
		if vir.previousOrThread() != nil && vol.previousOrThread() == nil {
			vol.thread = vir.previousOrThread()
			vol.mod += sir - sol
		}
		defaultAncestor = n
	}
	return defaultAncestor
}

func (n *layoutNode) moveSubtree(right *layoutNode, shift float32) {
	subtrees := float32(right.childIndex - n.childIndex)
	if subtrees != 0.0 {
		right.change -= shift / subtrees
		n.change += shift / subtrees
	}
	right.shift += shift
	right.y += shift
	right.mod += shift
}

func (n *layoutNode) getAncestor(other, defaultAncestor *layoutNode) *layoutNode {
	if n.ancestor != nil && n.ancestor.parent == other.parent {
		return n.ancestor
	}
	return defaultAncestor
}

// finalizePosition writes the computed coordinates back into the
// display definitions.
func (n *layoutNode) finalizePosition() {
	if def := n.tree.defs[n.advID]; def != nil && def.display != nil {
		def.display.x = float32(n.x)
		def.display.y = n.y
	}
	for _, child := range n.children {
		child.finalizePosition()
	}
}

// visibilityRule mirrors AdvancementVisibilityEvaluator.VisibilityRule.
type visibilityRule int8

const (
	visHide visibilityRule = iota
	visNoChange
	visShow
)

// updateTreeVisibility ports AdvancementVisibilityEvaluator.evaluateVisibility:
// a node is visible when it (or any descendant) is done, or when a rule
// within the two-ancestor window says show; display-less and hidden
// nodes hide. add/remove receive every transition so the flush can
// incrementally sync the client.
func (t *advancementTree) updateTreeVisibility(rootID string, done func(id string) bool, add func(id string), remove func(id string)) {
	// Three NO_CHANGE sentinels under the real rules (vanilla pushes the
	// sentinel stack before walking).
	stack := []visibilityRule{visNoChange, visNoChange, visNoChange}
	var walk func(id string) bool // returns isSelfOrDescendantDone
	walk = func(id string) bool {
		def := t.defs[id]
		if def == nil {
			return false
		}
		isSelfDone := done(id)
		rule := visNoChange
		if def.display == nil {
			rule = visHide
		} else if isSelfDone {
			rule = visShow
		} else if def.display.hidden {
			rule = visHide
		}
		stack = append(stack, rule)
		selfOrDescDone := isSelfDone
		for _, child := range t.children[id] {
			if walk(child) {
				selfOrDescDone = true
			}
		}
		visible := selfOrDescDone
		if !visible {
			// evaluateVisiblityForUnfinishedNode: peek(0)..peek(2).
			for i := 0; i <= 2; i++ {
				r := stack[len(stack)-1-i]
				if r == visShow {
					visible = true
					break
				}
				if r == visHide {
					break
				}
			}
		}
		stack = stack[:len(stack)-1]
		if visible {
			add(id)
		} else {
			remove(id)
		}
		return selfOrDescDone
	}
	walk(rootID)
}

// descendants returns the advancement and its whole subtree (vanilla
// /advancement from / through semantics), in deterministic order.
func (t *advancementTree) descendants(id string) []string {
	out := []string{id}
	var walk func(cur string)
	walk = func(cur string) {
		kids := append([]string(nil), t.children[cur]...)
		sort.Strings(kids)
		for _, k := range kids {
			out = append(out, k)
			walk(k)
		}
	}
	walk(id)
	return out
}

// ancestors returns the advancement and its parent chain up to the root
// (vanilla /advancement until semantics), root first.
func (t *advancementTree) ancestors(id string) []string {
	chain := []string{id}
	cur := id
	for {
		def := t.defs[cur]
		if def == nil || def.parent == "" {
			break
		}
		cur = def.parent
		chain = append([]string{cur}, chain...)
	}
	return chain
}
