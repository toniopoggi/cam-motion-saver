package engine

import (
	"net/http"
	"strings"
	"time"
)

// liveFrame supplies a recent preview to authenticated admins. View-only
// sessions are excluded by viewerGuard because the live camera is sensitive.
func (r *Runner) liveFrame(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" && req.Method != "HEAD" {
		http.Error(w, "GET or HEAD only", 405)
		return
	}
	m := &r.motion
	m.mu.Lock()
	now := time.Now()
	if !m.enabled && now.Sub(m.liveRequested) > 4*time.Second {
		m.generation++
		m.waitKey = true
		m.detector = nil
		m.drainLocked()
	}
	m.liveRequested = now
	var frame []byte
	previewStatus := ""
	if !r.recorderEnabled.Load() {
		previewStatus = "Start the recorder to see the camera preview."
	} else if m.previewError {
		previewStatus = "The phone could not create a camera preview. Check detector status."
	} else if strings.HasPrefix(m.status, "Phone motion unavailable:") {
		previewStatus = "The phone decoder is unavailable. Check detector status."
	}
	if r.recorderEnabled.Load() && now.Sub(m.liveFrameAt) <= 3*time.Second {
		frame = append([]byte(nil), m.liveFrame...)
	}
	m.mu.Unlock()
	w.Header().Set("Cache-Control", "no-store")
	if previewStatus != "" {
		w.Header().Set("X-Preview-Status", previewStatus)
	}
	if len(frame) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	if req.Method == "GET" {
		_, _ = w.Write(frame)
	}
}

// LiveFrameRequested enables JPEG creation only while a local/admin page is watching.
func LiveFrameRequested() bool {
	r := activeRunner()
	if r == nil {
		return false
	}
	r.motion.mu.Lock()
	defer r.motion.mu.Unlock()
	return time.Since(r.motion.liveRequested) <= 4*time.Second && r.recorderEnabled.Load()
}

// SubmitLiveFrame accepts a bounded JPEG from the native decoder.
func SubmitLiveFrame(frame []byte, generation int64) {
	r := activeRunner()
	if r == nil || len(frame) < 4 || len(frame) > 512*1024 || frame[0] != 0xff || frame[1] != 0xd8 {
		return
	}
	m := &r.motion
	m.mu.Lock()
	defer m.mu.Unlock()
	if uint64(generation) != m.generation || time.Since(m.liveRequested) > 4*time.Second {
		return
	}
	m.liveFrame = append(m.liveFrame[:0], frame...)
	m.liveFrameAt = time.Now()
	m.previewError = false
}

// LivePreviewFailed records an independent preview error without disrupting detection.
func LivePreviewFailed() {
	r := activeRunner()
	if r == nil {
		return
	}
	r.motion.mu.Lock()
	r.motion.previewError = true
	r.motion.mu.Unlock()
}
