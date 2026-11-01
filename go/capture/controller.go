// Package capture joins buffered H264 frames to MP4 recordings, preserving
// outstanding motion recording time across reconnects to the same camera.
package capture

import (
	"bytes"
	"camfreenest/mp4lite"
	"camfreenest/preroll"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type Controller struct {
	mu                                                 sync.Mutex
	clipsDir                                           string
	pre, duration                                      time.Duration
	buf                                                *preroll.Buffer
	epoch                                              uint64
	online, manual, pending, continuing, waitIDR       bool
	sps, pps                                           []byte
	last                                               time.Time
	writer                                             *mp4lite.Writer
	temp, name                                         string
	lastWritten                                        time.Time
	until                                              time.Time // retained only for compatibility with old diagnostics
	lastPTS, firstWrittenPTS, lastWrittenPTS, untilPTS time.Duration
	epochOrigin, epochBase, step                       time.Duration
	firstInEpoch, hasFrame                             bool
	onSaved                                            func(string, error)
	onState                                            func(bool)
	savedCount, dropped                                uint64
}
type Status struct {
	Online           bool    `json:"online"`
	Writing          bool    `json:"writing"`
	Manual           bool    `json:"manual"`
	Pending          bool    `json:"pending"`
	BufferSeconds    float64 `json:"bufferSeconds"`
	CachedFrames     int     `json:"cachedFrames"`
	CachedBytes      int     `json:"cachedBytes"`
	SavedClips       uint64  `json:"savedClips"`
	DroppedFrames    uint64  `json:"droppedFrames"`
	RemainingSeconds float64 `json:"remainingSeconds"`
	ClipSeconds      float64 `json:"clipSeconds"`
}
type ClipMetadata struct {
	DurationSeconds float64 `json:"durationSeconds"`
	EndReason       string  `json:"endReason"`
	Continuation    bool    `json:"continuation"`
}

func New(dir string, pre, duration time.Duration, onSaved func(string, error), onState func(bool)) (*Controller, error) {
	if pre < 0 || pre > 10*time.Second {
		return nil, errors.New("pre-roll must be 0-10s")
	}
	if duration < 5*time.Second || duration > 600*time.Second {
		return nil, errors.New("clip duration must be 5-600 seconds")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	b, err := preroll.New(20*time.Second, 32<<20)
	if err != nil {
		return nil, err
	}
	return &Controller{clipsDir: dir, pre: pre, duration: duration, buf: b, onSaved: onSaved, onState: onState, step: 50 * time.Millisecond, firstInEpoch: true}, nil
}
func (c *Controller) resetBufferLocked() {
	c.buf, _ = preroll.New(20*time.Second, 32<<20)
	if len(c.sps) > 0 && len(c.pps) > 0 {
		c.buf.SetCodec(c.epoch, c.sps, c.pps)
	}
}
func (c *Controller) BeginEpoch(epoch uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch = epoch
	c.online = true
	c.last = time.Time{}
	c.firstInEpoch = true
	c.waitIDR = true
	c.resetBufferLocked()
}

// Suspend keeps an open MP4 and its remaining recording budget. A transient
// WebRTC disconnect must not masquerade as a completed three-second recording.
func (c *Controller) Suspend() { c.mu.Lock(); defer c.mu.Unlock(); c.online = false; c.waitIDR = true }
func (c *Controller) Offline() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.online = false
	c.pending = false
	c.finishLocked("recorder stopped")
	c.manual = false
	c.continuing = false
}
func (c *Controller) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.online = false
	c.pending = false
	c.finishLocked("service stopped")
	c.manual = false
}
func (c *Controller) SetDuration(d time.Duration) error {
	if d < 5*time.Second || d > 600*time.Second {
		return errors.New("duration must be 5-600 seconds")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	old := c.duration
	c.duration = d
	if d > old && c.writer != nil && !c.manual && c.lastPTS+d > c.untilPTS {
		c.untilPTS = c.lastPTS + d
	}
	return nil
}
func (c *Controller) codecLocked(sps, pps []byte) {
	sps, pps = mp4lite.CanonicalParameterSet(sps), mp4lite.CanonicalParameterSet(pps)
	if len(sps) == 0 {
		sps = c.sps
	}
	if len(pps) == 0 {
		pps = c.pps
	}
	if bytes.Equal(sps, c.sps) && bytes.Equal(pps, c.pps) {
		return
	}
	if len(c.sps) > 0 && len(c.pps) > 0 && bytes.Equal(pps, c.pps) && mp4lite.SPSCompatible(sps, c.sps) {
		// Nest can update HRD bitrate/buffer metadata without changing the
		// video format. Keep recording and use the newest config for new clips.
		c.sps = append([]byte(nil), sps...)
		c.buf.SetCodec(c.epoch, c.sps, c.pps)
		return
	}
	resume := c.writer != nil || c.pending
	manual := c.manual
	deadline := c.untilPTS
	c.finishLocked("codec changed; recording continues in next file")
	c.sps = append([]byte(nil), sps...)
	c.pps = append([]byte(nil), pps...)
	c.pending = resume
	c.manual = manual
	c.untilPTS = deadline
	c.continuing = resume && deadline > c.lastPTS
	c.firstInEpoch = true
	c.waitIDR = true
	c.resetBufferLocked()
}
func (c *Controller) SetCodecFromSDP(sps, pps []byte) {
	if len(sps) < 4 || len(pps) < 2 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.online {
		c.codecLocked(sps, pps)
	}
}
func (c *Controller) Push(u preroll.Unit) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.online || u.Epoch != c.epoch {
		return errors.New("frame from inactive stream")
	}
	if !c.last.IsZero() && !u.At.After(c.last) {
		c.dropped++
		return errors.New("duplicate/out-of-order frame")
	}
	sps, pps, idr, vcl := mp4lite.NALInfo(u.Data)
	// Commit SPS and PPS together: separate updates used to close a clip twice.
	c.codecLocked(sps, pps)
	if !vcl {
		return nil
	}
	if len(c.sps) == 0 || len(c.pps) == 0 {
		c.dropped++
		return errors.New("waiting for H264 SPS/PPS")
	}
	if c.waitIDR && !idr {
		c.dropped++
		return nil
	}
	c.waitIDR = false
	if c.firstInEpoch {
		c.epochOrigin = u.PTS
		c.epochBase = 0
		if c.hasFrame {
			c.epochBase = c.lastPTS + c.step
		}
		c.firstInEpoch = false
	}
	u.PTS = c.epochBase + u.PTS - c.epochOrigin
	if c.hasFrame && u.PTS <= c.lastPTS {
		c.dropped++
		return errors.New("non-monotonic media time")
	}
	if c.hasFrame {
		d := u.PTS - c.lastPTS
		if d > 0 && d <= time.Second {
			c.step = d
		}
	}
	u.IsKeyframe = idr
	c.last = u.At
	c.lastPTS = u.PTS
	c.hasFrame = true
	if err := c.buf.Push(u); err != nil {
		c.dropped++
		return err
	}
	if c.pending && c.writer == nil {
		c.startLocked(u.At, 0)
	}
	if c.writer != nil && u.PTS > c.lastWrittenPTS {
		if err := c.writer.Write(u.Data, u.PTS); err != nil {
			c.abortLocked(fmt.Errorf("MP4 write: %w", err))
			return err
		}
		c.lastWritten = u.At
		c.lastWrittenPTS = u.PTS
	}
	if c.writer != nil && !c.manual && u.PTS >= c.untilPTS {
		c.finishLocked("duration reached")
	}
	return nil
}
func (c *Controller) Trigger(eventAt time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.online && c.writer == nil {
		return false
	}
	if c.writer != nil {
		if end := c.lastPTS + c.duration; end > c.untilPTS {
			c.untilPTS = end
		}
		return true
	}
	if c.last.IsZero() {
		c.pending = true
		return true
	}
	latency := time.Duration(0)
	if !eventAt.IsZero() && c.last.After(eventAt) {
		latency = c.last.Sub(eventAt)
	}
	if latency > 10*time.Second {
		latency = 10 * time.Second
	}
	return c.startLocked(c.last, latency)
}
func (c *Controller) startLocked(at time.Time, latency time.Duration) bool {
	snap, ok := c.buf.Get(at, c.pre+latency)
	if !ok || len(snap.Codec.SPS) == 0 || len(snap.Codec.PPS) == 0 {
		c.pending = true
		return true
	}
	c.name = time.Now().UTC().Format("20060102-150405.000000000") + ".mp4"
	c.temp = filepath.Join(c.clipsDir, c.name+".part")
	out, err := mp4lite.New(c.temp, snap.Codec.SPS, snap.Codec.PPS)
	if err != nil {
		c.abortLocked(err)
		return false
	}
	c.writer = out
	c.pending = false
	c.firstWrittenPTS = snap.Units[0].PTS
	if c.onState != nil {
		c.onState(true)
	}
	for _, u := range snap.Units {
		if err := out.Write(u.Data, u.PTS); err != nil {
			c.abortLocked(err)
			return false
		}
		c.lastWritten = u.At
		c.lastWrittenPTS = u.PTS
	}
	if !c.continuing {
		c.untilPTS = c.lastPTS + c.duration
	}
	c.continuing = false
	return true
}
func (c *Controller) finishLocked(reason string) {
	if c.writer == nil {
		return
	}
	writer := c.writer
	c.writer = nil
	meta := ClipMetadata{DurationSeconds: (c.lastWrittenPTS - c.firstWrittenPTS + c.step).Seconds(), EndReason: reason, Continuation: reason == "codec changed; recording continues in next file"}
	err := writer.Close()
	if err == nil {
		err = os.Rename(c.temp, filepath.Join(c.clipsDir, c.name))
	}
	if err != nil {
		_ = os.Remove(c.temp)
	} else {
		c.savedCount++
		data, _ := json.Marshal(meta)
		_ = os.WriteFile(filepath.Join(c.clipsDir, c.name+".meta.json"), data, 0600)
	}
	c.manual = false
	if c.onState != nil {
		c.onState(false)
	}
	if c.onSaved != nil {
		c.onSaved(c.name, err)
	}
}
func (c *Controller) abortLocked(err error) {
	if c.writer != nil {
		c.writer.Abort()
		c.writer = nil
	}
	c.pending = false
	c.manual = false
	if c.onState != nil {
		c.onState(false)
	}
	if c.onSaved != nil {
		c.onSaved(c.name, err)
	}
}
func (c *Controller) StartManual() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.online && c.writer == nil {
		return false
	}
	c.manual = true
	if c.writer != nil {
		return true
	}
	if c.last.IsZero() {
		c.pending = true
		return true
	}
	return c.startLocked(c.last, 0)
}
func (c *Controller) StopRecording() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending = false
	c.finishLocked("stopped by user")
	c.manual = false
	c.continuing = false
}
func (c *Controller) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	frames, size, age := c.buf.Stats()
	remaining := time.Duration(0)
	if c.writer != nil {
		remaining = c.untilPTS - c.lastPTS
		if remaining < 0 {
			remaining = 0
		}
	}
	return Status{Online: c.online, Writing: c.writer != nil, Manual: c.manual, Pending: c.pending, BufferSeconds: age.Seconds(), CachedFrames: frames, CachedBytes: size, SavedClips: c.savedCount, DroppedFrames: c.dropped, RemainingSeconds: remaining.Seconds(), ClipSeconds: c.duration.Seconds()}
}
