package java

import "github.com/masgzy/gopherite/protocol"

// M17 advancement packets, 26.2 wire forms verified against the
// decompiled vanilla sources (ClientboundUpdateAdvancementsPacket,
// ClientboundSelectAdvancementsTabPacket, ServerboundSeenAdvancementsPacket
// plus AdvancementHolder / Advancement / DisplayInfo / AdvancementRequirements /
// AdvancementProgress / CriterionProgress STREAM_CODECs).

// AdvancementDef is one display-ready advancement in wire field order.
// The server converts the embedded vanilla JSON definitions into this
// shape; the protocol layer only knows how to serialise it.
type AdvancementDef struct {
	ID         string  // "minecraft:story/root"
	Parent     string  // "" = root (optional-absent on the wire)
	HasDisplay bool    // false omits the display block entirely
	Title      string  // translate key (component {"translate": key})
	Desc       string  // translate key
	IconItem   int32   // item registry id (0 = air); encoded as holder id+1
	IconCount  int32   // vanilla icons are count 1
	Frame      int32   // AdvancementType ordinal: 0 task, 1 challenge, 2 goal
	Background string  // "" = no background (flags bit 0)
	ShowToast  bool    // flags bit 1
	Hidden     bool    // flags bit 4
	X, Y       float32 // tree layout coordinates (TreeNodePosition.run)
	// Requirements is the AND-of-OR criterion groups; every referenced
	// name must appear in progress updates.
	Requirements [][]string

	// SendsTelemetry is the trailing boolean of the Advancement codec.
	// Vanilla flips it for telemetry-gated advancements; the server never
	// sends telemetry events, the flag only round-trips.
	SendsTelemetry bool
}

// CriterionProgressWire is one criterion's completion state: the epoch
// millis it was granted (0 = incomplete).
type CriterionProgressWire struct {
	Obtained int64 // epoch millis; 0 = not done
}

// AdvancementProgressWire pairs an advancement id with its per-criterion
// completion map for the progress section of the packet.
type AdvancementProgressWire struct {
	ID       string
	Criteria map[string]CriterionProgressWire
}

// WriteUpdateAdvancements encodes ClientboundUpdateAdvancements (0x82):
// reset, added, removed, progress, showAdvancements.
func WriteUpdateAdvancements(w *protocol.Writer, reset bool, added []AdvancementDef, removed []string, progress []AdvancementProgressWire, show bool) {
	w.Bool(reset)
	w.VarInt(int32(len(added)))
	for _, a := range added {
		writeAdvancementHolder(w, a)
	}
	w.VarInt(int32(len(removed)))
	for _, id := range removed {
		w.String(id)
	}
	w.VarInt(int32(len(progress)))
	for _, p := range progress {
		w.String(p.ID)
		w.VarInt(int32(len(p.Criteria)))
		for name, c := range p.Criteria {
			w.String(name)
			if c.Obtained > 0 {
				w.Bool(true)
				w.Int64(c.Obtained)
			} else {
				w.Bool(false)
			}
		}
	}
	w.Bool(show)
}

// writeAdvancementHolder encodes AdvancementHolder.STREAM_CODEC:
// Identifier + Advancement.
func writeAdvancementHolder(w *protocol.Writer, a AdvancementDef) {
	w.String(a.ID)
	// Advancement: optional parent.
	if a.Parent != "" {
		w.Bool(true)
		w.String(a.Parent)
	} else {
		w.Bool(false)
	}
	// Optional display.
	if !a.HasDisplay {
		w.Bool(false)
	} else {
		w.Bool(true)
		writeDisplayInfo(w, a)
	}
	// AdvancementRequirements: collection of collections of names.
	w.VarInt(int32(len(a.Requirements)))
	for _, group := range a.Requirements {
		w.VarInt(int32(len(group)))
		for _, name := range group {
			w.String(name)
		}
	}
	// sends_telemetry_event.
	w.Bool(a.SendsTelemetry)
}

// writeDisplayInfo encodes DisplayInfo.serializeToNetwork: title,
// description, icon (ItemStackTemplate), frame enum, flags int,
// optional background, x, y.
func writeDisplayInfo(w *protocol.Writer, a AdvancementDef) {
	// ComponentSerialization.TRUSTED_STREAM_CODEC = network NBT compound.
	WriteTranslateComponent(w, a.Title)
	WriteTranslateComponent(w, a.Desc)
	// ItemStackTemplate.STREAM_CODEC: item holder (registry id + 1),
	// VarInt count, DataComponentPatch (empty = two zero VarInts).
	w.VarInt(a.IconItem + 1)
	w.VarInt(a.IconCount)
	w.VarInt(0) // component additions: none
	w.VarInt(0) // component removals: none
	// AdvancementType ordinal (VarInt): 0 task, 1 challenge, 2 goal.
	w.VarInt(a.Frame)
	flags := int32(0)
	if a.Background != "" {
		flags |= 1
	}
	if a.ShowToast {
		flags |= 2
	}
	if a.Hidden {
		flags |= 4
	}
	w.Int32(flags)
	if a.Background != "" {
		w.String(a.Background)
	}
	w.Float(a.X)
	w.Float(a.Y)
}

// WriteSelectAdvancementsTab encodes ClientboundSelectAdvancementsTab
// (0x55): nullable Identifier — false = clear the selection.
func WriteSelectAdvancementsTab(w *protocol.Writer, tab string, present bool) {
	if present {
		w.Bool(true)
		w.String(tab)
		return
	}
	w.Bool(false)
}

// ReadSeenAdvancements decodes ServerboundSeenAdvancements (0x32):
// VarInt action (0 opened tab, 1 closed screen); opened tabs carry the
// tab identifier.
func ReadSeenAdvancements(r *protocol.Reader) (action int32, tab string, err error) {
	action, err = r.VarInt()
	if err != nil {
		return 0, "", err
	}
	if action == 0 {
		tab, err = r.String(32767)
		if err != nil {
			return 0, "", err
		}
	}
	return action, tab, nil
}
