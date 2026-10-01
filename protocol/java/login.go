package java

import (
	"errors"

	"github.com/masgzy/gopherite/protocol"
)

// Login-phase packets (26.2).
//
// Clientbound Hello is the "encryption request"; Serverbound Hello is the
// "login start". The vanilla class names are Hello on both directions.

// ErrMalformed is returned when a packet payload does not match the
// expected 26.2 field layout.
var ErrMalformed = errors.New("java: malformed packet payload")

// ServerboundHello begins the login state: the client's chosen name plus
// the UUID it would use when playing offline (ignored by online-mode
// servers, which determine identity from the session server).
type ServerboundHello struct {
	Name      string
	ProfileID [16]byte
}

// ReadServerboundHello decodes the login start packet.
func ReadServerboundHello(r *protocol.Reader) (ServerboundHello, error) {
	var out ServerboundHello
	name, err := r.String(16)
	if err != nil {
		return out, err
	}
	id, err := r.UUID()
	if err != nil {
		return out, err
	}
	out.Name, out.ProfileID = name, id
	return out, nil
}

// ClientboundHello requests encryption. PublicKey is the X.509 DER blob,
// Challenge the four-byte verify token and ShouldAuthenticate tells the
// client whether the session server must be contacted (always true for
// online-mode servers).
type ClientboundHello struct {
	ServerID           string
	PublicKey          []byte
	Challenge          []byte
	ShouldAuthenticate bool
}

// WriteClientboundHello encodes the encryption request.
func WriteClientboundHello(w *protocol.Writer, p ClientboundHello) {
	w.String(p.ServerID)
	w.VarInt(int32(len(p.PublicKey))).FixedBytes(p.PublicKey)
	w.VarInt(int32(len(p.Challenge))).FixedBytes(p.Challenge)
	w.Bool(p.ShouldAuthenticate)
}

// ServerboundKey carries the client's AES shared secret and the encrypted
// verify token, both RSA-encrypted with the server public key.
type ServerboundKey struct {
	SharedSecret []byte
	VerifyToken  []byte
}

// ReadServerboundKey decodes the encryption response.
func ReadServerboundKey(r *protocol.Reader) (ServerboundKey, error) {
	var out ServerboundKey
	secret, err := r.Bytes()
	if err != nil {
		return out, err
	}
	token, err := r.Bytes()
	if err != nil {
		return out, err
	}
	out.SharedSecret, out.VerifyToken = secret, token
	return out, nil
}

// ClientboundLoginFinished is the login success packet: the resolved
// profile plus a per-connection session id introduced in 26.2.
type ClientboundLoginFinished struct {
	ProfileID [16]byte
	Name      string
	// Properties is empty for offline mode; online mode carries textures.
	Properties []ProfileProperty
	SessionID  [16]byte
}

// ProfileProperty is one signed or unsigned profile property entry.
type ProfileProperty struct {
	Name      string
	Value     string
	Signature string
	Signed    bool
}

// WriteClientboundLoginFinished encodes the login success packet.
func WriteClientboundLoginFinished(w *protocol.Writer, p ClientboundLoginFinished) {
	w.UUID(p.ProfileID)
	w.String(p.Name)
	w.VarInt(int32(len(p.Properties)))
	for _, prop := range p.Properties {
		w.String(prop.Name)
		w.String(prop.Value)
		if prop.Signed {
			w.Bool(true).String(prop.Signature)
		} else {
			w.Bool(false)
		}
	}
	w.UUID(p.SessionID)
}

// WriteLoginCompression encodes the Set Compression packet.
func WriteLoginCompression(w *protocol.Writer, threshold int32) {
	w.VarInt(threshold)
}

// WriteLoginDisconnect encodes the login-phase disconnect packet. reason
// is a translation key; the wire format is an NBT text component
// ({"translate": reason}).
func WriteLoginDisconnect(w *protocol.Writer, reason string) {
	WriteTranslateComponent(w, reason)
}
