package server

import (
	"math"
	"math/rand"
	"sync"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// Entity system (M5). The core is deliberately small: entities tick on
// the server ticker, are tracked per player like chunks, and each type
// implements its own behaviour. Item entities are the first tenant —
// they carry the mined-block drops, gravity physics and walk-over
// pickup — and the same skeleton hosts mobs later.

// entity is one tracked server-side object. alive()==false retires the
// entity at the end of the tick; netPos feeds the delta-move broadcasts.
type entity interface {
	entityID() int32
	typeID() int32
	xPos() (x, y, z float64)
	onGroundFlag() bool
	netPos() (x, y, z float64, ok bool)
	setNetPos(x, y, z float64)
	tick(s *Server)
	alive() bool
	metadataDirty() bool
	clearMetadataDirty()
}

// itemEntity is a dropped stack: superflat's bread and butter.
type itemEntity struct {
	mu         sync.Mutex // guards the physics fields; lock order e.mu -> Server.mu
	id         int32
	uuid       [16]byte
	x, y, z    float64
	vx, vy, vz float64
	ground     bool

	itemID int32 // vanilla item registry index
	count  int32

	age         int32 // ticks lived; despawn at 6000
	pickupDelay int32 // ticks before players can collect

	nx, ny, nz float64 // last broadcast position
	hasNet     bool
	dead       bool
	metaDirty  bool
}

// vanilla ItemEntity constants.
const (
	itemGravity        = 0.04
	itemAirDragH       = 0.98
	itemGroundFriction = 0.91
	itemHalfWidth      = 0.125 // 0.25-wide collision box
	itemPickupDist2    = 1.0   // squared distance for walk-over collection
	itemLifespanTicks  = 6000  // five minutes
	itemTerminalVy     = -3.92 // vanilla terminal velocity for items
	itemMaxStack       = 64
)

// newItemEntity mints a dropped stack with vanilla block-drop motion:
// a small upward pop plus horizontal jitter, ten ticks of pickup delay.
func newItemEntity(id int32, x, y, z float64, itemID, count int32) *itemEntity {
	var u [16]byte
	_, _ = rand.Read(u[:])
	u[6] = (u[6] & 0x0F) | 0x40
	u[8] = (u[8] & 0x3F) | 0x80
	return &itemEntity{
		id:          id,
		uuid:        u,
		x:           x,
		y:           y,
		z:           z,
		vx:          (rand.Float64() - 0.5) * 0.2,
		vy:          0.2 + rand.Float64()*0.1,
		vz:          (rand.Float64() - 0.5) * 0.2,
		itemID:      itemID,
		count:       count,
		pickupDelay: 10,
	}
}

func (e *itemEntity) entityID() int32 { return e.id }
func (e *itemEntity) typeID() int32   { return v776.EntityTypeItem }
func (e *itemEntity) xPos() (float64, float64, float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.x, e.y, e.z
}
func (e *itemEntity) onGroundFlag() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ground
}
func (e *itemEntity) alive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.dead
}
func (e *itemEntity) metadataDirty() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.metaDirty
}
func (e *itemEntity) clearMetadataDirty() {
	e.mu.Lock()
	e.metaDirty = false
	e.mu.Unlock()
}
func (e *itemEntity) netPos() (float64, float64, float64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nx, e.ny, e.nz, e.hasNet
}
func (e *itemEntity) setNetPos(x, y, z float64) {
	e.mu.Lock()
	e.nx, e.ny, e.nz, e.hasNet = x, y, z, true
	e.mu.Unlock()
}

// snapshot exposes the physics state for observers (tests, debug UIs).
func (e *itemEntity) snapshot() (x, y, z, vx, vy, vz float64, ground bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.x, e.y, e.z, e.vx, e.vy, e.vz, e.ground
}

// tick advances one server tick: gravity, axis-separated collision,
// friction, pickup and aging.
func (e *itemEntity) tick(s *Server) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.age++
	if e.age >= itemLifespanTicks {
		e.dead = true
		return
	}
	if e.pickupDelay > 0 {
		e.pickupDelay--
	}

	e.vy -= itemGravity
	if e.ground {
		e.vx *= itemGroundFriction
		e.vz *= itemGroundFriction
	} else {
		e.vx *= itemAirDragH
		e.vz *= itemAirDragH
	}
	if e.vy < itemTerminalVy {
		e.vy = itemTerminalVy
	}

	// Y axis first so ground state is fresh for horizontal friction.
	ny := e.y + e.vy
	if e.vy != 0 && s.entityBoxCollides(e.x, ny, e.z) {
		if e.vy < 0 {
			// Snap flush onto the block surface below (top of the cell
			// the box would have entered).
			ny = math.Floor(ny) + 1
		}
		e.vy = 0
	}
	e.y = ny
	e.ground = s.entityBoxCollides(e.x, e.y-0.001, e.z)

	// X / Z axes.
	nx := e.x + e.vx
	if e.vx != 0 && s.entityBoxCollides(nx, e.y, e.z) {
		nx = e.x
		e.vx = 0
	}
	e.x = nx

	nz := e.z + e.vz
	if e.vz != 0 && s.entityBoxCollides(e.x, e.y, nz) {
		nz = e.z
		e.vz = 0
	}
	e.z = nz

	if e.pickupDelay == 0 {
		e.tryPickup(s)
	}
}

// tryPickup collects the stack into any player within range. The give
// happens under the server lock; packet pushes are deferred to flags so
// the sync pass fans them out to every tracker.
func (e *itemEntity) tryPickup(s *Server) {
	s.mu.Lock()
	var taker *player
	for _, p := range s.players {
		dx := p.x - e.x
		dy := p.y - e.y
		dz := p.z - e.z
		if dx*dx+dy*dy+dz*dz < itemPickupDist2 {
			taker = p
			break
		}
	}
	if taker == nil {
		s.mu.Unlock()
		return
	}
	remaining := taker.giveItem(e.itemID, e.count)
	s.mu.Unlock()
	if remaining == e.count {
		return // inventory full: leave the item
	}
	taken := e.count - remaining
	e.count = remaining
	e.metaDirty = true
	if remaining == 0 {
		e.dead = true
	}
	// Collector feedback: animation + inventory slot refresh. Safe from
	// this goroutine (write-mutexed per conn).
	taker.conn.sendTakeItem(e.id, taker.id, taken)
}

// xpOrbEntity is a floating experience orb: item-like physics plus a
// magnet pull toward nearby players. The orb value rides server-side only
// (the client renders the default small orb without metadata).
type xpOrbEntity struct {
	mu         sync.Mutex
	id         int32
	uuid       [16]byte
	x, y, z    float64
	vx, vy, vz float64
	ground     bool

	value int32
	age   int32

	nx, ny, nz float64
	hasNet     bool
	dead       bool
}

func newXPOrbEntity(id int32, x, y, z float64, value int32) *xpOrbEntity {
	var u [16]byte
	_, _ = rand.Read(u[:])
	u[6] = (u[6] & 0x0F) | 0x40
	u[8] = (u[8] & 0x3F) | 0x80
	return &xpOrbEntity{id: id, uuid: u, x: x, y: y, z: z, value: value}
}

func (e *xpOrbEntity) entityID() int32 { return e.id }
func (e *xpOrbEntity) typeID() int32   { return v776.EntityTypeXPOrb }
func (e *xpOrbEntity) xPos() (float64, float64, float64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.x, e.y, e.z
}
func (e *xpOrbEntity) onGroundFlag() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ground
}
func (e *xpOrbEntity) alive() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return !e.dead
}
func (e *xpOrbEntity) metadataDirty() bool { return false }
func (e *xpOrbEntity) clearMetadataDirty() {}
func (e *xpOrbEntity) netPos() (float64, float64, float64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.nx, e.ny, e.nz, e.hasNet
}
func (e *xpOrbEntity) setNetPos(x, y, z float64) {
	e.mu.Lock()
	e.nx, e.ny, e.nz, e.hasNet = x, y, z, true
	e.mu.Unlock()
}

// tick: gravity, drag, magnet pull, pickup, aging — mirrors itemEntity.
func (e *xpOrbEntity) tick(s *Server) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.age++
	if e.age >= itemLifespanTicks {
		e.dead = true
		return
	}

	// Magnet: drift toward the nearest player within 4 blocks.
	s.mu.Lock()
	var tx, ty, tz float64
	found := false
	for _, p := range s.players {
		dx := p.x - e.x
		dy := p.y + 0.5 - e.y
		dz := p.z - e.z
		d2 := dx*dx + dy*dy + dz*dz
		if d2 < 16 && d2 > 0.01 {
			tx, ty, tz, found = dx, dy, dz, true
			break
		}
	}
	var taker *player
	if found {
		d := math.Sqrt(tx*tx + ty*ty + tz*tz)
		if d < 0.6 {
			for _, p := range s.players {
				dx := p.x - e.x
				dy := p.y + 0.5 - e.y
				dz := p.z - e.z
				if dx*dx+dy*dy+dz*dz < 1.0 {
					taker = p
					break
				}
			}
		} else {
			e.vx += tx / d * 0.035
			e.vy += ty / d * 0.035
			e.vz += tz / d * 0.035
		}
	}
	if taker != nil {
		taker.addXP(e.value)
	}
	s.mu.Unlock()
	if taker != nil {
		e.dead = true
		taker.conn.sendTakeItem(e.id, taker.id, 1)
		return
	}

	e.vy -= itemGravity
	e.vx *= itemAirDragH
	e.vz *= itemAirDragH
	if e.ground {
		e.vx *= itemGroundFriction
		e.vz *= itemGroundFriction
	}
	if e.vy < itemTerminalVy {
		e.vy = itemTerminalVy
	}
	ny := e.y + e.vy
	if e.vy != 0 && s.entityBoxCollides(e.x, ny, e.z) {
		if e.vy < 0 {
			ny = math.Floor(ny) + 1
		}
		e.vy = 0
	}
	e.y = ny
	e.ground = s.entityBoxCollides(e.x, e.y-0.001, e.z)
	nx := e.x + e.vx
	if e.vx != 0 && s.entityBoxCollides(nx, e.y, e.z) {
		nx = e.x
		e.vx = 0
	}
	e.x = nx
	nz := e.z + e.vz
	if e.vz != 0 && s.entityBoxCollides(e.x, e.y, nz) {
		nz = e.z
		e.vz = 0
	}
	e.z = nz
}

// ---- manager ----

// spawnEntity registers an entity; it starts streaming to nearby players
// on the next tick's entity sync.
func (s *Server) spawnEntity(e entity) {
	s.mu.Lock()
	s.entities[e.entityID()] = e
	s.mu.Unlock()
}

// allocEntityID hands out server-unique entity ids; players share the
// vanilla counter.
func (s *Server) allocEntityID() int32 {
	return s.nextEntityID.Add(1)
}

// entityBoxCollides reports whether the item collision box (0.25 cube,
// bottom center at x/z, bottom at y) intersects any solid block.
func (s *Server) entityBoxCollides(x, y, z float64) bool {
	const r = itemHalfWidth
	for bx := int(math.Floor(x - r)); bx <= int(math.Floor(x+r)); bx++ {
		for by := int(math.Floor(y)); by <= int(math.Floor(y+2*r)); by++ {
			for bz := int(math.Floor(z - r)); bz <= int(math.Floor(z+r)); bz++ {
				if s.world.getBlock(bx, by, bz) != stateAir {
					return true
				}
			}
		}
	}
	return false
}

// encodeSpawn writes the spawn packet body (packet id included).
func (s *Server) encodeSpawn(w *protocol.Writer, e entity) {
	w.VarInt(v776.PacketPlayAddEntity)
	switch it := e.(type) {
	case *itemEntity:
		// data=1 mirrors vanilla's item spawn hint; zero initial velocity
		// since trackers receive the true motion through move packets.
		java.WriteAddEntity(w, it.id, it.uuid, it.typeID(), it.x, it.y, it.z, 0, 0, 0, 0, 0, 0, 1)
	case *mobEntity:
		encodeMobSpawn(w, it)
	case *xpOrbEntity:
		java.WriteAddEntity(w, it.id, it.uuid, it.typeID(), it.x, it.y, it.z, 0, 0, 0, 0, 0, 0, 0)
	default:
		// future types plug in here
		java.WriteAddEntity(w, e.entityID(), [16]byte{}, e.typeID(), 0, 0, 0, 0, 0, 0, 0, 0, 0, 0)
	}
}

// encodeMetadata writes the per-type metadata packet for freshly tracked
// players (items: their stack, so the client can render them).
func (s *Server) encodeMetadata(w *protocol.Writer, e entity) {
	if it, ok := e.(*itemEntity); ok {
		w.VarInt(v776.PacketPlaySetEntityData)
		java.WriteSetEntityDataItem(w, it.id, it.itemID, it.count)
	}
}
