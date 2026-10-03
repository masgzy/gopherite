package server

// M17 advancement tests: tree integrity, layout, visibility rules,
// award/revoke semantics, packet encoding, persistence round-trip and
// trigger dispatch.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
)

// loadTestTree parses the embedded tree once per test binary.
func loadTestTree(t *testing.T) *advancementTree {
	t.Helper()
	tree, err := loadAdvancementTree()
	if err != nil {
		t.Fatalf("loadAdvancementTree: %v", err)
	}
	return tree
}

// newAdvServer builds a Server without binding a listener (the M17
// tests drive the model directly, no ticker).
func newAdvServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Options{
		ListenAddr:     "127.0.0.1:0",
		OnlineMode:     false,
		MOTD:           "adv-test",
		MaxPlayers:     20,
		VersionName:    "test",
		ProtocolNumber: 776,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if s.advTree == nil {
		t.Fatal("New did not load the advancement tree")
	}
	t.Cleanup(func() { _ = s.Shutdown(t.Context()) })
	return s
}

// bufConn is an in-memory transport capturing everything the server
// writes, standing in for a real client connection. The io.Copy sink
// locks on every Write so written() can read concurrently.
type bufConn struct {
	mu     sync.Mutex
	data   bytes.Buffer
	closed chan struct{}
	nc     net.Conn
}

func (b *bufConn) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.data.Write(p)
}

func newBufConn() (*bufConn, net.Conn) {
	client, server := net.Pipe()
	b := &bufConn{closed: make(chan struct{}), nc: client}
	go func() {
		defer close(b.closed)
		_, _ = io.Copy(b, server)
	}()
	return b, client
}

func (b *bufConn) written() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.data.Bytes()...)
}

// newAdvTestConn builds a conn wired to an in-memory transport.
func newAdvTestConn(t *testing.T, s *Server) (*conn, *bufConn) {
	t.Helper()
	b, client := newBufConn()
	c := &conn{s: s, nc: client, br: bufio.NewReader(client), bw: bufio.NewWriter(client), st: statePlay}
	c.fr = protocol.NewFrameReader(c.br, s.opts.MaxPacketLen)
	c.rd = &protocol.Reader{}
	c.wr = protocol.NewWriter()
	t.Cleanup(func() { _ = client.Close() })
	return c, b
}

func TestAdvancementTreeIntegrity(t *testing.T) {
	tree := loadTestTree(t)
	if len(tree.defs) != 126 {
		t.Fatalf("expected 126 vanilla advancements, got %d", len(tree.defs))
	}
	if len(tree.roots) != 5 {
		t.Fatalf("expected 5 tab roots, got %d", len(tree.roots))
	}
	roots := map[string]bool{}
	for _, r := range tree.roots {
		roots[r] = true
	}
	for _, want := range []string{"minecraft:story/root", "minecraft:nether/root", "minecraft:adventure/root", "minecraft:husbandry/root", "minecraft:end/root"} {
		if !roots[want] {
			t.Fatalf("missing root %s", want)
		}
	}
	// Every definition has non-empty requirements referencing its own
	// criteria.
	for id, def := range tree.defs {
		if len(def.criteria) == 0 {
			t.Fatalf("%s has no criteria", id)
		}
		names := map[string]bool{}
		for _, c := range def.criteria {
			names[c.name] = true
			if c.trigger == "" {
				t.Fatalf("%s criterion %s has no trigger", id, c.name)
			}
		}
		if len(def.requirements) == 0 {
			t.Fatalf("%s has no requirements", id)
		}
		for _, group := range def.requirements {
			for _, name := range group {
				if !names[name] {
					t.Fatalf("%s requirement references unknown criterion %s", id, name)
				}
			}
		}
	}
}

func TestAdvancementRootsUseRealTriggers(t *testing.T) {
	tree := loadTestTree(t)
	// 26.2 roots are real advancements: story/root completes by
	// crafting a crafting table (inventory_changed), adventure/root by
	// kill triggers.
	def := tree.defs["minecraft:story/root"]
	if len(def.criteria) != 1 || def.criteria[0].trigger != "minecraft:inventory_changed" {
		t.Fatalf("story/root criteria mismatch: %+v", def.criteria)
	}
	if def.criteria[0].name != "crafting_table" {
		t.Fatalf("story/root criterion should be crafting_table, got %s", def.criteria[0].name)
	}
	def2 := tree.defs["minecraft:adventure/root"]
	triggers := map[string]bool{}
	for _, c := range def2.criteria {
		triggers[c.trigger] = true
	}
	if !triggers["minecraft:player_killed_entity"] || !triggers["minecraft:entity_killed_player"] {
		t.Fatalf("adventure/root must reference kill triggers: %v", triggers)
	}
}

func TestAdvancementLayoutBounds(t *testing.T) {
	tree := loadTestTree(t)
	for _, id := range tree.order {
		def := tree.defs[id]
		if def.display == nil {
			continue
		}
		// Story tree depth sanity: coordinates inside the vanilla grid
		// (columns 0..8 — enter_the_end sits at depth 8, rows bounded
		// by the tab width).
		if def.display.x < 0 || def.display.x > 8 {
			t.Fatalf("%s has x=%f outside 0..8", id, def.display.x)
		}
		if def.display.y < -40 || def.display.y > 40 {
			t.Fatalf("%s has y=%f outside sane bounds", id, def.display.y)
		}
	}
	// Root sits at column 0; the walk normalizes the minimum row to 0
	// (which may belong to a deep child, not the root — vanilla
	// secondWalk/thirdWalk semantics).
	root := tree.defs["minecraft:story/root"]
	if root.display.x != 0 {
		t.Fatalf("story root at x=%f, want 0", root.display.x)
	}
	minY := float32(1e9)
	for _, id := range tree.order {
		d := tree.defs[id]
		if d.display != nil && id != "minecraft:story/root" && tree.rootOf(id) == "minecraft:story/root" {
			if d.display.y < minY {
				minY = d.display.y
			}
		}
	}
	if minY != 0 {
		t.Fatalf("story tab minimum row is %f, want 0", minY)
	}
	// Direct children share column 1 with distinct rows.
	seen := map[float32]string{}
	for _, child := range tree.children["minecraft:story/root"] {
		d := tree.defs[child].display
		if d.x != 1 {
			t.Fatalf("root child %s at x=%f, want 1", child, d.x)
		}
		if prev, ok := seen[d.y]; ok {
			t.Fatalf("root children %s and %s share row %f", prev, child, d.y)
		}
		seen[d.y] = child
	}
}

func TestAdvancementVisibilityRules(t *testing.T) {
	tree := loadTestTree(t)
	done := map[string]bool{}
	done["minecraft:story/root"] = true // "Minecraft": craft a table

	var added, removed []string
	tree.updateTreeVisibility("minecraft:story/root",
		func(id string) bool { return done[id] },
		func(id string) { added = append(added, id) },
		func(id string) { removed = append(removed, id) })

	contains := func(list []string, id string) bool {
		for _, v := range list {
			if v == id {
				return true
			}
		}
		return false
	}
	// Root and its depth-1/depth-2 descendants are visible: the rule
	// window spans two generations past a done node.
	if !contains(added, "minecraft:story/root") {
		t.Fatal("root must be visible when done")
	}
	if len(tree.children["minecraft:story/root"]) == 0 {
		t.Fatal("story root should have children")
	}
	for _, child := range tree.children["minecraft:story/root"] {
		if !contains(added, child) {
			t.Fatalf("root child %s must be visible", child)
		}
	}
	// enter_the_end sits at depth 8: without any intermediate
	// completions it must stay hidden.
	if contains(added, "minecraft:story/enter_the_end") {
		t.Fatal("enter_the_end must stay hidden at depth 8 with only the root done")
	}
	// Invisible nodes come back through the removal channel.
	if !contains(removed, "minecraft:story/enter_the_end") {
		t.Fatal("hidden nodes must be reported through the removal callback")
	}
}

func TestAdvancementAwardRevokeAndFlush(t *testing.T) {
	s := newAdvServer(t)
	pa := newPlayerAdvancements(s.advTree)
	c, b := newAdvTestConn(t, s)
	p := &player{conn: c, name: "tester", id: 1}
	s.mu.Lock()
	p.adv = pa
	for _, id := range pa.tree.order {
		pa.registerListeners(id)
	}
	// A fresh player has nothing visible: 26.2 roots are real
	// advancements, so the first flush sends nothing (matching the
	// vanilla visibility evaluator).
	s.advFlushDirty(pa, c)
	s.mu.Unlock()
	if len(b.written()) != 0 {
		t.Fatal("fresh player flush must send nothing")
	}

	// Crafting a table completes story/root and makes the story tab
	// (root + two generations) visible.
	s.mu.Lock()
	if !s.advAward(pa, p, "minecraft:story/root", "crafting_table") {
		t.Fatal("awarding crafting_table must flip the criterion")
	}
	// Unknown criteria are rejected like vanilla grantProgress.
	if s.advAward(pa, p, "minecraft:story/root", "nonexistent") {
		t.Fatal("unknown criterion must no-op")
	}
	s.advFlushDirty(pa, c)
	s.mu.Unlock()

	// written() 读取与 io.Copy 协程落盘存在调度间隙（net.Pipe 同步写
	// 返回后，拷贝协程还要持锁追加进缓冲），所以这里轮询而不是单次
	// 判空——旧写法约 7% 的概率误报空包。
	if !waitFor(t, 2*time.Second, func() bool { return len(b.written()) > 0 }) {
		t.Fatal("root completion flush must send the tree packet")
	}
	if !pa.getOrStartProgress("minecraft:story/root").isDone() {
		t.Fatal("root must be done after crafting_table award")
	}

	// Revoke everything on the root (commands path).
	s.mu.Lock()
	count := s.advApplyToAllCriteria(pa, p, "minecraft:story/root", false)
	s.mu.Unlock()
	if count != 1 {
		t.Fatalf("expected 1 criterion revoke, got %d", count)
	}
	if pa.getOrStartProgress("minecraft:story/root").isDone() {
		t.Fatal("root must be undone after revoke")
	}
}

func TestAdvancementInventoryChangedTrigger(t *testing.T) {
	s := newAdvServer(t)
	pa := newPlayerAdvancements(s.advTree)
	c, b := newAdvTestConn(t, s)
	p := &player{conn: c, name: "tester", id: 1}
	s.mu.Lock()
	p.adv = pa
	for _, id := range pa.tree.order {
		pa.registerListeners(id)
	}
	// Put a crafting table into the inventory and fire
	// inventory_changed: story/root ("Minecraft") completes, which
	// reveals the first two generations of the story tab.
	p.slots[0] = invSlot{item: itemIDByName["minecraft:crafting_table"], count: 1}
	s.advEventItemChanged(p, itemIDByName["minecraft:crafting_table"])
	s.advFlushDirty(pa, c)
	s.mu.Unlock()

	if !pa.getOrStartProgress("minecraft:story/root").isDone() {
		t.Fatal("story/root must complete via crafting_table in inventory")
	}
	// The tree packet was flushed to the transport.
	// 同 TestAdvancementAwardRevokeAndFlush：written() 有与 io.Copy
	// 协程的调度间隙，必须轮询判定。
	if !waitFor(t, 2*time.Second, func() bool { return len(b.written()) > 0 }) {
		t.Fatal("flush must produce wire bytes")
	}
}

func TestAdvancementPacketEncoding(t *testing.T) {
	tree := loadTestTree(t)
	def := tree.wireDef("minecraft:story/root")
	if def.ID != "minecraft:story/root" || def.Parent != "" || !def.HasDisplay {
		t.Fatalf("wire def mismatch: %+v", def)
	}
	if def.Frame != 0 {
		t.Fatalf("story/root frame should be task(0), got %d", def.Frame)
	}
	// Challenge frame ordinal check via a known challenge.
	challenge := tree.wireDef("minecraft:nether/all_effects")
	if challenge.Frame != 1 {
		t.Fatalf("all_effects should be challenge(1), got %d", challenge.Frame)
	}

	// Byte-level: reset + one root + one progress entry round-trips.
	w := protocol.NewWriter()
	java.WriteUpdateAdvancements(w, true,
		[]java.AdvancementDef{def},
		nil,
		[]java.AdvancementProgressWire{{
			ID: "minecraft:story/root",
			Criteria: map[string]java.CriterionProgressWire{
				"minecraft:tick": {Obtained: 1234567890},
			},
		}},
		true)

	// Decode back by hand: boolean, count, id string, optional parent
	// (absent), optional display (present) — verify a few landmarks.
	r := protocol.NewReader(w.Bytes())
	if got, err := r.Bool(); err != nil || !got {
		t.Fatalf("reset flag: %v %v", got, err)
	}
	count, err := r.VarInt()
	if err != nil || count != 1 {
		t.Fatalf("added count: %d %v", count, err)
	}
	id, err := r.String(32767)
	if err != nil || id != "minecraft:story/root" {
		t.Fatalf("id: %q %v", id, err)
	}
	hasParent, err := r.Bool()
	if err != nil || hasParent {
		t.Fatalf("root parent must be absent: %v %v", hasParent, err)
	}
	hasDisplay, err := r.Bool()
	if err != nil || !hasDisplay {
		t.Fatalf("display must be present: %v %v", hasDisplay, err)
	}
	// title component: NBT compound — first byte is the tag type.
	if _, err := r.Byte(); err != nil {
		t.Fatalf("title tag type: %v", err)
	}
	if _, err := r.Uint16(); err != nil {
		t.Fatalf("title name length: %v", err)
	}
}

func TestAdvancementPersistenceRoundTrip(t *testing.T) {
	s := newAdvServer(t)
	s.opts.LevelName = t.TempDir()

	pa := newPlayerAdvancements(s.advTree)
	c, _ := newAdvTestConn(t, s)
	p := &player{conn: c, name: "p", id: 1}
	var profile [16]byte
	copy(profile[:], "0123456789abcdef")

	s.mu.Lock()
	p.adv = pa
	s.advAward(pa, p, "minecraft:story/root", "crafting_table")
	s.mu.Unlock()
	s.advSave(pa, profile)

	pa2 := newPlayerAdvancements(s.advTree)
	s.advLoad(pa2, profile)
	if !pa2.getOrStartProgress("minecraft:story/root").isDone() {
		t.Fatal("story/root must be done after reload")
	}
}

func TestAdvancementTriggerConditionsMatch(t *testing.T) {
	// entity condition list (kill_a_mob shape).
	conds := json.RawMessage(`{"entity": [{"condition": "minecraft:entity_properties", "entity": "this", "predicate": {"minecraft:entity_type": "minecraft:creeper"}}]}`)
	ctx := advContext{entityType: "minecraft:creeper"}
	if !advConditionsMatch(conds, ctx) {
		t.Fatal("creeper kill must match creeper predicate")
	}
	if advConditionsMatch(conds, advContext{entityType: "minecraft:zombie"}) {
		t.Fatal("zombie must not match creeper predicate")
	}
	// Empty conditions always match.
	if !advConditionsMatch(json.RawMessage(`{}`), advContext{}) {
		t.Fatal("empty conditions must match")
	}
	// damage source tags (killed_by_arrow / shoot_arrow shape).
	dmg := json.RawMessage(`{"damage": {"type": {"tags": [{"expected": true, "id": "minecraft:is_projectile"}], "direct_entity": {"minecraft:entity_type": "#minecraft:arrows"}}}}`)
	if !advConditionsMatch(dmg, advContext{damageProjectile: true, damageDirectEntity: "minecraft:arrow"}) {
		t.Fatal("arrow damage must match projectile condition")
	}
	if advConditionsMatch(dmg, advContext{damageProjectile: false, damageDirectEntity: ""}) {
		t.Fatal("melee must not match projectile condition")
	}
	// inventory items with tag pattern.
	inv := json.RawMessage(`{"items": [{"items": "#minecraft:logs", "count": {"min": 1}}]}`)
	if !advConditionsMatch(inv, advContext{inventory: map[string]int32{"minecraft:oak_log": 2}}) {
		t.Fatal("oak_log must satisfy #minecraft:logs")
	}
	if advConditionsMatch(inv, advContext{inventory: map[string]int32{"minecraft:dirt": 64}}) {
		t.Fatal("dirt must not satisfy #minecraft:logs")
	}
	// distance (levitation shape).
	lev := json.RawMessage(`{"distance": {"y": {"min": 50.0}}}`)
	if !advConditionsMatch(lev, advContext{hasStart: true, startY: 0, currentY: 51}) {
		t.Fatal("51 blocks of rise must match y>=50")
	}
	if advConditionsMatch(lev, advContext{hasStart: true, startY: 0, currentY: 10}) {
		t.Fatal("10 blocks of rise must not match y>=50")
	}
}
