package java

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// ParseUndashedUUID parses Mojang's 32-hex-char uuid form.
func ParseUndashedUUID(s string) ([16]byte, error) {
	var out [16]byte
	if len(s) != 32 {
		return out, errors.New("java: undashed uuid must be 32 hex chars")
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return out, err
	}
	copy(out[:], b)
	return out, nil
}

// FormatUUID renders a UUID in the canonical dashed lowercase form.
func FormatUUID(id [16]byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}
