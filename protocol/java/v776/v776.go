// Package v776 implements the Minecraft Java Edition 26.2 protocol
// (protocol number 776).
//
// Packet identifiers below cover the states needed by the current
// milestone. They have been stable across versions since 1.7 and are
// cross-checked against the decompiled 26.2 server during each milestone.
package v776

// Version metadata.
const (
	Name              = "26.2"
	ProtocolNumber    = 776
	WorldVersion      = 4903
	DataPackMajor     = 107
	ResourcePackMajor = 88
)

// Serverbound packet identifiers, per connection state.
const (
	// Handshake state.
	PacketHandshake = 0x00

	// Status state.
	PacketStatusRequest = 0x00
	PacketPingRequest   = 0x01

	// Login state (serverbound).
	PacketLoginStart = 0x00
)

// Clientbound packet identifiers, per connection state.
const (
	// Status state.
	PacketStatusResponse = 0x00
	PacketPongResponse   = 0x01

	// Login state.
	PacketLoginDisconnect = 0x00
)
