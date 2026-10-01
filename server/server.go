// Package server implements the Gopherite server core: connection
// lifecycle, the handshake/status/login state machine and the public
// entry points used by cmd/gopherite.
package server

import (
	"context"
	"crypto/rand"
	"fmt"
	"net"
	"sync"
	"sync/atomic"

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
}

// Server accepts connections and drives the per-connection state machine.
type Server struct {
	opts Options

	ln      net.Listener
	mu      sync.Mutex
	wg      sync.WaitGroup
	closing atomic.Bool
	favicon string // data URI, computed once at start
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
	s := &Server{opts: opts}
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

// newRequestID returns a cryptographically random identifier, used for
// per-connection salt material in later milestones.
func newRequestID() [16]byte {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return b
}
