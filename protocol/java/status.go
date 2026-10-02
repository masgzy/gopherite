package java

import (
	"encoding/json"

	"github.com/masgzy/gopherite/protocol"
)

// Status packet payloads for the server list ping exchange.
//
// Flow: client sends StatusRequest (empty), server replies with
// StatusResponse carrying the JSON description, client sends PingRequest
// with an arbitrary long, server echoes it in PingPong. The exchange runs
// uncompressed in every version to date.

// StatusRequest is the serverbound request; it carries no fields.
type StatusRequest struct{}

// PlayerSampleEntry is one player shown in the server list hover tooltip.
type PlayerSampleEntry struct {
	Name string `json:"name"`
	ID   string `json:"id"` // offline-mode UUID or account UUID
}

// StatusResponse is the JSON document returned in the server list.
type StatusResponse struct {
	VersionName        string              `json:"-"`
	Protocol           int32               `json:"-"`
	MaxPlayers         int                 `json:"-"`
	OnlinePlayers      int                 `json:"-"`
	Sample             []PlayerSampleEntry `json:"-"`
	DescriptionText    string              `json:"-"`
	Favicon            string              `json:"favicon,omitempty"` // "data:image/png;base64,..."
	EnforcesSecureChat bool                `json:"enforcesSecureChat"`
}

type statusVersion struct {
	Name     string `json:"name"`
	Protocol int32  `json:"protocol"`
}

type statusPlayers struct {
	Max    int                 `json:"max"`
	Online int                 `json:"online"`
	Sample []PlayerSampleEntry `json:"sample,omitempty"`
}

type statusDescription struct {
	Text string `json:"text"`
}

type statusJSON struct {
	Version            statusVersion     `json:"version"`
	Players            statusPlayers     `json:"players"`
	Description        statusDescription `json:"description"`
	Favicon            string            `json:"favicon,omitempty"`
	EnforcesSecureChat bool              `json:"enforcesSecureChat"`
}

// MarshalJSON renders the status document, promoting the convenience
// fields onto their wire locations.
func (s StatusResponse) MarshalJSON() ([]byte, error) {
	return json.Marshal(statusJSON{
		Version:            statusVersion{Name: s.VersionName, Protocol: s.Protocol},
		Players:            statusPlayers{Max: s.MaxPlayers, Online: s.OnlinePlayers, Sample: s.Sample},
		Description:        statusDescription{Text: s.DescriptionText},
		Favicon:            s.Favicon,
		EnforcesSecureChat: s.EnforcesSecureChat,
	})
}

// ReadStatusResponse decodes a serverbound... it decodes the clientbound
// response JSON into a StatusResponse (used by tests and tooling).
func ReadStatusResponse(body []byte) (StatusResponse, error) {
	var raw statusJSON
	if err := json.Unmarshal(body, &raw); err != nil {
		return StatusResponse{}, err
	}
	return StatusResponse{
		VersionName:        raw.Version.Name,
		Protocol:           raw.Version.Protocol,
		MaxPlayers:         raw.Players.Max,
		OnlinePlayers:      raw.Players.Online,
		Sample:             raw.Players.Sample,
		DescriptionText:    raw.Description.Text,
		Favicon:            raw.Favicon,
		EnforcesSecureChat: raw.EnforcesSecureChat,
	}, nil
}

// PingRequest is the serverbound latency probe payload.
func ReadPingRequest(r *protocol.Reader) (int64, error) { return r.Int64() }

// MaxStatusJSONLen bounds the status response document size in characters.
// The 26.2 client reads the whole document with
// ByteBufCodecs.lenientJson(32767), which goes through Utf8String.read and
// throws DecoderException once the decoded string exceeds 32767 chars —
// before any JSON leniency can help, so an oversized document kills the
// server list ping outright instead of degrading gracefully.
const MaxStatusJSONLen = 32767
