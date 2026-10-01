package java

import "errors"

var (
	// ErrUnknownIntent is returned for handshake next-state values other
	// than status or login.
	ErrUnknownIntent = errors.New("java: unknown handshake intent")

	// ErrNotImplemented marks protocol paths that land in a later
	// milestone. The server converts these into a clean disconnect.
	ErrNotImplemented = errors.New("java: not implemented")
)
