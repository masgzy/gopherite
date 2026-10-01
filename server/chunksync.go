package server

import (
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// Chunk streaming for the play phase: keeps each player's seen set in
// sync with its radius. M2 streamed the spawn square once; M3 adds
// incremental loads and unload notifications as the player moves.

// syncChunks sends newly visible chunks (wrapped in a batch) and forgets
// chunks that fell out of the radius. Call after p.cx/p.cz change and
// once after the player loads.
func (c *conn) syncChunks() error {
	p := c.player
	if p == nil {
		return nil
	}
	r := p.radius
	if r <= 0 {
		r = 1
	}
	if p.seen == nil {
		p.seen = make(map[[2]int32]bool)
	}

	var sendList [][2]int32
	for dx := -r; dx <= r; dx++ {
		for dz := -r; dz <= r; dz++ {
			key := [2]int32{p.cx + dx, p.cz + dz}
			if !p.seen[key] {
				sendList = append(sendList, key)
			}
		}
	}
	var forgetList [][2]int32
	for key := range p.seen {
		if dx, dz := key[0]-p.cx, key[1]-p.cz; dx > r || dx < -r || dz > r || dz < -r {
			forgetList = append(forgetList, key)
		}
	}
	if len(sendList) == 0 && len(forgetList) == 0 {
		return nil
	}

	// Vanilla batches chunk sends so the client can pace itself.
	if len(sendList) > 0 {
		// Near-first ordering keeps the area around the player filled
		// before the fringe.
		sortChunkKeysByDist(sendList, p.cx, p.cz)
		c.wr.Reset()
		c.wr.VarInt(v776.PacketPlayChunkBatchStart)
		if err := c.sendPacket(c.wr.Bytes()); err != nil {
			return err
		}
		for _, key := range sendList {
			data, err := c.s.world.chunkData(key[0], key[1])
			if err != nil {
				return err
			}
			c.wr.Reset()
			c.wr.VarInt(v776.PacketPlayLevelChunk)
			c.wr.FixedBytes(data)
			if err := c.sendPacket(c.wr.Bytes()); err != nil {
				return err
			}
			p.seen[key] = true
		}
		c.wr.Reset()
		c.wr.VarInt(v776.PacketPlayCBChunkBatchDone)
		c.wr.VarInt(int32(len(sendList)))
		if err := c.sendPacket(c.wr.Bytes()); err != nil {
			return err
		}
	}
	for _, key := range forgetList {
		c.wr.Reset()
		c.wr.VarInt(v776.PacketPlayForgetChunk)
		java.WriteForgetChunk(c.wr, key[0], key[1])
		if err := c.sendPacket(c.wr.Bytes()); err != nil {
			return err
		}
		delete(p.seen, key)
	}
	return nil
}

// sortChunkKeysByDist orders keys by chebyshev distance from the center.
func sortChunkKeysByDist(keys [][2]int32, cx, cz int32) {
	dist := func(k [2]int32) int {
		dx := k[0] - cx
		dz := k[1] - cz
		if dx < 0 {
			dx = -dx
		}
		if dz < 0 {
			dz = -dz
		}
		if dx > dz {
			return int(dx)
		}
		return int(dz)
	}
	// insertion sort: the slice is small (radius <= 32 -> <= 6561)
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && dist(keys[j]) < dist(keys[j-1]); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
}
