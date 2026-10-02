package server

import (
	"bufio"
	"crypto/aes"
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
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
	stateConfig
	statePlay
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

	// writeMu serializes all writes to bw: the dispatch goroutine and the
	// keep-alive ticker both emit packets on this connection.
	writeMu sync.Mutex

	// cipher state; nil until negotiated
	decrypt *protocol.CFB8
	encrypt *protocol.CFB8

	// compression; nil until the Set Compression packet
	compression *protocol.CompressionLayer

	// login/session data
	username   string
	profileID  [16]byte
	sessionID  [16]byte
	challenge  []byte
	properties []java.ProfileProperty

	// clientViewDistance is the render distance the client reported
	// during configuration (0 = not sent).
	clientViewDistance int

	// play state
	player *player
}

func (s *Server) handleConn(nc net.Conn) {
	defer nc.Close()
	idle := time.Duration(s.opts.ReadTimeoutSeconds) * time.Second

	c := &conn{
		s:  s,
		nc: nc,
		br: bufio.NewReaderSize(nc, 4096),
		bw: bufio.NewWriterSize(nc, 4096),
		st: stateHandshake,
	}
	defer func() {
		if c.player != nil {
			c.s.removePlayer(c)
		}
		if c.username != "" {
			log.Printf("%s 断开连接", c.username)
		}
	}()
	c.fr = protocol.NewFrameReader(c.br, s.opts.MaxPacketLen)
	c.rd = &protocol.Reader{}
	c.wr = protocol.NewWriter()

	for {
		// Idle timeout: the deadline applies from now until the next frame
		// arrives. Active connections (keep-alives, movement) never trip it;
		// silent ones are dropped like vanilla's readTimeout.
		_ = nc.SetReadDeadline(time.Now().Add(idle))
		payload, err := c.fr.Next()
		if err != nil {
			return // EOF, reset or malformed frame: drop silently like vanilla
		}
		if c.decrypt != nil {
			plain := make([]byte, 0, len(payload)+16)
			plain = c.decrypt.Crypt(plain, payload)
			payload = plain
		}
		if c.compression != nil {
			payload, err = c.compression.DecompressFrame(payload, s.opts.MaxPacketLen)
			if err != nil {
				return
			}
		}
		c.rd.Reset(payload)
		if err := c.dispatch(); err != nil {
			if !errors.Is(err, errKicked) {
				log.Printf("%s 会话异常: %v", c.username, err)
			}
			return
		}
	}
}

// sendPacket encodes body (already including the packet id) and writes it
// with the current cipher/compression settings. The layering mirrors
// vanilla Netty: compress first, then encrypt, then the plaintext length
// VarInt around the encrypted frame.
//
// The WHOLE pipeline runs under writeMu: the zlib scratch buffer in
// CompressionLayer and the CFB8 keystream state are per-connection
// mutable state, and this method is called from the dispatch goroutine,
// the keep-alive ticker and the entity/bossbar tickers concurrently.
// Compressing outside the lock produced corrupted interleaved frames
// (truncated packets on real joins at large view distances).
func (c *conn) sendPacket(body []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	payload := body
	if c.compression != nil {
		payload = c.compression.CompressInner(body)
	}
	if c.encrypt != nil {
		buf := make([]byte, 0, len(payload)+16)
		payload = c.encrypt.Crypt(buf, payload)
	}
	return protocol.WriteFramed(c.bw, payload)
}

// writeWith wraps c.wr in an encoder func and flushes it as one packet.
func (c *conn) writeWith(id int32, encode func(w *protocol.Writer)) error {
	c.wr.Reset()
	c.wr.VarInt(id)
	encode(c.wr)
	return c.sendPacket(c.wr.Bytes())
}

// startEncryption enables CFB8 for both directions with the shared secret.
func (c *conn) startEncryption(secret []byte) error {
	iv := secret[:aes.BlockSize] // vanilla uses the secret itself as IV
	dec, err := protocol.NewCFB8Decrypter(secret, iv)
	if err != nil {
		return err
	}
	enc, err := protocol.NewCFB8Encrypter(secret, iv)
	if err != nil {
		return err
	}
	c.decrypt, c.encrypt = dec, enc
	return nil
}

// enableCompression installs the zlib framing layer.
func (c *conn) enableCompression(threshold int32) {
	c.compression = protocol.NewCompressionLayer(threshold)
}

// randomBytes fills a buffer with cryptographically random bytes.
func randomBytes(n int) ([]byte, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return b, nil
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
	case stateConfig:
		return c.handleConfig()
	case statePlay:
		return c.handlePlay()
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

// sendStatusResponse renders and writes the server list JSON document.
func (c *conn) sendStatusResponse() error {
	doc := c.s.statusDocument()
	c.wr.Reset()
	c.wr.VarInt(v776.PacketStatusResponse).String(doc)
	return protocol.WriteFramed(c.bw, c.wr.Bytes())
}
