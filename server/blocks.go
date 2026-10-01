package server

import (
	"strings"
)

// Query helpers over the generated block-state tables (blocks_gen.go).

// blockStateCount is the size of the global state id space (26.2).
var blockStateCount = len(stateBlock)

// blockNameOf returns the block identifier ("minecraft:xxx") of a global
// state id, or "" when out of range.
func blockNameOf(id int) string {
	if id < 0 || id >= len(stateBlock) {
		return ""
	}
	return blockNames[stateBlock[id]]
}

// blockPropsOf returns the property set of a state as a map (empty for
// blocks without properties).
func blockPropsOf(id int) map[string]string {
	if id < 0 || id >= len(stateProps) {
		return nil
	}
	raw := propsTable[stateProps[id]]
	if raw == "" {
		return nil
	}
	out := make(map[string]string)
	for _, kv := range strings.Split(raw, ";") {
		eq := strings.IndexByte(kv, '=')
		if eq > 0 {
			out[kv[:eq]] = kv[eq+1:]
		}
	}
	return out
}

// defaultStateOf returns the default state id of a block, or -1.
func defaultStateOf(block string) int {
	lo, hi := 0, len(blockNames)
	for lo < hi {
		mid := (lo + hi) / 2
		cmp := strings.Compare(blockNames[mid], block)
		switch {
		case cmp == 0:
			return int(defaultState[mid])
		case cmp < 0:
			lo = mid + 1
		default:
			hi = mid
		}
	}
	return -1
}
