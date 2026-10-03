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
	"sync/atomic"
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

	// dead marks a connection whose write pipeline failed (write timeout
	// or socket error). Once set, sendPacket refuses further work so
	// callers parked behind writeMu unwind instantly instead of queueing
	// another full timeout per packet.
	dead atomic.Bool
}

// defaultWriteTimeout applies when the option is unset (e.g. bare &Server{}
// in unit tests) so a zero value can never turn into an instantly expired
// deadline.
const defaultWriteTimeout = 10 * time.Second

// errConnDead is returned by sendPacket once the connection has been
// poisoned by a write failure; callers treat it like any other write error.
var errConnDead = errors.New("conn: write pipeline dead")

func (s *Server) handleConn(nc net.Conn) {
	idle := time.Duration(s.opts.ReadTimeoutSeconds) * time.Second

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
	defer func() {
		// 先毒化写管线再关套接字：keepalive ticker 与其它玩家的广播
		// 协程要么在 sendPacket 入口短路，要么拿到 net.ErrClosed 后由
		// poison 静默收尾——不再出现 "use of closed network connection"
		// 的误报日志（旧序只 Close 不置 dead，写入方会把关停竞争当
		// 成真实写入故障上报）。
		c.dead.Store(true)
		_ = nc.Close()
		if c.player != nil {
			c.s.removePlayer(c)
		}
		if c.username != "" {
			log.Printf("%s 断开连接", c.username)
		}
	}()

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
			// errKicked：已发 disconnect 包的正常踢出；
			// errCloseAfterPong：status 阶段回完 pong 后的正常关闭
			//（vanilla 行为，客户端刷新 MOTD 每次都会走到这里）。
			// 两者都不算异常，静默退出即可。
			if !errors.Is(err, errKicked) && !errors.Is(err, errCloseAfterPong) {
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
//
// 写超时：客户端停止读取后 TCP 发送缓冲会被写满，WriteFramed 的底层
// write 会无限阻塞。多个广播路径（broadcastSoundLocked、
// broadcastBlockUpdateLocked、broadcastTimeLocked 等）是在持有
// Server.mu 的情况下逐个 sendPacket 的——单个卡死的连接就能冻结整个
// 服务端 tick。因此每次写入都带独立截止时间，超时立即毒化连接并返回
// 错误，让广播循环得以继续服务其余玩家。
func (c *conn) sendPacket(body []byte) error {
	if c.dead.Load() {
		return errConnDead
	}
	if c.nc == nil {
		return errConnDead // 单测中的无套接字 conn：静默拒绝发包
	}
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
	timeout := time.Duration(c.s.opts.WriteTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = defaultWriteTimeout
	}
	_ = c.nc.SetWriteDeadline(time.Now().Add(timeout))
	err := protocol.WriteFramed(c.bw, payload)
	if err != nil {
		c.poison(err)
		return err
	}
	return nil
}

// poison marks the connection unusable after a write failure and closes
// the underlying socket so every writer parked on it unwinds immediately.
// A failed framed write leaves bufio holding a partial frame and the frame
// stream desynced — there is no recovery, closing is the only option. The
// CAS keeps the log and the close to a single occurrence even when the
// dispatch loop and the tickers fail on the same connection simultaneously.
func (c *conn) poison(cause error) {
	if !c.dead.CompareAndSwap(false, true) {
		return
	}
	// net.ErrClosed 表示套接字早已被 Close（读循环退出的清理路径），
	// 这次写入失败只是关停竞争的回声而非真实故障——vanilla 面对已
	// 断开的连接同样只静默丢弃，不该刷 "写入失败" 恐慌日志。
	if !errors.Is(cause, net.ErrClosed) {
		log.Printf("%s 写入失败，断开连接: %v", c.username, cause)
	}
	_ = c.nc.Close()
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
		// 走 sendPacket 而非直写：获得写超时与 writeMu 串行化保护。
		if err := c.sendPacket(c.wr.Bytes()); err != nil {
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
	// 走 sendPacket 而非直写：获得写超时与 writeMu 串行化保护。
	return c.sendPacket(c.wr.Bytes())
}
