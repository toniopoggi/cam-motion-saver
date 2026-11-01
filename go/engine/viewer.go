package engine

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func (r *Runner) preview(w http.ResponseWriter, req *http.Request) {
	if req.Method != "GET" && req.Method != "HEAD" {
		http.Error(w, "GET or HEAD only", 405)
		return
	}
	name := strings.TrimPrefix(req.URL.Path, "/preview/")
	if name != filepath.Base(name) || !strings.HasSuffix(name, ".mp4.gif") || strings.ContainsAny(name, "/\\") {
		http.Error(w, "Invalid preview", 400)
		return
	}
	w.Header().Set("Content-Type", "image/gif")
	w.Header().Set("Cache-Control", "private, max-age=3600")
	http.ServeFile(w, req, filepath.Join(r.dir, "clips", name))
}

func (r *Runner) startRecording(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	r.recordingMu.Lock()
	defer r.recordingMu.Unlock()
	r.captureMu.RLock()
	ctl := r.captureController
	r.captureMu.RUnlock()
	if !r.recorderEnabled.Load() || ctl == nil || !ctl.StartManual() {
		r.writeJSON(w, 409, map[string]string{"error": "Start the recorder and wait for the WebRTC stream to connect"})
		return
	}
	r.manualWanted.Store(true)
	r.log("info", "Manual recording started; press Stop recording to save")
	r.writeJSON(w, 202, map[string]string{"status": "recording"})
}
func (r *Runner) stopRecording(w http.ResponseWriter, req *http.Request) {
	if req.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	r.recordingMu.Lock()
	defer r.recordingMu.Unlock()
	r.captureMu.RLock()
	ctl := r.captureController
	r.captureMu.RUnlock()
	r.manualWanted.Store(false)
	if ctl != nil {
		ctl.StopRecording()
	}
	r.log("info", "Recording stopped and MP4 finalized")
	r.writeJSON(w, 200, map[string]string{"status": "stopped"})
}
func (r *Runner) startRecorder(w http.ResponseWriter, req *http.Request) { r.setRecorder(w, req, true) }
func (r *Runner) stopRecorder(w http.ResponseWriter, req *http.Request)  { r.setRecorder(w, req, false) }
func (r *Runner) setRecorder(w http.ResponseWriter, req *http.Request, enabled bool) {
	if req.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	r.recordingMu.Lock()
	defer r.recordingMu.Unlock()
	if enabled && !validCamera(r.loadConfig()) {
		r.writeJSON(w, 409, map[string]string{"error": "Configure Google camera access first"})
		return
	}
	paused := "true"
	if enabled {
		paused = "false"
	}
	if err := os.WriteFile(filepath.Join(r.dir, "recorder-paused"), []byte(paused), 0600); err != nil {
		r.writeJSON(w, 500, map[string]string{"error": err.Error()})
		return
	}
	r.recorderEnabled.Store(enabled)
	if !enabled {
		r.cancelEventPull()
	}
	if !enabled {
		r.manualWanted.Store(false)
		r.sessionMu.Lock()
		if r.sessionCancel != nil {
			r.sessionCancel()
		}
		r.sessionMu.Unlock()
		r.captureMu.RLock()
		ctl := r.captureController
		r.captureMu.RUnlock()
		if ctl != nil {
			ctl.Offline()
		}
	}
	text := "Recorder paused; viewer stays available"
	if enabled {
		text = "Recorder enabled; connecting persistent stream"
	}
	r.log("info", text)
	r.writeJSON(w, 200, map[string]any{"enabled": enabled, "message": text, "time": time.Now().Format(time.RFC3339)})
}
