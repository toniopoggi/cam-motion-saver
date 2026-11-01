package engine

import (
	"bytes"
	"camfreenest/localmotion"
	"camfreenest/mp4lite"
	"encoding/binary"
	"sync"
	"time"
)

// A bounded decoder queue is independent of the recording/pre-roll path.
type motionState struct {
	mu                        sync.Mutex
	enabled                   bool
	sensitivity               int
	generation, epoch         uint64
	sps, pps                  []byte
	waitKey                   bool
	queue                     chan []byte
	detector                  *localmotion.Detector
	status                    string
	lastFrame                 time.Time
	frames, triggers, dropped uint64
	area                      localmotion.Region
	liveRequested             time.Time
	liveFrame                 []byte
	liveFrameAt               time.Time
	previewError              bool
}

// drainLocked never blocks if the Android consumer races the reset.
func (m *motionState) drainLocked() {
	for {
		select {
		case <-m.queue:
		default:
			return
		}
	}
}

func (r *Runner) initMotion() {
	c := r.loadConfig()
	r.motion.queue = make(chan []byte, 12)
	r.motion.configure(c)
}
func (m *motionState) configure(c Configuration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	en := !c.LocalMotionDisabled
	if m.enabled != en {
		m.generation++
		m.waitKey = true
		m.detector = nil
		m.drainLocked()
	}
	m.enabled = en
	m.sensitivity = c.MotionSensitivity
	area := motionArea(c)
	if m.area != area {
		m.area = area
		m.detector = nil
	}
	if !en {
		m.status = "Phone motion detection off"
	} else if m.status == "" || m.status == "Phone motion detection off" {
		m.status = "Waiting for video and phone decoder"
	}
}
func (m *motionState) newEpoch(epoch uint64, sps, pps []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.epoch = epoch
	m.generation++
	m.waitKey = true
	m.detector = nil
	m.liveFrame = nil
	m.liveFrameAt = time.Time{}
	m.previewError = false
	m.sps = append([]byte(nil), sps...)
	m.pps = append([]byte(nil), pps...)
	m.drainLocked()
}
func (m *motionState) offer(epoch uint64, pts int64, data []byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (!m.enabled && time.Since(m.liveRequested) > 4*time.Second) || epoch != m.epoch {
		return
	}
	if len(data) > 2*1024*1024 {
		m.dropped++
		m.status = "Phone detector: video frame too large"
		m.generation++
		m.waitKey = true
		m.detector = nil
		m.drainLocked()
		return
	}
	s, p, key, _ := mp4lite.NALInfo(data)
	if (len(s) > 0 && len(m.sps) > 0 && !mp4lite.SPSCompatible(s, m.sps)) || (len(p) > 0 && len(m.pps) > 0 && !bytes.Equal(p, m.pps)) {
		m.generation++
		m.waitKey = true
		m.detector = nil
		m.drainLocked()
	}
	if len(s) > 0 {
		m.sps = append(m.sps[:0], s...)
	}
	if len(p) > 0 {
		m.pps = append(m.pps[:0], p...)
	}
	if m.waitKey && (!key || len(m.sps) == 0 || len(m.pps) == 0) {
		return
	}
	w, h, e := mp4lite.Dimensions(m.sps)
	if e != nil {
		return
	}
	if len(m.queue) == cap(m.queue) {
		m.dropped++
		m.generation++
		m.waitKey = true
		m.detector = nil
		m.drainLocked()
		if !key {
			return
		}
	}
	payload := data
	if m.waitKey {
		payload = make([]byte, 0, 8+len(m.sps)+len(m.pps)+len(data))
		payload = append(payload, 0, 0, 0, 1)
		payload = append(payload, m.sps...)
		payload = append(payload, 0, 0, 0, 1)
		payload = append(payload, m.pps...)
		payload = append(payload, data...)
		m.waitKey = false
	}
	packet := make([]byte, 20+len(payload))
	binary.BigEndian.PutUint64(packet[:8], m.generation)
	binary.BigEndian.PutUint64(packet[8:16], uint64(pts))
	binary.BigEndian.PutUint16(packet[16:18], w)
	binary.BigEndian.PutUint16(packet[18:20], h)
	copy(packet[20:], payload)
	m.queue <- packet
}
func (m *motionState) snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	status := m.status
	if m.enabled && status == "Phone motion detection active" && time.Since(m.lastFrame) > 5*time.Second {
		status = "Waiting for decoded video"
	}
	return map[string]any{"enabled": m.enabled, "status": status, "analysedFrames": m.frames, "triggers": m.triggers, "droppedFrames": m.dropped}
}

// MotionEnabled is polled by the Android worker; no additional camera session is created.
func MotionEnabled() bool {
	r := activeRunner()
	if r == nil {
		return false
	}
	r.motion.mu.Lock()
	defer r.motion.mu.Unlock()
	return (r.motion.enabled || time.Since(r.motion.liveRequested) <= 4*time.Second) && r.recorderEnabled.Load()
}

// NextMotionPacket returns a bounded Annex-B packet or nil after a short wait.
func NextMotionPacket() []byte {
	r := activeRunner()
	if r == nil {
		return nil
	}
	select {
	case p := <-r.motion.queue:
		return p
	case <-time.After(250 * time.Millisecond):
		return nil
	}
}

// MotionGenerationCurrent lets the worker abandon a stale packet when a
// reconnect, queue overflow or setting change has reset the decoder stream.
func MotionGenerationCurrent(generation int64) bool {
	r := activeRunner()
	if r == nil || !r.recorderEnabled.Load() {
		return false
	}
	r.motion.mu.Lock()
	defer r.motion.mu.Unlock()
	return uint64(generation) == r.motion.generation && (r.motion.enabled || time.Since(r.motion.liveRequested) <= 4*time.Second)
}

func (m *motionState) needsKeyframe(epoch uint64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.epoch == epoch && m.waitKey && (m.enabled || time.Since(m.liveRequested) <= 4*time.Second)
}

// MotionStatus accepts only short status messages generated by the native worker.
func MotionStatus(s string) {
	r := activeRunner()
	if r == nil {
		return
	}
	if len(s) > 180 {
		s = s[:180]
	}
	r.motion.mu.Lock()
	r.motion.status = s
	r.motion.mu.Unlock()
}

// SubmitMotionFrame accepts a 96x54 greyscale sample generated by Android.
func SubmitMotionFrame(frame []byte, generation int64) {
	r := activeRunner()
	if r == nil || !r.recorderEnabled.Load() {
		return
	}
	m := &r.motion
	m.mu.Lock()
	if !m.enabled || uint64(generation) != m.generation {
		m.mu.Unlock()
		return
	}
	if m.detector == nil {
		m.detector, _ = localmotion.New(96, 54, m.sensitivity*20)
	}
	_ = m.detector.SetSensitivity(m.sensitivity * 20)
	_ = m.detector.SetRegion(m.area)
	hit, _, err := m.detector.Observe(frame)
	if err == nil {
		m.frames++
		m.lastFrame = time.Now()
		m.status = "Phone motion detection active"
	}
	m.mu.Unlock()
	if hit && r.beginCapture("phone motion", time.Now()) {
		m.mu.Lock()
		m.triggers++
		m.mu.Unlock()
	}
}

// ResetMotionDecoder drops queued frames and waits for a fresh H.264 keyframe.
func ResetMotionDecoder() {
	r := activeRunner()
	if r == nil {
		return
	}
	m := &r.motion
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	m.waitKey = true
	m.detector = nil
	m.drainLocked()
}
