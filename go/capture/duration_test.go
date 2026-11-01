package capture

import (
	"camfreenest/mp4lite"
	"camfreenest/preroll"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func feed(t *testing.T, c *Controller, frames [][]byte, i int, epoch uint64, wall time.Time) {
	t.Helper()
	pts := time.Duration(i) * 50 * time.Millisecond
	if e := c.Push(preroll.Unit{At: wall.Add(pts), PTS: pts, Data: frames[i%len(frames)], Epoch: epoch}); e != nil {
		t.Fatalf("frame %d: %v", i, e)
	}
}
func decodeAll(t *testing.T, dir string) []ClipMetadata {
	t.Helper()
	files := recordingFiles(t, dir)
	out := []ClipMetadata{}
	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".mp4") {
			continue
		}
		var m ClipMetadata
		b, e := os.ReadFile(filepath.Join(dir, f.Name()+".meta.json"))
		if e != nil || json.Unmarshal(b, &m) != nil {
			t.Fatal("missing recording reason")
		}
		out = append(out, m)
		if ff, e := exec.LookPath("ffmpeg"); e == nil {
			b, e := exec.Command(ff, "-v", "error", "-i", filepath.Join(dir, f.Name()), "-f", "null", "-").CombinedOutput()
			if e != nil || len(b) > 0 {
				t.Fatalf("decode: %v %s", e, b)
			}
		}
	}
	return out
}
func TestThirtySecondMotionSurvivesStreamReconnect(t *testing.T) {
	frames, sps, pps := frameStream(t)
	dir := t.TempDir()
	c, e := New(dir, 5*time.Second, 30*time.Second, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	c.BeginEpoch(1)
	c.SetCodecFromSDP(sps, pps)
	wall := time.Now()
	for i := 0; i < 200; i++ {
		feed(t, c, frames, i, 1, wall)
		if i == 140 {
			c.Trigger(wall.Add(7 * time.Second))
		}
	}
	c.Suspend()
	if !c.Status().Writing || c.Status().Online || c.Status().SavedClips != 0 {
		t.Fatal("disconnect finalized an unfinished recording")
	}
	c.BeginEpoch(2)
	c.SetCodecFromSDP(sps, pps)
	wall = wall.Add(time.Minute)
	for i := 0; i < 540; i++ {
		feed(t, c, frames, i, 2, wall)
	}
	if !c.Status().Writing {
		t.Fatal("recording ended before 30 seconds of post-motion media")
	}
	feed(t, c, frames, 540, 2, wall)
	if c.Status().Writing || c.Status().SavedClips != 1 {
		t.Fatalf("deadline status: %+v", c.Status())
	}
	meta := decodeAll(t, dir)
	if len(meta) != 1 || meta[0].DurationSeconds < 35 || meta[0].EndReason != "duration reached" {
		t.Fatalf("clip: %+v", meta)
	}
}
func TestNewMotionExtendsExistingClip(t *testing.T) {
	frames, sps, pps := frameStream(t)
	dir := t.TempDir()
	c, _ := New(dir, 5*time.Second, 5*time.Second, nil, nil)
	c.BeginEpoch(3)
	c.SetCodecFromSDP(sps, pps)
	wall := time.Now()
	for i := 0; i <= 260; i++ {
		feed(t, c, frames, i, 3, wall)
		if i == 80 || i == 160 {
			if !c.Trigger(wall.Add(time.Duration(i) * 50 * time.Millisecond)) {
				t.Fatal("motion rejected")
			}
		}
		if i == 240 && !c.Status().Writing {
			t.Fatal("second event did not extend clip")
		}
	}
	if c.Status().Writing || c.Status().SavedClips != 1 {
		t.Fatal("wrong extended deadline")
	}
	if m := decodeAll(t, dir); len(m) != 1 || m[0].DurationSeconds < 13 {
		t.Fatalf("duration: %+v", m)
	}
}

func TestPaddedParameterSetDoesNotSplitRecording(t *testing.T) {
	frames, sps, pps := frameStream(t)
	dir := t.TempDir()
	c, _ := New(dir, 5*time.Second, 30*time.Second, nil, nil)
	c.BeginEpoch(10)
	c.SetCodecFromSDP(sps, pps)
	wall := time.Now()
	for i := 0; i < 700; i++ {
		if i == 240 || i == 320 || i == 400 || i == 480 {
			padded := append(append([]byte{0, 0, 0, 1}, sps...), 0, 0)
			pts := time.Duration(i) * 50 * time.Millisecond
			if e := c.Push(preroll.Unit{At: wall.Add(pts), PTS: pts, Data: padded, Epoch: 10}); e != nil {
				t.Fatal(e)
			}
		}
		feed(t, c, frames, i, 10, wall)
		if i == 140 && !c.Trigger(wall.Add(7*time.Second)) {
			t.Fatal("trigger failed")
		}
	}
	c.StopRecording()
	meta := decodeAll(t, dir)
	if len(meta) != 1 || meta[0].DurationSeconds < 25 {
		t.Fatalf("padding split the recording: %+v", meta)
	}
}
func TestCodecChangeContinuesOutstandingMotionBudget(t *testing.T) {
	frames, sps, pps := frameStream(t)
	dir := t.TempDir()
	c, _ := New(dir, 5*time.Second, 10*time.Second, nil, nil)
	c.BeginEpoch(4)
	c.SetCodecFromSDP(sps, pps)
	wall := time.Now()
	for i := 0; i < 200; i++ {
		feed(t, c, frames, i, 4, wall)
		if i == 140 {
			c.Trigger(wall.Add(7 * time.Second))
		}
	}
	// Raise only the SPS level indication; the same encoded pictures still decode.
	changed := append([]byte(nil), sps...)
	changed[3]++
	c.SetCodecFromSDP(changed, pps)
	if !c.Status().Pending || c.Status().SavedClips != 1 {
		t.Fatal("codec switch lost pending recording")
	}
	stripped := make([][]byte, len(frames))
	for i, f := range frames {
		for _, n := range mp4lite.AnnexBUnits(f) {
			if n[0]&31 == 7 || n[0]&31 == 8 {
				continue
			}
			stripped[i] = append(stripped[i], 0, 0, 0, 1)
			stripped[i] = append(stripped[i], n...)
		}
	}
	for i := 0; i <= 140; i++ {
		feed(t, c, stripped, i, 4, wall.Add(time.Minute))
	}
	if c.Status().SavedClips != 2 || c.Status().Writing {
		t.Fatalf("continuation status %+v", c.Status())
	}
	meta := decodeAll(t, dir)
	var total float64
	continuation := false
	for _, m := range meta {
		total += m.DurationSeconds
		continuation = continuation || m.Continuation
	}
	if total < 15 || !continuation {
		t.Fatalf("lost motion budget: %.2f %+v", total, meta)
	}
}
