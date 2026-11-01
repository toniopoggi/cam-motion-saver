package engine

import (
	"camfreenest/localmotion"
	"encoding/json"
	"net/http"
	"os"
	"time"
)

func (r *Runner) saveConfig(c Configuration) error {
	b, e := json.MarshalIndent(c, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(r.cfgPath()+".tmp", b, 0600); e != nil {
		return e
	}
	return os.Rename(r.cfgPath()+".tmp", r.cfgPath())
}
func motionArea(c Configuration) localmotion.Region {
	if c.MotionArea != nil && c.MotionArea.Valid() {
		return *c.MotionArea
	}
	return localmotion.FullRegion()
}
func (r *Runner) recordingSettings(w http.ResponseWriter, req *http.Request) {
	r.configMu.Lock()
	defer r.configMu.Unlock()
	if req.Method == "GET" {
		c := r.loadConfig()
		r.writeJSON(w, 200, map[string]any{"ClipSeconds": c.ClipSeconds, "RetentionDays": c.RetentionDays, "LocalMotionEnabled": !c.LocalMotionDisabled, "GoogleEventsEnabled": !c.GoogleEventsDisabled, "MotionSensitivity": c.MotionSensitivity, "MotionArea": motionArea(c)})
		return
	}
	if req.Method != "POST" {
		http.Error(w, "POST or GET only", 405)
		return
	}
	var update struct {
		ClipSeconds         *int
		RetentionDays       *int64
		LocalMotionEnabled  *bool
		GoogleEventsEnabled *bool
		MotionSensitivity   *int
		MotionArea          *localmotion.Region
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, req.Body, 4096)).Decode(&update); e != nil {
		r.writeJSON(w, 400, map[string]string{"error": "Invalid recording settings"})
		return
	}
	if update.ClipSeconds != nil && (*update.ClipSeconds < 5 || *update.ClipSeconds > 600) {
		r.writeJSON(w, 400, map[string]string{"error": "Duration must be 5-600 seconds"})
		return
	}
	if update.RetentionDays != nil && (*update.RetentionDays < 1 || *update.RetentionDays > 9007199254740991) {
		r.writeJSON(w, 400, map[string]string{"error": "Retention must be a whole number of days, at least 1"})
		return
	}
	if update.MotionSensitivity != nil && (*update.MotionSensitivity < 1 || *update.MotionSensitivity > 5) {
		r.writeJSON(w, 400, map[string]string{"error": "Sensitivity must be 1–5"})
		return
	}
	if update.MotionArea != nil && !update.MotionArea.Valid() {
		r.writeJSON(w, 400, map[string]string{"error": "Invalid motion area"})
		return
	}
	if update.ClipSeconds == nil && update.RetentionDays == nil && update.LocalMotionEnabled == nil && update.GoogleEventsEnabled == nil && update.MotionSensitivity == nil && update.MotionArea == nil {
		r.writeJSON(w, 400, map[string]string{"error": "No recording settings supplied"})
		return
	}
	c := r.loadConfig()
	if update.ClipSeconds != nil {
		c.ClipSeconds = *update.ClipSeconds
	}
	if update.RetentionDays != nil {
		c.RetentionDays = *update.RetentionDays
	}
	if update.LocalMotionEnabled != nil {
		c.LocalMotionDisabled = !*update.LocalMotionEnabled
	}
	if update.GoogleEventsEnabled != nil {
		c.GoogleEventsDisabled = !*update.GoogleEventsEnabled
	}
	if update.MotionSensitivity != nil {
		c.MotionSensitivity = *update.MotionSensitivity
	}
	if update.MotionArea != nil {
		c.MotionArea = update.MotionArea
	}
	if e := r.saveConfig(c); e != nil {
		r.writeJSON(w, 500, map[string]string{"error": e.Error()})
		return
	}
	r.motion.configure(c)
	if c.GoogleEventsDisabled {
		r.cancelEventPull()
	}
	r.captureMu.RLock()
	ctl := r.captureController
	r.captureMu.RUnlock()
	if ctl != nil && update.ClipSeconds != nil {
		_ = ctl.SetDuration(time.Duration(c.ClipSeconds) * time.Second)
	}
	r.log("info", "Recording settings saved; camera stream kept connected")
	r.writeJSON(w, 200, map[string]any{"ClipSeconds": c.ClipSeconds, "RetentionDays": c.RetentionDays, "LocalMotionEnabled": !c.LocalMotionDisabled, "GoogleEventsEnabled": !c.GoogleEventsDisabled, "MotionSensitivity": c.MotionSensitivity, "MotionArea": motionArea(c)})
}
