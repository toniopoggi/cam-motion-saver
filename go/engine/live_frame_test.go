package engine

import (
	"bytes"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPreviewContentSecurityPolicy(t *testing.T) {
	r := testRunner(t)
	w := httptest.NewRecorder()
	r.home(w, httptest.NewRequest("GET", "http://127.0.0.1:8767/admin", nil))
	csp := w.Header().Get("Content-Security-Policy")
	if !strings.Contains(csp, "img-src 'self' data: blob:") {
		t.Fatalf("camera preview blob image blocked by CSP: %s", csp)
	}
}

func TestMotionAreaSettingsAndPrivateLiveFrame(t *testing.T) {
	r := testRunner(t)
	r.initMotion()
	stateMu.Lock()
	old := state
	state = r
	stateMu.Unlock()
	t.Cleanup(func() { stateMu.Lock(); state = old; stateMu.Unlock() })
	post := func(body string) int {
		w := httptest.NewRecorder()
		r.recordingSettings(w, httptest.NewRequest("POST", "/api/recording/settings", strings.NewReader(body)))
		return w.Code
	}
	if got := motionArea(r.loadConfig()); got.Width != 1 || got.Height != 1 {
		t.Fatal("old installation should use full frame")
	}
	if post(`{"MotionArea":{"X":0.2,"Y":0.1,"Width":0.4,"Height":0.6}}`) != 200 {
		t.Fatal("area save failed")
	}
	if post(`{"MotionSensitivity":5}`) != 200 {
		t.Fatal("sensitivity save failed")
	}
	c := r.loadConfig()
	if c.MotionArea == nil || c.MotionArea.X != 0.2 || c.MotionSensitivity != 5 {
		t.Fatal("area not persisted independently")
	}
	for _, bad := range []string{`{"MotionArea":{"X":0.8,"Y":0,"Width":0.5,"Height":1}}`, `{"MotionArea":{"X":0,"Y":0,"Width":0,"Height":1}}`} {
		if post(bad) != 400 {
			t.Fatal("accepted invalid area", bad)
		}
	}
	cookie := pairedCookie(t, r, "/view", r.viewerToken)
	w := httptest.NewRecorder()
	r.viewerGuard(http.HandlerFunc(r.liveFrame)).ServeHTTP(w, shareRequest("GET", "/api/live/frame", cookie))
	if w.Code != 403 {
		t.Fatal("viewer accessed live frame")
	}
	w = httptest.NewRecorder()
	r.liveFrame(w, httptest.NewRequest("GET", "/api/live/frame", nil))
	if w.Code != 204 || !LiveFrameRequested() {
		t.Fatal("preview request was not recorded")
	}
	LivePreviewFailed()
	w = httptest.NewRecorder()
	r.liveFrame(w, httptest.NewRequest("GET", "/api/live/frame", nil))
	if w.Code != 204 || !strings.Contains(w.Header().Get("X-Preview-Status"), "could not create") {
		t.Fatal("preview decoder error was hidden")
	}
	var jpegData bytes.Buffer
	jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 2, 2)), nil)
	SubmitLiveFrame(jpegData.Bytes(), int64(r.motion.generation))
	w = httptest.NewRecorder()
	r.liveFrame(w, httptest.NewRequest("GET", "/api/live/frame", nil))
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/jpeg" || !bytes.Equal(w.Body.Bytes(), jpegData.Bytes()) {
		t.Fatal("live JPEG missing")
	}
	if w.Header().Get("X-Preview-Status") != "" {
		t.Fatal("preview error did not clear after recovery")
	}
	r.motion.mu.Lock()
	r.motion.liveFrameAt = time.Now().Add(-10 * time.Second)
	r.motion.mu.Unlock()
	w = httptest.NewRecorder()
	r.liveFrame(w, httptest.NewRequest("GET", "/api/live/frame", nil))
	if w.Code != 204 {
		t.Fatal("stale frame served")
	}
}
