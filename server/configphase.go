package server

import (
	"fmt"
	"log"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// handleConfig runs the configuration phase: brand, feature flags,
// known-pack negotiation, registry sync and the final switch into play.
func (c *conn) handleConfig() error {
	id, err := c.rd.VarInt()
	if err != nil {
		return err
	}
	switch id {
	case v776.PacketConfigClientInfo:
		// Client information (language, view distance, ...). The play
		// milestone reads only the view distance; everything else is kept
		// for future phases.
		c.readClientInformation()
		return nil
	case v776.PacketConfigKnownPacks:
		return c.handleKnownPacks()
	case v776.PacketConfigFinish:
		// Client acknowledged the switch into play.
		c.st = statePlay
		return c.startPlay()
	case v776.PacketConfigKeepAlive:
		nonce, err := java.ReadConfigKeepAlive(c.rd)
		if err != nil {
			return err
		}
		_ = nonce // echo handled by vanilla; we only send during play
		return nil
	case v776.PacketConfigPong:
		_, err := java.ReadConfigPong(c.rd)
		return err
	case v776.PacketConfigPayload:
		return c.readChannelPayload()
	}
	// Skip unknown configuration packets (e.g. cookie responses) instead
	// of dropping the connection; vanilla tolerates them too.
	return nil
}

// readClientInformation consumes the client info packet fields.
func (c *conn) readClientInformation() {
	_, _ = c.rd.String(16) // language
	_, _ = c.rd.Byte()     // view distance
	_, _ = c.rd.VarInt()   // chat visibility
	_, _ = c.rd.Bool()     // chat colors
	_, _ = c.rd.Byte()     // model customisation
	_, _ = c.rd.VarInt()   // main hand
	_, _ = c.rd.Bool()     // text filtering
	_, _ = c.rd.Bool()     // allows listing
	_, _ = c.rd.VarInt()   // particle status
}

// readChannelPayload consumes an unexpected-but-harmless channel payload.
func (c *conn) readChannelPayload() error {
	_, err := c.rd.String(256) // channel
	if err != nil {
		return err
	}
	_, err = c.rd.Bytes()
	return err
}

// startConfiguration begins the configuration phase, mirroring
// ServerConfigurationPacketListenerImpl.startConfiguration: brand, feature
// flags, then the registry synchronisation task.
func (c *conn) startConfiguration() error {
	log.Printf("%s 进入配置阶段", c.username)

	// Brand payload on the minecraft:brand channel.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketCfgPayload)
	java.WriteConfigCustomPayload(c.wr, java.ClientboundCustomPayload{
		Channel: v776.BrandChannel,
		Data:    protocol.NewWriter().String("gopherite").Bytes(),
	})
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Feature flags: a vanilla world enables exactly minecraft:vanilla.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketCfgFeatures)
	java.WriteConfigFeatures(c.wr, java.ClientboundUpdateEnabledFeatures{
		Features: []string{"minecraft:vanilla"},
	})
	if err := c.sendPacket(c.wr.Bytes()); err != nil {
		return err
	}

	// Ask which data packs the client knows.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketCfgKnownPacks)
	java.WriteConfigKnownPacks(c.wr, []java.KnownPack{{
		Namespace: v776.NamespaceVanilla,
		ID:        v776.PackCoreID,
		Version:   v776.Name,
	}})
	return c.sendPacket(c.wr.Bytes())
}

// handleKnownPacks finishes the negotiation and sends the registry data.
// Gopherite always sends every entry with its full network NBT payload
// (the vanilla server behaviour when the client's known-pack list does
// not exactly match ours). Relying on empty payloads would only be valid
// for a pristine vanilla client: modded clients answer with a superset
// of packs, and the vanilla client then fails to rebuild entries from
// its built-in data ("Failed to load registries").
func (c *conn) handleKnownPacks() error {
	if _, err := java.ReadConfigKnownPacks(c.rd); err != nil {
		return err
	}

	packets, err := v776.RegistryDataPackets()
	if err != nil {
		return err
	}
	for _, p := range packets {
		c.wr.Reset()
		c.wr.VarInt(v776.PacketCfgRegistryData)
		java.WriteConfigRegistryData(c.wr, p)
		if err := c.sendPacket(c.wr.Bytes()); err != nil {
			return err
		}
	}

	// Vanilla also sends Update Tags here; the vanilla client tolerates the
	// absence of tag data, which only degrades block/fluid tag behaviour.
	// Tags are planned together with the tag-driven milestones.

	// Finish configuration: the client must now answer with the
	// configuration finish acknowledgement.
	c.wr.Reset()
	c.wr.VarInt(v776.PacketCfgFinish)
	return c.sendPacket(c.wr.Bytes())
}

// kickConfig sends a configuration-phase disconnect.
func (c *conn) kickConfig(reason string) error {
	c.wr.Reset()
	c.wr.VarInt(v776.PacketCfgDisconnect)
	c.wr.String(fmt.Sprintf(`{"text":%q}`, reason))
	return c.sendPacket(c.wr.Bytes())
}
