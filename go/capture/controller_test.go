package capture

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"camfreenest/mp4lite"
	"camfreenest/preroll"
)

func frameStream(t *testing.T) ([][]byte, []byte, []byte) {
	t.Helper()
	data, err := os.ReadFile("../mp4lite/testdata/sample.h264")
	if err != nil {
		t.Fatal(err)
	}
	nals := mp4lite.AnnexBUnits(data)
	var frames [][]byte
	var frame, sps, pps []byte
	for _, n := range nals {
		if n[0]&31 == 9 && len(frame) > 0 {
			frames = append(frames, frame)
			frame = nil
		}
		frame = append(frame, 0, 0, 0, 1)
		frame = append(frame, n...)
		if n[0]&31 == 7 {
			sps = append([]byte(nil), n...)
		}
		if n[0]&31 == 8 {
			pps = append([]byte(nil), n...)
		}
	}
	if len(frame) > 0 {
		frames = append(frames, frame)
	}
	if len(frames) != 60 {
		t.Fatalf("frames %d", len(frames))
	}
	return frames, sps, pps
}

func TestMotionSavesFiveSecondsBeforeTrigger(t *testing.T) {
	frames, sps, pps := frameStream(t)
	dir := t.TempDir()
	saved := make([]string, 0)
	controller, err := New(dir, 5*time.Second, 5*time.Second, func(name string, e error) {
		if e != nil {
			t.Errorf("saving %s: %v", name, e)
		} else {
			saved = append(saved, name)
		}
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	controller.BeginEpoch(5)
	controller.SetCodecFromSDP(sps, pps)
	at := time.Date(2026, 10, 23, 16, 20, 0, 0, time.UTC)
	for i := 0; i < 240; i++ { // Four 3-second copies => 12s at 20fps.
		frame := frames[i%60]
		when := at.Add(time.Duration(i) * 50 * time.Millisecond)
		if err := controller.Push(preroll.Unit{At: when, PTS: time.Duration(i) * 50 * time.Millisecond, Data: frame, Epoch: 5}); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if i == 169 { // last sample at 8.45s; physical motion occurred 0.5s earlier
			if !controller.Trigger(when.Add(-500 * time.Millisecond)) {
				t.Fatal("motion rejected")
			}
		}
	}
	controller.Offline()
	if len(saved) != 1 {
		t.Fatalf("saved %d clips, expected 1", len(saved))
	}
	path := filepath.Join(dir, saved[0])
	if _, e := os.Stat(path); e != nil {
		t.Fatal(e)
	}
	if probe, e := exec.LookPath("ffprobe"); e == nil {
		out, err := exec.Command(probe, "-v", "error", "-show_entries", "format=duration", "-of", "default=noprint_wrappers=1:nokey=1", path).CombinedOutput()
		if err != nil {
			t.Fatal(err, string(out))
		}
		var dur float64
		if _, err = fmt.Sscanf(strings.TrimSpace(string(out)), "%f", &dur); err != nil {
			t.Fatal(err)
		}
		if dur < 6.5 {
			t.Errorf("expected 5s pre-roll plus post-motion footage, got %.2fs", dur)
		}
	}
	if decoder, e := exec.LookPath("ffmpeg"); e == nil {
		out, err := exec.Command(decoder, "-hide_banner", "-v", "error", "-i", path, "-f", "null", "-").CombinedOutput()
		if err != nil {
			t.Fatalf("produced MP4 could not decode: %v %s", err, out)
		}
	}
}
func TestNoWebRtcStreamReturnsFalse(t *testing.T) {
	c, e := New(t.TempDir(), 5*time.Second, 30*time.Second, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	if c.Trigger(time.Now()) {
		t.Fatal("offline trigger falsely accepted")
	}
	c.BeginEpoch(1)
	if !c.Trigger(time.Now()) {
		t.Fatal("online pending trigger falsely rejected")
	}
	if !c.Status().Pending {
		t.Fatal("pending motion not retained")
	}
}

func TestManualRecordingContinuesUntilStopAndKeepsPreRoll(t *testing.T) {
	frames, sps, pps := frameStream(t)
	dir := t.TempDir()
	c, err := New(dir, 5*time.Second, 5*time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	c.BeginEpoch(2)
	c.SetCodecFromSDP(sps, pps)
	start := time.Now()
	for i := 0; i < 400; i++ {
		pts := time.Duration(i) * 50 * time.Millisecond
		if err := c.Push(preroll.Unit{At: start.Add(pts), PTS: pts, Data: frames[i%60], Epoch: 2}); err != nil {
			t.Fatal(err)
		}
		if i == 140 && !c.StartManual() {
			t.Fatal("manual start rejected")
		}
	}
	status := c.Status()
	if !status.Manual || !status.Writing {
		t.Fatal("manual recording ended at timed clip limit")
	}
	files := recordingFiles(t, dir)
	if len(files) != 1 || !strings.HasSuffix(files[0].Name(), ".part") {
		t.Fatal("manual clip finalized too early")
	}
	c.StopRecording()
	status = c.Status()
	if status.Manual || status.Writing || !status.Online || status.CachedFrames == 0 {
		t.Fatalf("stop lost live buffer: %+v", status)
	}
	files = recordingFiles(t, dir)
	if len(files) != 1 || !strings.HasSuffix(files[0].Name(), ".mp4") {
		t.Fatal("stop did not finalize MP4")
	}
	if decoder, e := exec.LookPath("ffmpeg"); e == nil {
		out, e := exec.Command(decoder, "-v", "error", "-i", filepath.Join(dir, files[0].Name()), "-f", "null", "-").CombinedOutput()
		if e != nil || len(out) != 0 {
			t.Fatalf("stopped manual MP4 decode: %v %s", e, out)
		}
	}
	// Motion continues using the same stream and five-second history.
	if !c.Trigger(start.Add(20 * time.Second)) {
		t.Fatal("motion failed after manual stop")
	}
	c.StopRecording()
	files = recordingFiles(t, dir)
	if len(files) != 2 {
		t.Fatal("post-stop motion clip missing")
	}
}

func TestStopCancelsPendingManualRecording(t *testing.T) {
	frames, sps, pps := frameStream(t)
	c, e := New(t.TempDir(), 5*time.Second, 5*time.Second, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.BeginEpoch(3)
	if !c.StartManual() || !c.Status().Pending {
		t.Fatal("manual wait not registered")
	}
	c.StopRecording()
	c.SetCodecFromSDP(sps, pps)
	if e = c.Push(preroll.Unit{At: time.Now(), Data: frames[0], Epoch: 3}); e != nil {
		t.Fatal(e)
	}
	if c.Status().Writing || c.Status().Pending {
		t.Fatal("cancelled recording started later")
	}
}

func recordingFiles(t *testing.T, dir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := []os.DirEntry{}
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".mp4") || strings.HasSuffix(entry.Name(), ".part") {
			out = append(out, entry)
		}
	}
	return out
}
