package server

// player is the server-side player model for the play phase.
type player struct {
	conn       *conn
	name       string
	id         int32 // entity id
	x, y, z    float64
	yaw, pitch float32
	cx, cz     int32 // last chunk coordinates

	chunksSent bool // set once the spawn batch has been streamed
}
