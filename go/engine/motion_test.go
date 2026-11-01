package engine

import (
	"camfreenest/mp4lite"
	"encoding/binary"
	"encoding/json"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestPhoneMotionDefaultsSettingsAndTrigger(t *testing.T) {
	r := testRunner(t)
	r.initMotion()
	c := r.loadConfig()
	if c.LocalMotionDisabled || c.GoogleEventsDisabled || c.MotionSensitivity != 3 {
		t.Fatalf("defaults %+v", c)
	}
	stateMu.Lock()
	old := state
	state = r
	stateMu.Unlock()
	t.Cleanup(func() { stateMu.Lock(); state = old; stateMu.Unlock() })
	ctl := readyController(t, r)
	gray := make([]byte, 96*54)
	for i := range gray {
		gray[i] = 100
	}
	SubmitMotionFrame(gray, int64(r.motion.generation))
	for y := 8; y < 20; y++ {
		for x := 8; x < 20; x++ {
			gray[y*96+x] = 0
		}
	}
	SubmitMotionFrame(gray, int64(r.motion.generation))
	if !ctl.Status().Pending || r.motion.snapshot()["triggers"].(uint64) != 1 {
		t.Fatal("phone motion did not trigger recording")
	}
	post := func(b string) int {
		w := httptest.NewRecorder()
		r.recordingSettings(w, httptest.NewRequest("POST", "/api/recording/settings", strings.NewReader(b)))
		return w.Code
	}
	if post(`{"LocalMotionEnabled":false,"GoogleEventsEnabled":false,"MotionSensitivity":5}`) != 200 {
		t.Fatal("save failed")
	}
	c = r.loadConfig()
	if !c.LocalMotionDisabled || !c.GoogleEventsDisabled || c.MotionSensitivity != 5 || c.RetentionDays != 1 {
		t.Fatalf("settings %+v", c)
	}
	if post(`{"MotionSensitivity":0}`) != 400 || post(`{"MotionSensitivity":6}`) != 400 {
		t.Fatal("bad sensitivity accepted")
	}
	w := httptest.NewRecorder()
	r.recordingSettings(w, httptest.NewRequest("GET", "/api/recording/settings", nil))
	var got map[string]any
	if json.Unmarshal(w.Body.Bytes(), &got) != nil || got["LocalMotionEnabled"] != false || got["MotionSensitivity"] != float64(5) {
		t.Fatal(w.Body.String())
	}
}

func TestMotionQueueBoundedAndRecoversAtKeyframe(t *testing.T) {
	data, e := os.ReadFile("../mp4lite/testdata/sample.h264")
	if e != nil {
		t.Fatal(e)
	}
	sps, pps, key, _ := mp4lite.NALInfo(data)
	if !key || len(sps) == 0 || len(pps) == 0 {
		t.Fatal("fixture lacks codec/keyframe")
	}
	r := testRunner(t)
	r.initMotion()
	r.motion.newEpoch(4, sps, pps)
	if !r.motion.needsKeyframe(4) || r.motion.needsKeyframe(3) {
		t.Fatal("keyframe request not scoped to active epoch")
	}
	r.motion.offer(3, 0, data)
	if len(r.motion.queue) != 0 {
		t.Fatal("accepted stale stream")
	}
	r.motion.offer(4, 0, data)
	if r.motion.needsKeyframe(4) {
		t.Fatal("still requests keyframe after one was queued")
	}
	select {
	case p := <-r.motion.queue:
		if len(p) < 21 || binary.BigEndian.Uint16(p[16:18]) == 0 {
			t.Fatal("bad decoder packet")
		}
	default:
		t.Fatal("missing keyframe")
	}
	for i := 0; i < 40; i++ {
		r.motion.offer(4, int64(i), data)
	}
	if len(r.motion.queue) > cap(r.motion.queue) || r.motion.snapshot()["droppedFrames"].(uint64) == 0 {
		t.Fatal("backpressure not bounded")
	}
}

func TestMotionGenerationAbandonsStalePackets(t *testing.T) {
	r := testRunner(t)
	r.initMotion()
	stateMu.Lock()
	old := state
	state = r
	stateMu.Unlock()
	t.Cleanup(func() { stateMu.Lock(); state = old; stateMu.Unlock() })
	gen := int64(r.motion.generation)
	if !MotionGenerationCurrent(gen) {
		t.Fatal("current generation rejected")
	}
	ResetMotionDecoder()
	if MotionGenerationCurrent(gen) {
		t.Fatal("old generation accepted after reset")
	}
	if !r.motion.needsKeyframe(r.motion.epoch) {
		t.Fatal("reset did not request keyframe")
	}
}
