package java

import "github.com/masgzy/gopherite/protocol"

// Configuration-phase packets (26.2). The configuration phase negotiates
// client options, brands, registry data and known data packs before the
// player enters the world.

// ClientboundCustomPayload sends a plugin-channel message. Channel is the
// namespaced channel identifier and Data the raw channel payload.
type ClientboundCustomPayload struct {
	Channel string
	Data    []byte
}

// WriteConfigCustomPayload encodes a configuration-phase custom payload.
func WriteConfigCustomPayload(w *protocol.Writer, p ClientboundCustomPayload) {
	w.String(p.Channel)
	w.VarInt(int32(len(p.Data))).FixedBytes(p.Data)
}

// ClientboundUpdateEnabledFeatures announces enabled feature flags. A
// vanilla world enables exactly "minecraft:vanilla".
type ClientboundUpdateEnabledFeatures struct {
	Features []string
}

// WriteConfigFeatures encodes the feature-flag packet.
func WriteConfigFeatures(w *protocol.Writer, p ClientboundUpdateEnabledFeatures) {
	w.VarInt(int32(len(p.Features)))
	for _, f := range p.Features {
		w.String(f)
	}
}

// KnownPack identifies one data pack the client already knows, letting the
// server skip registry entry payloads the client can rebuild locally.
type KnownPack struct {
	Namespace string
	ID        string
	Version   string
}

// WriteConfigKnownPacks encodes the server's known-pack list.
func WriteConfigKnownPacks(w *protocol.Writer, packs []KnownPack) {
	w.VarInt(int32(len(packs)))
	for _, p := range packs {
		w.String(p.Namespace)
		w.String(p.ID)
		w.String(p.Version)
	}
}

// ReadConfigKnownPacks decodes the client's known-pack answer.
func ReadConfigKnownPacks(r *protocol.Reader) ([]KnownPack, error) {
	n, err := r.VarInt()
	if err != nil {
		return nil, err
	}
	if n < 0 || n > 64 {
		return nil, ErrMalformed
	}
	out := make([]KnownPack, 0, n)
	for i := int32(0); i < n; i++ {
		ns, err := r.String(256)
		if err != nil {
			return nil, err
		}
		id, err := r.String(256)
		if err != nil {
			return nil, err
		}
		ver, err := r.String(256)
		if err != nil {
			return nil, err
		}
		out = append(out, KnownPack{Namespace: ns, ID: id, Version: ver})
	}
	return out, nil
}

// ClientboundRegistryData sends one registry's entries. Each entry carries
// an identifier and an optional NBT payload; empty payloads are used when
// the client reported knowledge of the entry's source data pack.
type ClientboundRegistryData struct {
	RegistryKey string
	Entries     []RegistryEntry
}

// RegistryEntry is one registry entry: identifier plus optional NBT.
type RegistryEntry struct {
	ID      string
	Payload []byte // raw NBT, nil when absent
	HasData bool
}

// WriteConfigRegistryData encodes one Registry Data packet.
func WriteConfigRegistryData(w *protocol.Writer, p ClientboundRegistryData) {
	w.String(p.RegistryKey)
	w.VarInt(int32(len(p.Entries)))
	for _, e := range p.Entries {
		w.String(e.ID)
		if e.HasData {
			w.Bool(true).VarInt(int32(len(e.Payload))).FixedBytes(e.Payload)
		} else {
			w.Bool(false)
		}
	}
}

// WriteConfigKeepAlive encodes a configuration keep-alive challenge.
func WriteConfigKeepAlive(w *protocol.Writer, id int64) {
	w.Int64(id)
}

// ReadConfigKeepAlive decodes the client's keep-alive answer.
func ReadConfigKeepAlive(r *protocol.Reader) (int64, error) {
	return r.Int64()
}

// ClientboundPing asks the client to echo a nonce during configuration.
func WriteConfigPing(w *protocol.Writer, id int32) {
	w.Int32(id)
}

// ServerboundFinishConfiguration acknowledges the switch to play.
func ReadConfigFinish(r *protocol.Reader) error {
	// No payload.
	return nil
}

// ServerboundPong answers the configuration ping.
func ReadConfigPong(r *protocol.Reader) (int32, error) {
	return r.Int32()
}

// EmptyRegistryEntries builds registry entries with identifiers only, used
// when the client knows the source data pack and rebuilds contents from its
// built-in data.
func EmptyRegistryEntries(ids []string) []RegistryEntry {
	out := make([]RegistryEntry, 0, len(ids))
	for _, id := range ids {
		out = append(out, RegistryEntry{ID: id})
	}
	return out
}
