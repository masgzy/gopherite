package java

import (
	"encoding/json"
	"io"
)

// HasJoinedResponse models the Mojang session server's
// /session/minecraft/hasJoined answer: the profile id, its name and the
// signed profile properties (usually textures).
type HasJoinedResponse struct {
	ID         string `json:"id"` // undashed 32-hex-char uuid
	Name       string `json:"name"`
	Properties []struct {
		Name      string  `json:"name"`
		Value     string  `json:"value"`
		Signature *string `json:"signature,omitempty"`
	} `json:"properties"`
}

// ParseHasJoined decodes the hasJoined body into the profile UUID and the
// protocol-ready property list.
func ParseHasJoined(r io.Reader) (id [16]byte, props []ProfileProperty, err error) {
	var body HasJoinedResponse
	if err = json.NewDecoder(io.LimitReader(r, 1<<20)).Decode(&body); err != nil {
		return id, nil, err
	}
	id, err = ParseUndashedUUID(body.ID)
	if err != nil {
		return id, nil, err
	}
	props = make([]ProfileProperty, 0, len(body.Properties))
	for _, p := range body.Properties {
		props = append(props, ProfileProperty{
			Name:      p.Name,
			Value:     p.Value,
			Signature: deref(p.Signature),
			Signed:    p.Signature != nil,
		})
	}
	return id, props, nil
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
