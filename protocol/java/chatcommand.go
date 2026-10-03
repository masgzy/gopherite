package java

import "github.com/masgzy/gopherite/protocol"

// M16 chat command / suggestion packets. 26.2 decompiled sources:
// ServerboundChatCommandSignedPacket, ServerboundCommandSuggestionPacket
// and ClientboundCommandSuggestionsPacket. The signed variant is
// consumed (fields read to keep the stream aligned) but signatures are
// not verified: offline play has no chat session.

// ReadChatCommandSigned consumes the signed command payload (0x08) and
// returns the command line: utf command, epoch-milli timestamp, salt,
// per-argument signatures (count × {utf name, 256-byte signature}) and
// the LastSeenMessages.Update acknowledgement (VarInt offset, fixed 20
// bit set = 3 bytes, checksum byte).
func ReadChatCommandSigned(r *protocol.Reader) (string, error) {
	command, err := r.String(65536)
	if err != nil {
		return "", err
	}
	// Instant timestamp + salt.
	if _, err := r.Int64(); err != nil {
		return "", err
	}
	if _, err := r.Int64(); err != nil {
		return "", err
	}
	// ArgumentSignatures: VarInt count, entries {utf16 name, 256B sig}.
	n, err := r.VarInt()
	if err != nil {
		return "", err
	}
	for i := int32(0); i < n; i++ {
		if _, err := r.String(16); err != nil {
			return "", err
		}
		sig, err := r.FixedBytes(256)
		if err != nil {
			return "", err
		}
		_ = sig
	}
	// LastSeenMessages.Update: VarInt offset + 20-bit fixed bit set +
	// checksum byte.
	if _, err := r.VarInt(); err != nil {
		return "", err
	}
	if _, err := r.FixedBytes(3); err != nil {
		return "", err
	}
	if _, err := r.Byte(); err != nil {
		return "", err
	}
	return command, nil
}
