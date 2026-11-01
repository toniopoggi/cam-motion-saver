package engine

import (
	"bytes"
	"camfreenest/capture"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/gif"
	"image/jpeg"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDurationSettingKeepsLiveStreamAndPersists(t *testing.T) {
	r := testRunner(t)
	ctl, e := capture.New(filepath.Join(r.dir, "clips"), 5*time.Second, 30*time.Second, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	r.captureController = ctl
	original := Configuration{ClientID: "id", ClientSecret: "secret", RefreshToken: "token", DeviceName: "enterprises/test/devices/camera", ClipSeconds: 30}
	if e = r.saveConfig(original); e != nil {
		t.Fatal(e)
	}
	for _, seconds := range []int{120, 600} {
		b, _ := json.Marshal(map[string]int{"ClipSeconds": seconds})
		req := httptest.NewRequest("POST", "http://127.0.0.1:8767/api/recording/settings", bytes.NewReader(b))
		w := httptest.NewRecorder()
		r.recordingSettings(w, req)
		if w.Code != 200 || r.loadConfig().ClipSeconds != seconds || ctl.Status().ClipSeconds != float64(seconds) {
			t.Fatal("duration not applied and persisted")
		}
		if len(r.configChanged) != 0 || r.captureController != ctl {
			t.Fatal("duration change reconnected stream")
		}
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1:8767/api/recording/settings", bytes.NewBufferString(`{"ClipSeconds":601}`))
	w := httptest.NewRecorder()
	r.recordingSettings(w, req)
	if w.Code != 400 || r.loadConfig().ClipSeconds != 600 {
		t.Fatal("invalid setting silently accepted")
	}
	// The original full settings form must not reconnect on a duration-only save.
	req = httptest.NewRequest("POST", "http://127.0.0.1:8767/api/config", bytes.NewBufferString(`{"ClipSeconds":180}`))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.config(w, req)
	if w.Code != 200 || len(r.configChanged) != 0 || r.loadConfig().ClientSecret != "secret" {
		t.Fatal("duration form interrupted the camera or lost credentials")
	}
}
func TestFailedNewPreviewsCannotStarveOlderClips(t *testing.T) {
	r := testRunner(t)
	now := time.Now()
	for i := 0; i < 7; i++ {
		name := string(rune('a'+i)) + ".mp4"
		p := filepath.Join(r.dir, "clips", name)
		os.WriteFile(p, []byte("fixture"), 0600)
		os.Chtimes(p, now.Add(time.Duration(i)*time.Second), now.Add(time.Duration(i)*time.Second))
		if i > 0 {
			r.storePreviewState(name, previewState{Status: "retrying", Attempts: 1, RetryAt: now.Add(time.Hour)})
		}
	}
	jobs := r.claimPreviewJobs(now)
	if len(jobs) != 1 || jobs[0] != "a.mp4" {
		t.Fatalf("old clip starved: %v", jobs)
	}
	r.storePreviewState("a.mp4", previewState{Status: "processing", Attempts: 3, Updated: now.Add(-2 * time.Minute)})
	if jobs = r.claimPreviewJobs(now); len(jobs) != 0 || r.readPreviewState("a.mp4").Status != "failed" {
		t.Fatal("stalled worker remains preparing forever")
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1:8767/api/preview/retry", bytes.NewBufferString(`{"Name":"a.mp4"}`))
	w := httptest.NewRecorder()
	r.retryPreview(w, req)
	if w.Code != 200 || r.readPreviewState("a.mp4").Attempts != 0 || len(r.claimPreviewJobs(now)) != 1 {
		t.Fatal("manual retry failed")
	}
}
func TestSingleDecodedFrameStillGetsPreview(t *testing.T) {
	r := testRunner(t)
	name := "tiny.mp4"
	p := filepath.Join(r.dir, "clips", name)
	os.WriteFile(p, []byte("fixture"), 0600)
	var jpegBytes, payload bytes.Buffer
	jpeg.Encode(&jpegBytes, image.NewRGBA(image.Rect(0, 0, 32, 18)), nil)
	binary.Write(&payload, binary.BigEndian, uint32(jpegBytes.Len()))
	payload.Write(jpegBytes.Bytes())
	if e := writePreview(r.dir, name, payload.Bytes()); e != nil {
		t.Fatal(e)
	}
	f, e := os.Open(p + ".gif")
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	g, e := gif.DecodeAll(f)
	if e != nil || len(g.Image) != 1 || g.Delay[0] != 300 {
		t.Fatal("single-frame fallback missing or has wrong duration")
	}
	if e = writePreview(r.dir, name, nil); e == nil {
		t.Fatal("empty preview reported success")
	}
}
