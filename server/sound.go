package server

import (
	"math/rand"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// M9 sound effects. Sounds travel as direct registry holders, so no
// sound_event id table is needed — only the vanilla resource location.
// Every sound gets a fresh random seed like vanilla's level.playSound.

// soundRange2 is the squared radius inside which players hear a sound.
const soundRange2 = 48 * 48

// broadcastSound plays a sound at a world position for every nearby
// player. Caller holds no lock (fan-out is per-connection write-mutexed).
func (s *Server) broadcastSound(soundID string, source int32, x, y, z, volume, pitch float32) {
	seed, err := protocol.RandomLong()
	if err != nil {
		seed = 0
	}
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySound)
	java.WritePlaySound(body, soundID, source, x, y, z, volume, pitch, seed)
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, p := range s.players {
		dx, dy, dz := p.x-float64(x), p.y-float64(y), p.z-float64(z)
		if dx*dx+dy*dy+dz*dz <= soundRange2 {
			_ = p.conn.sendPacket(body.Bytes())
		}
	}
}

// broadcastSoundLocked is the variant for paths that already hold s.mu.
func (s *Server) broadcastSoundLocked(soundID string, source int32, x, y, z, volume, pitch float32) {
	seed, err := protocol.RandomLong()
	if err != nil {
		seed = 0
	}
	body := protocol.NewWriter()
	body.VarInt(v776.PacketPlaySound)
	java.WritePlaySound(body, soundID, source, x, y, z, volume, pitch, seed)
	for _, p := range s.players {
		dx, dy, dz := p.x-float64(x), p.y-float64(y), p.z-float64(z)
		if dx*dx+dy*dy+dz*dz <= soundRange2 {
			_ = p.conn.sendPacket(body.Bytes())
		}
	}
}

// mobSound is the per-species hurt/death sound table.
var mobSound = map[string][2]string{
	// species: {hurt, death}
	"pig":      {"minecraft:entity.pig.hurt", "minecraft:entity.pig.death"},
	"cow":      {"minecraft:entity.cow.hurt", "minecraft:entity.cow.death"},
	"sheep":    {"minecraft:entity.sheep.hurt", "minecraft:entity.sheep.death"},
	"chicken":  {"minecraft:entity.chicken.hurt", "minecraft:entity.chicken.death"},
	"zombie":   {"minecraft:entity.zombie.hurt", "minecraft:entity.zombie.death"},
	"skeleton": {"minecraft:entity.skeleton.hurt", "minecraft:entity.skeleton.death"},
	"creeper":  {"minecraft:entity.creeper.hurt", "minecraft:entity.creeper.death"},
}

// mobSource picks the sound channel: undead/creeper are HOSTILE, animals
// are NEUTRAL (vanilla getSoundSource defaults).
func mobSource(name string) int32 {
	switch name {
	case "zombie", "skeleton", "creeper":
		return v776.SoundSourceHostile
	}
	return v776.SoundSourceNeutral
}

// randomPitch mimics vanilla's 1/(rand*0.4+0.8) pitch jitter.
func randomPitch() float32 {
	return 1 / (rand.Float32()*0.4 + 0.8)
}
