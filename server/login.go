package server

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// ErrBadName rejects usernames like the vanilla StringUtil validator.
var ErrBadName = errors.New("login: invalid characters in username")

// validName mirrors StringUtil.isValidPlayerName: 1..16 chars of
// [A-Za-z0-9_].
func validName(s string) bool {
	if len(s) == 0 || len(s) > 16 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
		default:
			return false
		}
	}
	return true
}

// handleLogin runs the 26.2 login state machine.
func (c *conn) handleLogin() error {
	id, err := c.rd.VarInt()
	if err != nil {
		return err
	}
	switch id {
	case v776.PacketLSHello:
		return c.handleLoginHello()
	case v776.PacketLSKey:
		return c.handleLoginKey()
	case v776.PacketLSAcknowledged:
		// Switch into configuration exactly like ServerLoginPacketListenerImpl
		// does on login acknowledgement.
		c.st = stateConfig
		return c.startConfiguration()
	}
	return fmt.Errorf("unexpected packet 0x%x in login state", id)
}

// handleLoginHello processes the login start packet. In offline mode the
// profile is derived immediately; online mode requests encryption first.
func (c *conn) handleLoginHello() error {
	hello, err := java.ReadServerboundHello(c.rd)
	if err != nil {
		return err
	}
	if !validName(hello.Name) {
		return ErrBadName
	}
	c.username = hello.Name

	if !c.s.opts.OnlineMode {
		c.profileID = protocol.OfflinePlayerUUID(c.username)
		log.Printf("%s 正在登录（离线模式，实体 ID 待分配）", c.username)
		return c.finishLogin()
	}
	// Online mode: send the encryption request; the next packet must be Key.
	if c.s.keys == nil {
		kp, err := protocol.GenerateKeyPair()
		if err != nil {
			return err
		}
		c.s.keys = kp
	}
	challenge, err := randomBytes(4)
	if err != nil {
		return err
	}
	c.challenge = challenge
	c.wr.Reset()
	c.wr.VarInt(v776.PacketLSHello)
	java.WriteClientboundHello(c.wr, java.ClientboundHello{
		ServerID:           "",
		PublicKey:          c.s.keys.Public,
		Challenge:          challenge,
		ShouldAuthenticate: true,
	})
	return c.sendPacket(c.wr.Bytes())
}

// handleLoginKey processes the encryption response and authenticates the
// session with the Mojang session server. Blocking here is fine: every
// connection owns its goroutine, mirroring the vanilla authenticator
// thread's decoupling from the packet loop only stylistically.
func (c *conn) handleLoginKey() error {
	if c.challenge == nil || c.s.keys == nil {
		return errors.New("login: key packet without encryption request")
	}
	key, err := java.ReadServerboundKey(c.rd)
	if err != nil {
		return err
	}
	secret, err := c.s.keys.DecryptPKCS1v1(key.SharedSecret)
	if err != nil || len(secret) != 16 {
		return errors.New("login: cannot decrypt shared secret")
	}
	token, err := c.s.keys.DecryptPKCS1v1(key.VerifyToken)
	if err != nil || string(token) != string(c.challenge) {
		return errors.New("login: verify token mismatch")
	}
	if err := c.startEncryption(secret); err != nil {
		return err
	}
	if c.s.opts.SkipSessionAuth {
		// Test-only path: complete the encrypted handshake without
		// contacting the session servers.
		c.profileID = protocol.OfflinePlayerUUID(c.username)
		return c.finishLogin()
	}
	digest := protocol.AuthDigest("", secret, c.s.keys.Public)

	base := c.s.opts.SessionServerURL
	if base == "" {
		base = "https://sessionserver.mojang.com/session/minecraft"
	}
	url := fmt.Sprintf("%s/hasJoined?username=%s&serverId=%s", base, c.username, digest)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		log.Printf(ui.Error("X 登录")+": 会话服务器不可达: %v", err)
		return c.kickLogin("multiplayer.disconnect.authservers_down")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		log.Printf(ui.Error("X 登录")+": 玩家 %s 正版验证未通过", c.username)
		return c.kickLogin("multiplayer.disconnect.unverified_username")
	}
	id, props, err := java.ParseHasJoined(resp.Body)
	if err != nil {
		log.Printf(ui.Error("X 登录")+": 会话服务器响应异常: %v", err)
		return c.kickLogin("multiplayer.disconnect.unverified_username")
	}
	c.profileID = id
	c.properties = props
	log.Printf("玩家 %s 的 UUID 为 %s", c.username, ui.Number(java.FormatUUID(id)))
	return c.finishLogin()
}

// finishLogin sends compression + login success; the client answers with
// Login Acknowledged which moves the loop into configuration.
func (c *conn) finishLogin() error {
	const threshold = 256
	c.wr.Reset()
	c.wr.VarInt(v776.PacketLoginCompression)
	java.WriteLoginCompression(c.wr, threshold)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}
	c.enableCompression(threshold)

	var session [16]byte
	if _, err := rand.Read(session[:]); err != nil {
		return err
	}
	c.sessionID = session
	c.wr.Reset()
	c.wr.VarInt(v776.PacketLoginFinished)
	java.WriteClientboundLoginFinished(c.wr, java.ClientboundLoginFinished{
		ProfileID:  c.profileID,
		Name:       c.username,
		Properties: c.properties,
		SessionID:  session,
	})
	return c.sendPacket(c.wr.Bytes())
}

// kickLogin sends a login-phase disconnect and terminates the loop.
func (c *conn) kickLogin(translationKey string) error {
	c.wr.Reset()
	c.wr.VarInt(v776.PacketLoginDisconnect)
	java.WriteLoginDisconnect(c.wr, translationKey)
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}
	return errKicked
}

// errKicked terminates the packet loop after a disconnect packet.
var errKicked = errors.New("login: disconnected")
