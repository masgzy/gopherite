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
}
