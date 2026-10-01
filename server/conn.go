package server

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// connState mirrors the vanilla connection state machine.
type connState int8

const (
	stateHandshake connState = iota
	stateStatus
	stateLogin
)

// conn wraps one TCP connection and runs its packet loop.
type conn struct {
	s  *Server
	nc net.Conn
	br *bufio.Reader
	bw *bufio.Writer
	fr *protocol.FrameReader
	rd *protocol.Reader
	wr *protocol.Writer
	st connState
}

func (s *Server) handleConn(nc net.Conn) {
	defer nc.Close()
	idle := time.Duration(s.opts.ReadTimeoutSeconds) * time.Second
	_ = nc.SetDeadline(time.Now().Add(idle))

	c := &conn{
		s:  s,
		nc: nc,
		br: bufio.NewReaderSize(nc, 4096),
		bw: bufio.NewWriterSize(nc, 4096),
		st: stateHandshake,
	}
	c.fr = protocol.NewFrameReader(c.br, s.opts.MaxPacketLen)
	c.rd = &protocol.Reader{}
	c.wr = protocol.NewWriter()

	for {
		payload, err := c.fr.Next()
		if err != nil {
			return // EOF, reset or malformed frame: drop silently like vanilla
		}
		c.rd.Reset(payload)
		if err := c.dispatch(); err != nil {
			return
		}
	}
}

// dispatch routes the current packet by connection state.
func (c *conn) dispatch() error {
	switch c.st {
	case stateHandshake:
		return c.handleHandshake()
	case stateStatus:
		return c.handleStatus()
	case stateLogin:
		return c.handleLogin()
	}
	return fmt.Errorf("unknown state %d", c.st)
}

func (c *conn) handleHandshake() error {
	id, err := c.rd.VarInt()
	if err != nil {
		return err
	}
	if id != v776.PacketHandshake {
		return fmt.Errorf("unexpected packet 0x%x in handshake state", id)
	}
	h, err := java.ReadHandshake(c.rd)
	if err != nil {
		return err
	}
	switch h.NextState {
	case java.IntentStatus:
		c.st = stateStatus
		return nil
	case java.IntentLogin:
		c.st = stateLogin
		return nil
	default:
		return java.ErrUnknownIntent
	}
}

// handleStatus implements the server list ping exchange.
func (c *conn) handleStatus() error {
	id, err := c.rd.VarInt()
	if err != nil {
		return err
	}
	switch id {
	case v776.PacketStatusRequest:
		return c.sendStatusResponse()
	case v776.PacketPingRequest:
		nonce, err := java.ReadPingRequest(c.rd)
		if err != nil {
			return err
		}
		c.wr.Reset()
		c.wr.VarInt(v776.PacketPongResponse).Int64(nonce)
		if err := protocol.WriteFramed(c.bw, c.wr.Bytes()); err != nil {
			return err
		}
		// Vanilla closes right after the pong; the client never sends
		// anything further in this state.
		return errCloseAfterPong
	}
	return fmt.Errorf("unexpected packet 0x%x in status state", id)
}

// errCloseAfterPong terminates the loop cleanly after answering a ping.
var errCloseAfterPong = errors.New("status: closed after pong")

// handleLogin rejects joins for now with the same disconnect packet the
// final implementation will use; M2 replaces the body with the real flow.
func (c *conn) handleLogin() error {
	id, err := c.rd.VarInt()
	if err != nil {
		return err
	}
	if id != v776.PacketLoginStart {
		return fmt.Errorf("unexpected packet 0x%x in login state", id)
	}
	// LoginStart carries the username; we intentionally do not consume it
	// further: the connection is rejected regardless.
	const msg = `{"text":"Gopherite: joining is not implemented yet (planned for M2).","color":"yellow"}`
	c.wr.Reset()
	c.wr.VarInt(v776.PacketLoginDisconnect).String(msg)
	return protocol.WriteFramed(c.bw, c.wr.Bytes())
}

// sendStatusResponse renders and writes the server list JSON document.
func (c *conn) sendStatusResponse() error {
	doc := c.s.statusDocument()
	c.wr.Reset()
	c.wr.VarInt(v776.PacketStatusResponse).String(doc)
	return protocol.WriteFramed(c.bw, c.wr.Bytes())
}
