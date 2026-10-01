// Package java contains version-independent definitions of the Minecraft
// Java Edition protocol: connection intents, handshake parsing and the
// status (server list ping) exchange.
//
// Concrete packet identifiers and version constants live in per-version
// sub-packages (e.g. v776 for 1.26.2). The server selects one of them at
// startup; the version registry that will negotiate multiple protocol
// versions is planned for a later milestone.
package java

import "github.com/masgzy/gopherite/protocol"

// Intent is the value of the Handshake packet's "next state" field.
type Intent int32

const (
	// IntentStatus means the client will perform a server list ping.
	IntentStatus Intent = 1

	// IntentLogin means the client will attempt to join the server.
	IntentLogin Intent = 2
)

// Handshake is the first packet a client sends on every connection.
type Handshake struct {
	ProtocolVersion int32  // client's protocol number, e.g. 776 for 26.2
	ServerAddress   string // address the client used to connect (SRV-resolved)
	ServerPort      uint16
	NextState       Intent
}

// MaxServerAddressLen matches the vanilla String(255) bound.
const MaxServerAddressLen = 255

// ReadHandshake decodes the handshake packet payload.
func ReadHandshake(r *protocol.Reader) (Handshake, error) {
	var h Handshake
	pv, err := r.VarInt()
	if err != nil {
		return h, err
	}
	addr, err := r.String(MaxServerAddressLen)
	if err != nil {
		return h, err
	}
	port, err := r.Uint16()
	if err != nil {
		return h, err
	}
	next, err := r.VarInt()
	if err != nil {
		return h, err
	}
	h.ProtocolVersion = pv
	h.ServerAddress = addr
	h.ServerPort = port
	h.NextState = Intent(next)
	if h.NextState != IntentStatus && h.NextState != IntentLogin {
		return Handshake{}, ErrUnknownIntent
	}
	return h, nil
}
