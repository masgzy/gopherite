// Package server implements the Gopherite server core: connection
// lifecycle, the handshake/status/login state machine and the public
// entry points used by cmd/gopherite.
package server

import (
	"context"
	"fmt"
	"log"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
)

// Options configures a Server. Field defaults come from config.Config.
type Options struct {
	// ListenAddr is the host:port to bind, e.g. ":25565".
	ListenAddr string

	// MOTD is the message shown in the server list.
	MOTD string

	// MaxPlayers caps concurrent joins (advertised in the status ping).
	MaxPlayers int

	// OnlineMode enables Mojang session validation (login phase, M2).
	// It is advertised and persisted now so operators can configure the
	// final behaviour ahead of the milestone that implements it.
	OnlineMode bool

	// SkipSessionAuth performs the full encryption handshake but skips the
	// Mojang hasJoined call, deriving the offline UUID instead. It exists
	// so integration tests can exercise the encrypted path without real
	// accounts; production configs never set it.
	SkipSessionAuth bool

	// VersionName and ProtocolNumber are advertised in the status ping.
	VersionName    string
	ProtocolNumber int32

	// FaviconPath points to a 64x64 PNG shown in the server list; empty
	// disables the icon.
	FaviconPath string

	// MaxPacketLen bounds a single framed packet payload.
	MaxPacketLen int32

	// ReadTimeoutSeconds bounds how long a connection may stay silent in
	// the handshake and status states.
	ReadTimeoutSeconds int

	// WriteTimeoutSeconds bounds a single packet write. A client that
	// stops reading fills its TCP send buffer and would otherwise block
	// every lock-held broadcast forever; when the deadline trips, the
	// write is abandoned and the connection closed.
	WriteTimeoutSeconds int

	// ViewDistance is the server-side chunk radius sent to clients.
	ViewDistance int

	// LevelName is the world directory under the working dir (vanilla
	// server.properties level-name). Empty disables persistence — used
	// by tests.
	LevelName string

	// KeepAliveInterval overrides the 15s keep-alive period (tests).
	// Zero means the vanilla default.
	KeepAliveInterval time.Duration

	// SessionServerURL overrides the Mojang session server base used for
	// online-mode hasJoined verification. Empty means the production
	// endpoint; tests point this at a stub.
	SessionServerURL string

	// 智能 GC 调控（Gopherite 扩展）。GCTuning 关闭时不启动调控器。
	GCTuning         bool
	GCTargetPauseMS  int
	GCMemLimitMiB    int64
	GCMinGOGC        int
	GCMaxGOGC        int
	GCBaseGOGC       int
	GCSampleInterval int
}

// Server accepts connections and drives the per-connection state machine.
type Server struct {
	opts Options

	ln      net.Listener
	mu      sync.Mutex
	wg      sync.WaitGroup
	closing atomic.Bool
	favicon string // data URI, computed once at start

	// stop is closed on Shutdown so background loops (autosave) can
	// exit before the final flush.
	stop chan struct{}

	// timeTicks is the overworld clock (game time in ticks), guarded by mu.
	timeTicks int64

	// teleportSeq mints teleport ids, guarded by mu.
	teleportSeq int32

	// started closes once Serve registered its background workers with
	// the WaitGroup; shutdown paths must observe it before Wait so the
	// Add/Wait pair stays ordered.
	started chan struct{}

	// keys is the login RSA keypair, generated lazily on first online-mode
	// login and reused for the server lifetime.
	keys *protocol.KeyPair

	// world is the M2 superflat overworld.
	world *world

	// stats is the rolling tick-timing recorder behind /tps and
	// /tpsbar; written only from the ticker goroutine.
	stats tickStats

	// tickCount drives the 1 Hz tpsbar refresh cadence; ticker-only.
	tickCount int64

	// players are the joined, in-play players; guarded by mu together
	// with player.seen, player.seenEnt and player.mining (the ticker
	// touches both).
	players map[*conn]*player

	// entities holds every live tracked entity (M5); guarded by mu.
	entities map[int32]entity

	// blockEnts holds the placed container block entities (M10: chest,
	// furnace); guarded by mu like the rest of the model.
	blockEnts map[[3]int]*blockEntity

	// nextEntityID is the vanilla entity id counter; players included.
	nextEntityID atomic.Int32

	// gc 是智能 GC 调控器；nil 表示未启用（GCTuning=false 或测试）。
	gc *gcTuner

	// M12 天气：全局状态与剩余 ticks（guarded by mu，ticker 专用写）。
	weather           int32 // weatherClear / weatherRain / weatherThunder
	weatherTicks      int64
	lastLoggedWeather int32
}

// playerListLocked returns the joined players; caller holds mu.
// nextTeleportID mints sequential teleport ids. Caller holds mu.
func (s *Server) nextTeleportID() int32 {
	s.teleportSeq++
	return s.teleportSeq
}

// broadcastSystemChat sends a chat message to every connected player.
// Caller holds no lock.
func (s *Server) broadcastSystemChat(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		if err := p.conn.sendSystemChat(text); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) playerListLocked() []*player {
	out := make([]*player, 0, len(s.players))
	for _, p := range s.players {
		out = append(out, p)
	}
	return out
}

// addPlayer registers a joined player for ticking and broadcasts.
func (s *Server) addPlayer(p *player) {
	s.mu.Lock()
	s.players[p.conn] = p
	s.mu.Unlock()
}

// removePlayer unregisters a disconnecting player and retires them from
// every other client's tab list and view.
func (s *Server) removePlayer(c *conn) {
	s.mu.Lock()
	p := s.players[c]
	delete(s.players, c)
	s.mu.Unlock()
	if p != nil {
		s.broadcastRemovePlayer(p)
	}
}

// startTicker runs the 20 TPS server tick loop until shutdown. M3 uses
// it for mining progress; later milestones extend it to world ticks.
func (s *Server) startTicker() {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				if s.closing.Load() {
					return
				}
				s.tickOnce()
			}
		}
	}()
}

// tickOnce advances per-tick state: mining progress today, plus the
// tick-timing record and the 1 Hz tpsbar refresh. Runs on the ticker
// goroutine only.
func (s *Server) tickOnce() {
	start := time.Now()

	s.mu.Lock()
	var digging []*player
	var bars []*player
	for _, p := range s.players {
		if p.mining != nil {
			digging = append(digging, p)
		}
		if p.tpsbar {
			bars = append(bars, p)
		}
	}
	s.mu.Unlock()
	for _, p := range digging {
		_ = s.advanceMining(p)
	}

	// M9: advance the overworld clock (day/night) and re-sync clients.
	s.tickTime()

	// M12: advance the weather state machine (rain/thunder/lightning).
	s.tickWeather()

	// M5: entity ticks (item physics, pickup) + tracker reconciliation.
	s.tickEntities()

	// M10: furnace block entities (burn, cook, lit state, progress push).
	// M13: brewing stands + mob status effects share this locked pass.
	s.mu.Lock()
	s.tickBlockEntities()
	s.tickAllMobEffects()
	s.mu.Unlock()

	// M13: resolve thrown-potion impacts (applySplashAt) + despawns.
	s.consumePotionImpacts()

	// M8: survival ticks (hunger, regen, void, eating) for every player.
	// Snapshot under the lock: tickSurvival re-locks internally.
	s.mu.Lock()
	players := make([]*player, 0, len(s.players))
	for _, p := range s.players {
		players = append(players, p)
	}
	s.mu.Unlock()
	for _, p := range players {
		s.tickSurvival(p)
	}

	// M8.5: top the passive herd back up every 20 seconds (production
	// worlds only — tests construct focused entity sets).
	if s.opts.LevelName != "" && s.tickCount%400 == 0 {
		s.topUpMobs()
		// M9: hostiles spawn in the dark on the same cadence.
		s.topUpHostiles()
	}

	s.stats.record(time.Since(start), time.Now())
	// tickCount is read under s.mu by combat cooldowns and /time,
	// so the increment joins the same lock.
	s.mu.Lock()
	s.tickCount++
	refreshBars := s.tickCount%20 == 0
	s.mu.Unlock()
	if refreshBars {
		s.refreshTpsbars(bars)
	}
}

// New validates options and returns a ready-to-start Server.
func New(opts Options) (*Server, error) {
	if opts.ListenAddr == "" {
		return nil, fmt.Errorf("server: empty listen address")
	}
	if opts.MaxPacketLen <= 0 {
		opts.MaxPacketLen = protocol.DefaultMaxPacketLen
	}
	if opts.ReadTimeoutSeconds <= 0 {
		opts.ReadTimeoutSeconds = 30
	}
	if opts.WriteTimeoutSeconds <= 0 {
		opts.WriteTimeoutSeconds = int(defaultWriteTimeout / time.Second)
	}
	if opts.ViewDistance <= 0 {
		opts.ViewDistance = 8
	}
	s := &Server{opts: opts, world: newWorld(0), players: make(map[*conn]*player), entities: make(map[int32]entity), blockEnts: make(map[[3]int]*blockEntity), stop: make(chan struct{}), started: make(chan struct{})}
	s.nextEntityID.Store(0) // first allocEntityID() yields 1, matching tests
	if opts.GCTuning {
		s.gc = newGCTuner(gcConfig{
			Enabled:     true,
			TargetPause: time.Duration(opts.GCTargetPauseMS) * time.Millisecond,
			MemLimit:    opts.GCMemLimitMiB << 20,
			MinGOGC:     opts.GCMinGOGC,
			MaxGOGC:     opts.GCMaxGOGC,
			BaseGOGC:    opts.GCBaseGOGC,
			Interval:    time.Duration(opts.GCSampleInterval) * time.Second,
		})
	}
	if opts.LevelName != "" {
		// M4: replay persisted chunks before accepting connections.
		s.world.enableSaving(opts.LevelName)
		// M10: take over the container block entities decoded during the
		// replay so chests/furnaces keep their contents across restarts.
		for k, b := range s.world.drainPendingBEs() {
			s.blockEnts[k] = b
		}
	}
	if f, err := loadFaviconDataURI(opts.FaviconPath); err != nil {
		// A broken icon must not keep the server offline: log it and go
		// on without the picture.
		log.Printf(ui.Warn("! ")+"跳过服务器图标 %s: %v", opts.FaviconPath, err)
	} else if f != "" {
		s.favicon = f
	}
	return s, nil
}

// Listen binds the listener synchronously; use Addr afterwards and Serve
// to start accepting connections.
func (s *Server) Listen() error {
	ln, err := net.Listen("tcp", s.opts.ListenAddr)
	if err != nil {
		return fmt.Errorf("server: listen %s: %w", s.opts.ListenAddr, err)
	}
	s.ln = ln
	return nil
}

// Addr returns the bound listener address (nil before Listen).
func (s *Server) Addr() net.Addr {
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Serve accepts connections until Shutdown or error; Listen must succeed
// first.
func (s *Server) Serve() error {
	if s.ln == nil {
		return fmt.Errorf("server: Serve called before Listen")
	}
	s.startTicker()
	s.autosaveLoop()
	if s.gc != nil {
		s.gc.start(s.stop, &s.wg)
	}
	close(s.started)
	// M8: seed the passive herd around spawn (production worlds only;
	// tests construct focused entity sets themselves).
	if s.opts.LevelName != "" {
		s.spawnStarterMobs()
	}
	for {
		c, err := s.ln.Accept()
		if err != nil {
			if s.closing.Load() {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return fmt.Errorf("server: accept: %w", err)
		}
		// Register under the server lock so a concurrent Shutdown's
		// Wait (which takes the lock once) can never overtake the Add.
		s.mu.Lock()
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			s.handleConn(c)
		}()
	}
}

// ListenAndServe binds the listener and serves until Shutdown or error.
func (s *Server) ListenAndServe() error {
	if err := s.Listen(); err != nil {
		return err
	}
	return s.Serve()
}

// Shutdown stops accepting new connections and waits for in-flight
// connections to drain.
func (s *Server) Shutdown(ctx context.Context) error {
	if !s.closing.Swap(true) {
		close(s.stop)
	}
	if s.ln != nil {
		_ = s.ln.Close()
	}
	done := make(chan struct{})
	go func() {
		// Order against any in-flight wg.Add: the lock handoff below
		// serialises this Wait with the registration critical sections.
		s.mu.Lock()
		s.mu.Unlock()
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		if err := s.saveAllWorld(); err != nil {
			log.Printf(ui.Error("X ")+"关停保存失败: %v", err)
		}
		return nil
	case <-ctx.Done():
		go func() {
			if err := s.saveAllWorld(); err != nil {
				log.Printf(ui.Error("X ")+"关停保存失败: %v", err)
			}
		}()
		return ctx.Err()
	}
}

// Options exposes the effective options (used by handlers and tests).
func (s *Server) Options() Options { return s.opts }
