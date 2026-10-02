package server

// player is the server-side player model for the play phase.
type player struct {
	conn       *conn
	name       string
	id         int32 // entity id
	x, y, z    float64
	yaw, pitch float32
	cx, cz     int32 // last chunk coordinates
	onGround   bool

	radius int32             // effective chunk radius (client request, server-capped)
	seen   map[[2]int32]bool // chunks streamed to this player

	// chunksSent is set once the initial batch has been streamed.
	chunksSent bool

	// mining is the current dig, guarded by Server.mu.
	mining *miningState

	// tpsbar marks the personal performance boss bar as visible; the
	// ticker refreshes it once per second. barUUID is minted at spawn.
	tpsbar  bool
	barUUID [16]byte

	// heldSlot is the client's hotbar selection (0-8), guarded by
	// Server.mu like the rest of the player model.
	heldSlot int32

	// sneaking mirrors the last Player Input packet (shift flag).
	sneaking bool

	// armor/offhand extend the inventory model (inv slots 36-39/40);
	// craft grid lives in invMenu, openMenu is the transient container.
	armor    [4]invSlot // head, chest, legs, feet (inv 39..36)
	offhand  invSlot
	invMenu  *menu // container id 0, always open
	openMenu *menu // crafting table window, nil when closed
	// nextWindowID hands out server-side container ids (1+).
	nextWindowID int32

	// gameMode mirrors the gamemode change game event (0 survival..).
	gameMode int32

	// M8 survival state (guarded by Server.mu like the rest of the model).
	health        float32 // 0..20 hearts*2
	food          int32   // 0..20
	saturation    float32
	exhaustion    float32
	foodTickTimer int32 // regen/starvation accumulator
	fallDistance  float32
	voidTicks     int32 // ticks spent below the void threshold
	dead          bool  // death screen shown, awaiting respawn

	expProgress float32
	expLevel    int32
	expTotal    int32

	// Eating state: eatTicksLeft > 0 while consuming eatingFood.
	eatTicksLeft   int32
	eatingFood     foodValue
	sprinting      bool
	lastAttackTick int64 // server tick of the last melee swing

	// seenEnt holds the entity ids streamed to this player, guarded by
	// Server.mu (ticker entity sync touches it).
	seenEnt map[int32]bool

	// seenPlayers tracks which OTHER players this client sees (M8.5);
	// psX..psPitch is the per-tick move baseline for this player's own
	// broadcasts. Guarded by Server.mu.
	seenPlayers    map[int32]bool
	psX, psY, psZ  float64
	psYaw, psPitch float32
	psHas          bool

	// slots is the 36-slot inventory model (0-8 hotbar), guarded by
	// Server.mu. Zero ids are empty stacks.
	slots [36]invSlot
}

// invSlot is one inventory cell: a vanilla item registry id and stack
// size; item 0 / count 0 is an empty slot.
type invSlot struct {
	item  int32
	count int32
}
