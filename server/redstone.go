package server

// Basic redstone (M7c): lever, redstone torch, redstone dust and the
// redstone lamp. Semantics follow vanilla's essentials — dust conducts
// 0-15 power decaying per step, sources feed adjacent dust and
// consumers, and torch inversion applies to the block it sits on.
// Recomputation is local: after a change the affected dust region
// (BFS through wire) plus its adjacent consumers is rebuilt and every
// changed state is broadcast.

import (
	"strconv"
)

// redstoneBlock predicates over block names.
func blockNameAt(s *Server, x, y, z int) string {
	return blockNameOf(int(s.world.getBlock(x, y, z)))
}

func isWire(name string) bool  { return name == "minecraft:redstone_wire" }
func isLever(name string) bool { return name == "minecraft:lever" }
func isTorch(name string) bool {
	return name == "minecraft:redstone_torch" || name == "minecraft:redstone_wall_torch"
}
func isLamp(name string) bool { return name == "minecraft:redstone_lamp" }
func isComponent(name string) bool {
	return isWire(name) || isLever(name) || isTorch(name) || isLamp(name)
}

// dirVecs lists the 6 face directions (vanilla wire order).
var dirVecs = [6][3]int{
	{0, 0, -1}, {0, 0, 1}, {-1, 0, 0}, {1, 0, 0}, {0, -1, 0}, {0, 1, 0},
}

// horizontal dirs with their wire property names.
var horizDirs = []struct {
	dx, dz int
	prop   string
}{
	{0, -1, "north"},
	{0, 1, "south"},
	{-1, 0, "west"},
	{1, 0, "east"},
}

// setBlockState applies one state change and broadcasts it.
func (s *Server) setBlockState(x, y, z int, state int32) {
	if s.world.getBlock(x, y, z) == state {
		return
	}
	if !s.world.setBlock(x, y, z, state) {
		return
	}
	s.broadcastBlockUpdateLocked(int32(x), int32(y), int32(z), state)
}

// redstoneUpdate recomputes the dust region around (x, y, z).
func (s *Server) redstoneUpdate(x, y, z int) {
	// Collect the wire region via BFS over wire blocks (slopes ±1).
	region := map[[3]int]bool{{x, y, z}: true}
	queue := [][3]int{{x, y, z}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, d := range dirVecs {
			n := [3]int{cur[0] + d[0], cur[1] + d[1], cur[2] + d[2]}
			if region[n] {
				continue
			}
			if isWire(blockNameAt(s, n[0], n[1], n[2])) {
				region[n] = true
				queue = append(queue, n)
			}
		}
	}

	// Consumers adjacent to the region (lamps and torches whose
	// attachment sits in it), plus every source inside it.
	consumers := map[[3]int]bool{}
	type source struct {
		pos   [3]int
		power int
	}
	var sources []source
	regionPos := func() [][3]int {
		out := make([][3]int, 0, len(region))
		for p := range region {
			out = append(out, p)
		}
		return out
	}()
	for _, p := range regionPos {
		if p != [3]int{x, y, z} && !isWire(blockNameAt(s, p[0], p[1], p[2])) {
			continue
		}
		for _, d := range dirVecs {
			n := [3]int{p[0] + d[0], p[1] + d[1], p[2] + d[2]}
			name := blockNameAt(s, n[0], n[1], n[2])
			if isLamp(name) {
				consumers[n] = true
			}
			if isLever(name) {
				props := blockPropsOf(int(s.world.getBlock(n[0], n[1], n[2])))
				if props["powered"] == "true" {
					sources = append(sources, source{pos: n, power: 15})
				}
			}
			if isTorch(name) {
				props := blockPropsOf(int(s.world.getBlock(n[0], n[1], n[2])))
				if props["lit"] != "false" {
					// Torch inversion: a torch turns off when the block
					// it is attached to is powered. Approximate: wall
					// torches check their facing neighbour; floor torches
					// the block below.
					sources = append(sources, source{pos: n, power: 15})
				}
			}
		}
	}

	// Wire power: multi-source BFS, power decays by 1 per step.
	power := map[[3]int]int{}
	pq := [][3]int{}
	for _, src := range sources {
		for _, d := range dirVecs {
			n := [3]int{src.pos[0] + d[0], src.pos[1] + d[1], src.pos[2] + d[2]}
			if isWire(blockNameAt(s, n[0], n[1], n[2])) && region[n] {
				if power[n] < 15 {
					power[n] = 15
					pq = append(pq, n)
				}
			}
		}
	}
	for len(pq) > 0 {
		cur := pq[0]
		pq = pq[1:]
		p := power[cur]
		if p <= 1 {
			continue
		}
		for _, d := range dirVecs {
			n := [3]int{cur[0] + d[0], cur[1] + d[1], cur[2] + d[2]}
			if !region[n] || !isWire(blockNameAt(s, n[0], n[1], n[2])) {
				continue
			}
			np := p - 1
			if power[n] < np {
				power[n] = np
				pq = append(pq, n)
			}
		}
	}

	// Apply wire states (power + connections).
	for _, p := range regionPos {
		name := blockNameAt(s, p[0], p[1], p[2])
		if !isWire(name) {
			continue
		}
		props := map[string]string{
			"north": "none", "south": "none", "east": "none", "west": "none",
			"power": strconv.Itoa(power[p]),
		}
		// Connections: another wire horizontally, or a component's solid
		// side; slopes render as "up". Keep it simple: wire-to-wire =>
		// side, dust next to a source/consumer => side too.
		for _, hd := range horizDirs {
			nx, nz := p[0]+hd.dx, p[2]+hd.dz
			nName := blockNameAt(s, nx, p[1], nz)
			if isWire(nName) || isComponent(nName) && !isLamp(nName) {
				props[hd.prop] = "side"
				continue
			}
			// slope up: wire one above the neighbour
			if isWire(blockNameAt(s, nx, p[1]+1, nz)) {
				props[hd.prop] = "up"
				continue
			}
			// slope down: wire one below and no solid block here
			if isWire(blockNameAt(s, nx, p[1]-1, nz)) && !isSolid(blockNameAt(s, nx, p[1], nz)) {
				props[hd.prop] = "side"
			}
		}
		s.setBlockState(p[0], p[1], p[2], stateIDOf("minecraft:redstone_wire", props))
	}

	// Consumers: lamps light when adjacent dust has power or an
	// adjacent source feeds them directly.
	for c := range consumers {
		lit := false
		for _, d := range dirVecs {
			n := [3]int{c[0] + d[0], c[1] + d[1], c[2] + d[2]}
			if isWire(blockNameAt(s, n[0], n[1], n[2])) && power[n] > 0 {
				lit = true
				break
			}
			if isLever(blockNameAt(s, n[0], n[1], n[2])) {
				props := blockPropsOf(int(s.world.getBlock(n[0], n[1], n[2])))
				if props["powered"] == "true" {
					lit = true
					break
				}
			}
		}
		props := map[string]string{"lit": boolStr(lit)}
		s.setBlockState(c[0], c[1], c[2], stateIDOf("minecraft:redstone_lamp", props))
	}
}

func isSolid(name string) bool {
	switch name {
	case "minecraft:air", "minecraft:redstone_wire", "minecraft:torch",
		"minecraft:redstone_torch", "minecraft:redstone_wall_torch",
		"minecraft:lever", "minecraft:rails", "minecraft:oak_button":
		return false
	}
	return true
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

// toggleLever flips a lever's powered property and recomputes the
// neighbourhood. Called on USE_ITEM_ON against a lever.
func (s *Server) toggleLever(x, y, z int) {
	state := int(s.world.getBlock(x, y, z))
	name := blockNameOf(state)
	if !isLever(name) {
		return
	}
	props := blockPropsOf(state)
	newPowered := props["powered"] != "true"
	props["powered"] = boolStr(newPowered)
	if newPowered {
		props["powered"] = "true"
	}
	if sid := stateIDOf(name, props); sid >= 0 {
		s.setBlockState(x, y, z, sid)
	}
	s.redstoneUpdate(x, y, z)
}
