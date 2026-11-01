package mp4lite

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMP4DecodesWithFFmpeg(t *testing.T) {
	data, err := os.ReadFile("testdata/sample.h264")
	if err != nil {
		t.Fatal(err)
	}
	nals := AnnexBUnits(data)
	if len(nals) < 50 {
		t.Fatalf("too few NALs: %d", len(nals))
	}
	var sps, pps []byte
	var frames [][]byte
	var current []byte
	for _, nal := range nals {
		if nal[0]&31 == 9 && len(current) > 0 {
			frames = append(frames, current)
			current = nil
		}
		current = append(current, 0, 0, 0, 1)
		current = append(current, nal...)
		if nal[0]&31 == 7 {
			sps = append([]byte(nil), nal...)
		}
		if nal[0]&31 == 8 {
			pps = append([]byte(nil), nal...)
		}
	}
	if len(current) > 0 {
		frames = append(frames, current)
	}
	w, h, err := Dimensions(sps)
	if err != nil || w != 640 || h != 360 {
		t.Fatalf("SPS dimensions %d x %d %v", w, h, err)
	}
	out := filepath.Join(t.TempDir(), "capture.mp4")
	mux, err := New(out, sps, pps)
	if err != nil {
		t.Fatal(err)
	}
	for i, f := range frames {
		if err := mux.Write(f, time.Duration(i)*50*time.Millisecond); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	if mux.Samples() != 60 {
		t.Errorf("samples %d expected 60", mux.Samples())
	}
	if err := mux.Close(); err != nil {
		t.Fatal(err)
	}
	if path, err := exec.LookPath("ffprobe"); err == nil {
		o, e := exec.Command(path, "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=codec_name,width,height,nb_frames,duration", "-of", "default=noprint_wrappers=1", out).CombinedOutput()
		if e != nil {
			t.Fatalf("ffprobe: %v %s", e, o)
		}
		got := string(o)
		for _, expect := range []string{"codec_name=h264", "width=640", "height=360", "nb_frames=60"} {
			if !strings.Contains(got, expect) {
				t.Errorf("missing %s: %s", expect, got)
			}
		}
	}
	if path, err := exec.LookPath("ffmpeg"); err == nil {
		o, e := exec.Command(path, "-hide_banner", "-v", "error", "-i", out, "-f", "null", "-").CombinedOutput()
		if e != nil {
			t.Fatalf("ffmpeg failed to decode saved MP4: %v %s", e, o)
		}
		if len(o) > 0 {
			t.Errorf("ffmpeg decoding warnings: %s", o)
		}
	}
}
func TestWriterRequiresKeyframe(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x01, 0x40, 0x7b, 0x20}
	pps := []byte{0x68, 0xce, 0x3c, 0x80}
	_, _, e := Dimensions(sps)
	if e != nil {
		t.Skip("test fixture is not complete")
	}
	w, e := New(filepath.Join(t.TempDir(), "x.mp4"), sps, pps)
	if e != nil {
		t.Fatal(e)
	}
	if err := w.Write([]byte{0, 0, 0, 1, 0x41, 0x11}, 0); err == nil {
		t.Fatal("accepted P frame first")
	}
	w.Abort()
}
