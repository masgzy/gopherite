package v776

import (
	"encoding/binary"
	"sync"

	_ "embed"
)

//go:embed registry_frames.bin
var registryFramesBin []byte

//go:embed tags_frame.bin
var tagsFrameBin []byte

var (
	framesOnce sync.Once
	registryF  [][]byte
	tagsF      []byte
)

// RegistryFrames returns the vanilla byte-exact ClientboundRegistryData
// payloads (packet id included) for every synchronised registry, in the
// order the vanilla 26.2 server emits them.
//
// The frames were captured from a pristine vanilla 26.2 server by
// scripts/dumpbot after answering the known-packs query with an empty
// list, which forces vanilla to send every entry with full network NBT.
// The NBT is the network projection of each registry value (worldgen-only
// fields stripped, floats kept as floats), which hand-converting the
// bundled datapack JSON cannot reproduce faithfully — the vanilla client
// rejects those blobs during registry loading.
func RegistryFrames() [][]byte {
	framesOnce.Do(parseFrames)
	return registryF
}

// UpdateTagsFrame returns the vanilla byte-exact
// ClientboundUpdateTagsPacket payload (config phase packet id included).
// Tag entries reference registries by protocol id, which matches vanilla
// exactly because the registry frames above are byte-identical to
// vanilla's.
func UpdateTagsFrame() []byte {
	framesOnce.Do(parseFrames)
	return tagsF
}

func parseFrames() {
	registryF = splitFrames(registryFramesBin)
	if len(tagsFrameBin) < 4 {
		panic("v776: empty tags frame")
	}
	tagsF = tagsFrameBin[4:] // strip u32 length prefix
}

func splitFrames(blob []byte) [][]byte {
	var out [][]byte
	for len(blob) >= 4 {
		n := int(binary.BigEndian.Uint32(blob[:4]))
		if 4+n > len(blob) {
			panic("v776: truncated registry frames")
		}
		out = append(out, blob[4:4+n])
		blob = blob[4+n:]
	}
	return out
}
