// Package preroll implements a bounded, in-memory rolling buffer for complete
// encoded H.264 access units received from a persistent camera WebRTC stream.
//
// It does not open or renew a Nest stream and does not mux MP4. Integrate this
// between Pion SampleBuilder and the existing MP4 writer. Never feed raw RTP
// packets or incomplete fragmented NALs to this buffer.
package preroll

import (
	"errors"
	"sync"
	"time"
)

var ErrInvalidUnit = errors.New("preroll: invalid access unit")

// Unit contains exactly one complete depacketized H.264 access unit.
// At is the mapped camera/media capture time (not the motion notification time).
// PTS is the normalized presentation timestamp used by the MP4 writer.
// Data uses Annex B encoding. IsKeyframe must be true only for an IDR frame.
// Epoch identifies one uninterrupted WebRTC stream; changes flush stale frames.
type Unit struct {
	At         time.Time
	PTS        time.Duration
	Data       []byte
	IsKeyframe bool
	Epoch      uint64
}

type CodecConfig struct {
	SPS []byte
	PPS []byte
}

type Snapshot struct {
	Units                  []Unit
	Codec                  CodecConfig
	DesiredStart           time.Time
	ActualStart            time.Time
	AvailableBeforeTrigger time.Duration
	// When true, an earlier keyframe was unavailable and the clip starts later
	// than the requested pre-event time. Never write non-IDR samples first.
	Truncated bool
}

type Buffer struct {
	mu          sync.Mutex
	retention   time.Duration
	maxBytes    int
	units       []Unit
	bytes       int
	epoch       uint64
	initialized bool
	codec       CodecConfig
}

// New returns a buffer with hard byte and age caps. Store at least twice the
// requested pre-event duration to leave room for an earlier keyframe/GOP.
func New(retention time.Duration, maxBytes int) (*Buffer, error) {
	if retention <= 0 || maxBytes <= 0 {
		return nil, errors.New("preroll: retention and maxBytes must be positive")
	}
	return &Buffer{retention: retention, maxBytes: maxBytes}, nil
}

func dup(p []byte) []byte { return append([]byte(nil), p...) }

// SetCodec records SPS/PPS for the active stream. Use the exact parameter sets
// associated with the IDR being emitted. On parameter-set changes, flush the
// ring and start a fresh stream epoch to avoid corrupt mixed-codec MP4 files.
func (b *Buffer) SetCodec(epoch uint64, sps, pps []byte) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checkEpoch(epoch)
	b.codec = CodecConfig{SPS: dup(sps), PPS: dup(pps)}
}

func (b *Buffer) checkEpoch(epoch uint64) {
	if !b.initialized || b.epoch != epoch {
		b.units = nil
		b.bytes = 0
		b.codec = CodecConfig{}
		b.epoch = epoch
		b.initialized = true
	}
}

func (b *Buffer) evictOldest() {
	if len(b.units) == 0 {
		return
	}
	b.bytes -= len(b.units[0].Data)
	b.units[0].Data = nil
	b.units = b.units[1:]
}

// Push never retains caller-owned memory. Feed ordered complete samples; RTP
// packet reordering and H.264 depacketizing belong upstream in SampleBuilder.
func (b *Buffer) Push(unit Unit) error {
	if unit.At.IsZero() || len(unit.Data) == 0 || len(unit.Data) > b.maxBytes {
		return ErrInvalidUnit
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.checkEpoch(unit.Epoch)
	if n := len(b.units); n > 0 && !unit.At.After(b.units[n-1].At) {
		// Never allow stale/out-of-order samples to disturb MP4 timestamps.
		return ErrInvalidUnit
	}
	unit.Data = dup(unit.Data)
	b.units = append(b.units, unit)
	b.bytes += len(unit.Data)
	cutoff := unit.At.Add(-b.retention)
	for len(b.units) > 0 && (b.units[0].At.Before(cutoff) || b.bytes > b.maxBytes) {
		b.evictOldest()
	}
	return nil
}

// Get selects the earliest decodable pre-motion output by finding the latest
// IDR at or before trigger-preRoll. If that keyframe has been evicted it uses
// the first subsequent IDR, and reports the shortage. It never includes
// future frames beyond triggerAt; ongoing live capture must be joined by the
// caller using the same stream, sample timebase, and serial ingest loop.
func (b *Buffer) Get(triggerAt time.Time, preRoll time.Duration) (Snapshot, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if triggerAt.IsZero() || preRoll < 0 || len(b.units) == 0 {
		return Snapshot{}, false
	}
	target := triggerAt.Add(-preRoll)
	earliestIDR := -1
	earlierIDR := -1
	end := -1
	for i, u := range b.units {
		if u.At.After(triggerAt) {
			break
		}
		end = i
		if u.IsKeyframe {
			if earliestIDR == -1 {
				earliestIDR = i
			}
			if !u.At.After(target) {
				earlierIDR = i
			}
		}
	}
	if end == -1 || earliestIDR == -1 {
		return Snapshot{}, false
	}
	start := earlierIDR
	if start == -1 {
		start = earliestIDR
	}
	units := make([]Unit, 0, end-start+1)
	for _, u := range b.units[start : end+1] {
		u.Data = dup(u.Data)
		units = append(units, u)
	}
	s := Snapshot{
		Units:                  units,
		Codec:                  CodecConfig{SPS: dup(b.codec.SPS), PPS: dup(b.codec.PPS)},
		DesiredStart:           target,
		ActualStart:            units[0].At,
		AvailableBeforeTrigger: triggerAt.Sub(units[0].At),
		Truncated:              earlierIDR == -1,
	}
	return s, true
}

func (b *Buffer) Stats() (units, bytes int, age time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.units) == 0 {
		return 0, 0, 0
	}
	return len(b.units), b.bytes, b.units[len(b.units)-1].At.Sub(b.units[0].At)
}
