// Package server implements the Gopherite server core: connection
// lifecycle, the handshake/status/login state machine and the public
// entry points used by cmd/gopherite.
package server

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

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

	// ViewDistance is the server-side chunk radius sent to clients.
	ViewDistance int

	// KeepAliveInterval overrides the 15s keep-alive period (tests).
	// Zero means the vanilla default.
	KeepAliveInterval time.Duration

	// SessionServerURL overrides the Mojang session server base used for
	// online-mode hasJoined verification. Empty means the production
	// endpoint; tests point this at a stub.
	SessionServerURL string
}

// Server accepts connections and drives the per-connection state machine.
type Server struct {
	opts Options

	ln      net.Listener
	mu      sync.Mutex
	wg      sync.WaitGroup
	closing atomic.Bool
	favicon string // data URI, computed once at start

	// keys is the login RSA keypair, generated lazily on first online-mode
	// login and reused for the server lifetime.
	keys *protocol.KeyPair

	// world is the M2 superflat overworld.
	world *world

	// players are the joined, in-play players; guarded by mu together
	// with player.seen and player.mining (the ticker touches both).
	players map[*conn]*player
}

// playerListLocked returns the joined players; caller holds mu.
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

// removePlayer unregisters a disconnecting player.
func (s *Server) removePlayer(c *conn) {
	s.mu.Lock()
	delete(s.players, c)
	s.mu.Unlock()
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

// tickOnce advances per-tick state: mining progress today.
func (s *Server) tickOnce() {
	s.mu.Lock()
	var digging []*player
	for _, p := range s.players {
		if p.mining != nil {
			digging = append(digging, p)
		}
	}
	s.mu.Unlock()
	for _, p := range digging {
		_ = s.advanceMining(p)
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
	if opts.ViewDistance <= 0 {
		opts.ViewDistance = 8
	}
	s := &Server{opts: opts, world: newWorld(0), players: make(map[*conn]*player)}
	if f, err := loadFaviconDataURI(opts.FaviconPath); err != nil {
		return nil, err
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
		s.wg.Add(1)
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
	s.closing.Store(true)
	if s.ln != nil {
		_ = s.ln.Close()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Options exposes the effective options (used by handlers and tests).
func (s *Server) Options() Options { return s.opts }
