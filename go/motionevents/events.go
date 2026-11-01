// Package motionevents interprets Nest SDM Pub/Sub payloads without tying the
// recording trigger to an account-specific SDM device resource ID.
package motionevents

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

const (
	MaxEventAge   = 2 * time.Minute
	MaxFutureSkew = 15 * time.Second
)

type Message struct {
	EventID        string `json:"eventId"`
	Timestamp      string `json:"timestamp"`
	UserID         string `json:"userId"`
	ResourceUpdate struct {
		Name   string                     `json:"name"`
		Events map[string]json.RawMessage `json:"events"`
	} `json:"resourceUpdate"`
}
type Trigger struct {
	ID         string
	At         time.Time
	Kind       string
	DeviceName string
	UserID     string
}

// Parse returns a genuine, recent camera event. Device IDs may differ by
// authorized Google account and intentionally are NOT compared to the device
// name used for WebRTC. Only SDM camera events can trigger a clip.
func Parse(raw []byte, pubsubMessageID string, now time.Time) (Trigger, error) {
	var m Message
	if err := json.Unmarshal(raw, &m); err != nil {
		return Trigger{}, err
	}
	if !strings.HasPrefix(m.ResourceUpdate.Name, "enterprises/") || !strings.Contains(m.ResourceUpdate.Name, "/devices/") {
		return Trigger{}, errors.New("not a Nest camera device event")
	}
	var kind string
	for k := range m.ResourceUpdate.Events {
		if strings.HasPrefix(k, "sdm.devices.events.Camera") || strings.HasPrefix(k, "sdm.devices.events.Doorbell") {
			kind = k
			break
		}
	}
	if kind == "" {
		return Trigger{}, errors.New("no Nest camera event")
	}
	at := now
	if m.Timestamp != "" {
		t, err := time.Parse(time.RFC3339Nano, m.Timestamp)
		if err != nil {
			return Trigger{}, errors.New("invalid event timestamp")
		}
		at = t
	}
	if now.Sub(at) > MaxEventAge || at.Sub(now) > MaxFutureSkew {
		return Trigger{}, errors.New("motion event outside freshness window")
	}
	id := m.EventID
	if id == "" {
		id = pubsubMessageID
	}
	if id == "" {
		return Trigger{}, errors.New("event without deduplication ID")
	}
	return Trigger{ID: id, At: at, Kind: kind, DeviceName: m.ResourceUpdate.Name, UserID: m.UserID}, nil
}
