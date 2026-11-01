package engine

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testRunner(t *testing.T) *Runner {
	t.Helper()
	r := &Runner{dir: t.TempDir(), configChanged: make(chan struct{}, 1)}
	if err := os.MkdirAll(filepath.Join(r.dir, "clips"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.initViewerToken(); err != nil {
		t.Fatal(err)
	}
	r.lastIgnored.Store("")
	r.recorderEnabled.Store(true)
	return r
}
func remoteRequest(method, path string) *http.Request {
	req := httptest.NewRequest(method, "http://192.168.1.5:8767"+path, nil)
	req.RemoteAddr = "192.168.1.20:40000"
	return req
}
func TestLANPairingAndPrivateSettings(t *testing.T) {
	r := testRunner(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", r.home)
	mux.HandleFunc("/api/config", r.config)
	mux.HandleFunc("/api/access", r.viewerAccess)
	handler := r.viewerGuard(mux)
	first := httptest.NewRecorder()
	handler.ServeHTTP(first, remoteRequest("GET", "/"))
	if first.Code != 401 {
		t.Fatalf("unpaired LAN request: %d", first.Code)
	}
	pair := httptest.NewRecorder()
	handler.ServeHTTP(pair, remoteRequest("GET", "/?key="+r.viewerToken))
	if pair.Code != 200 || pair.Header().Get("Location") != "" || !strings.Contains(pair.Body.String(), "View only") {
		t.Fatalf("pairing: %d", pair.Code)
	}
	cookies := pair.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("missing private session cookie")
	}
	req := remoteRequest("GET", "/")
	req.AddCookie(cookies[0])
	view := httptest.NewRecorder()
	handler.ServeHTTP(view, req)
	if view.Code != 200 || !strings.Contains(view.Body.String(), "View only") || !strings.Contains(view.Body.String(), "<video") {
		t.Fatal("paired viewer missing controls/player")
	}
	req = remoteRequest("GET", "/api/config")
	req.AddCookie(cookies[0])
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("remote viewer can access Google settings")
	}
	req = remoteRequest("GET", "/api/access")
	req.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), r.viewerToken) {
		t.Fatal("pairing secret exposed remotely")
	}
	// A malicious page resolving to localhost must not gain local privileges.
	req = httptest.NewRequest("GET", "http://attacker.invalid:8767/api/config", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 401 {
		t.Fatal("host rebinding bypass")
	}
	req = httptest.NewRequest("POST", "http://127.0.0.1:8767/api/config", nil)
	req.RemoteAddr = "127.0.0.1:40000"
	req.Header.Set("Origin", "http://attacker.invalid")
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal("cross-origin control accepted")
	}
}
func TestRemoteControlsRequirePairingAndDenyGoogleSettings(t *testing.T) {
	r := testRunner(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/", r.home)
	mux.HandleFunc("/api/recorder/stop", r.stopRecorder)
	mux.HandleFunc("/api/config", r.config)
	handler := r.viewerGuard(mux)
	// No key and no cookie must never grant remote control.
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, remoteRequest("POST", "/api/recorder/stop"))
	if w.Code != http.StatusUnauthorized || !r.recorderEnabled.Load() {
		t.Fatal("unpaired browser can stop the recorder")
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("authentication error can be cached")
	}
	if !strings.Contains(w.Body.String(), "You reached the recording phone") {
		t.Fatal("authentication failure presented as a network failure")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, remoteRequest("GET", "/?key=incorrect"))
	if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
		t.Fatal("invalid key created a session")
	}
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, remoteRequest("GET", "/?key="+r.adminToken))
	if w.Code != http.StatusOK || w.Header().Get("Location") != "" {
		t.Fatal("pairing still depends on a redirect")
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != 30*24*60*60 {
		t.Fatal("missing remembered browser session")
	}
	// A remembered session works at the plain URL and permits recording control.
	req := remoteRequest("POST", "/api/recorder/stop")
	req.AddCookie(cookies[0])
	w = httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK || r.recorderEnabled.Load() {
		t.Fatal("paired browser could not control recording")
	}
	for _, method := range []string{"GET", "POST"} {
		req := remoteRequest(method, "/api/config")
		req.AddCookie(cookies[0])
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Fatal("paired browser can change Google configuration")
		}
	}
}

func TestInlineClipRangesAndDownloads(t *testing.T) {
	r := testRunner(t)
	data := []byte("0123456789abcdefghijklmnop")
	if err := os.WriteFile(filepath.Join(r.dir, "clips", "example.mp4"), data, 0600); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/clip/", r.clip)
	server := httptest.NewServer(r.viewerGuard(mux))
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/clip/example.mp4", nil)
	req.Header.Set("Range", "bytes=3-8")
	res, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != 206 || string(b) != "345678" || res.Header.Get("Content-Type") != "video/mp4" || !strings.HasPrefix(res.Header.Get("Content-Disposition"), "inline") {
		t.Fatalf("range playback: %d %q %v", res.StatusCode, b, res.Header)
	}
	res, err = server.Client().Get(server.URL + "/clip/example.mp4?download=1")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if !strings.HasPrefix(res.Header.Get("Content-Disposition"), "attachment") {
		t.Fatal("download disposition missing")
	}
}
func TestThreeSecondGIFPreviewAndRetention(t *testing.T) {
	r := testRunner(t)
	name := "clip.mp4"
	clip := filepath.Join(r.dir, "clips", name)
	if err := os.WriteFile(clip, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	var payload bytes.Buffer
	for i := 0; i < 6; i++ {
		frame := image.NewRGBA(image.Rect(0, 0, 64, 36))
		for y := 0; y < 36; y++ {
			for x := 0; x < 64; x++ {
				frame.Set(x, y, color.RGBA{uint8(i * 40), uint8(x * 3), uint8(y * 4), 255})
			}
		}
		var jpg bytes.Buffer
		if err := jpeg.Encode(&jpg, frame, nil); err != nil {
			t.Fatal(err)
		}
		binary.Write(&payload, binary.BigEndian, uint32(jpg.Len()))
		payload.Write(jpg.Bytes())
	}
	if err := writePreview(r.dir, name, payload.Bytes()); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(clip + ".gif")
	if err != nil {
		t.Fatal(err)
	}
	animation, err := gif.DecodeAll(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	duration := 0
	for _, delay := range animation.Delay {
		duration += delay
	}
	if len(animation.Image) != 6 || duration != 300 || animation.LoopCount != 0 {
		t.Fatalf("GIF frames/duration/loop: %d %d %d", len(animation.Image), duration, animation.LoopCount)
	}
	if bytes.Equal(animation.Image[0].Pix, animation.Image[5].Pix) {
		t.Fatal("preview does not animate")
	}
	items := r.clipList()
	if len(items) != 1 || items[0].Preview != "/preview/clip.mp4.gif" {
		t.Fatal("preview missing from gallery")
	}
	req := httptest.NewRequest("GET", "http://127.0.0.1:8767/preview/clip.mp4.gif", nil)
	w := httptest.NewRecorder()
	r.preview(w, req)
	if w.Code != 200 || w.Header().Get("Content-Type") != "image/gif" {
		t.Fatal("GIF endpoint failed")
	}
	if err := writePreview(r.dir, "../escape.mp4", payload.Bytes()); err == nil {
		t.Fatal("path traversal accepted")
	}
	if err := writePreview(r.dir, name, payload.Bytes()[:20]); err == nil {
		t.Fatal("truncated preview accepted")
	}
}
func TestStopRecorderKeepsViewerAvailable(t *testing.T) {
	r := testRunner(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "http://127.0.0.1:8767/api/recorder/stop", nil)
	r.stopRecorder(w, req)
	if w.Code != 200 || r.recorderEnabled.Load() {
		t.Fatal("recorder did not pause")
	}
	if r.beginCapture("pubsub", time.Now()) {
		t.Fatal("motion accepted while stopped")
	}
	w = httptest.NewRecorder()
	r.home(w, httptest.NewRequest("GET", "http://127.0.0.1:8767/", nil))
	if w.Code != 200 {
		t.Fatal("stopping recorder disabled viewer")
	}
	b, err := os.ReadFile(filepath.Join(r.dir, "recorder-paused"))
	if err != nil || string(b) != "true" {
		t.Fatal("pause not persisted")
	}
}
