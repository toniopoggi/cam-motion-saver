package engine

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color/palette"
	"image/draw"
	"image/gif"
	_ "image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// SavePreview accepts six length-prefixed JPEG frames decoded by Android's
// hardware-backed MediaMetadataRetriever. The saved GIF loops for three
// seconds, keeping video decoding off the Go/WebRTC capture thread.
func SavePreview(name string, frames []byte) string {
	stateMu.Lock()
	r := state
	stateMu.Unlock()
	if r == nil {
		return "Recorder service is not running"
	}
	if err := writePreview(r.dir, name, frames); err != nil {
		return err.Error()
	}
	r.previewMu.Lock()
	err := r.storePreviewState(name, previewState{Status: "ready", Updated: time.Now()})
	r.previewMu.Unlock()
	if err != nil {
		return err.Error()
	}
	return ""
}

func writePreview(dir, name string, frames []byte) error {
	if name != filepath.Base(name) || !strings.HasSuffix(name, ".mp4") || strings.ContainsAny(name, "/\\") {
		return errors.New("invalid clip name")
	}
	if len(frames) > 8<<20 {
		return errors.New("preview input too large")
	}
	clip := filepath.Join(dir, "clips", name)
	if _, err := os.Stat(clip); err != nil {
		return err
	}
	reader := bytes.NewReader(frames)
	animation := &gif.GIF{LoopCount: 0}
	for i := 0; i < 6 && reader.Len() > 0; i++ {
		var size uint32
		if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
			return err
		}
		if size == 0 || size > 1<<20 || int(size) > reader.Len() {
			return errors.New("invalid preview frame size")
		}
		b := make([]byte, int(size))
		if _, err := io.ReadFull(reader, b); err != nil {
			return err
		}
		config, _, err := image.DecodeConfig(bytes.NewReader(b))
		if err != nil {
			return err
		}
		if config.Width < 1 || config.Height < 1 || config.Width > 320 || config.Height > 320 {
			return errors.New("preview dimensions too large")
		}
		src, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			return err
		}
		dst := image.NewPaletted(src.Bounds(), palette.Plan9)
		draw.Draw(dst, dst.Bounds(), src, src.Bounds().Min, draw.Src)
		if len(animation.Image) > 0 && animation.Image[0].Bounds() != dst.Bounds() {
			return errors.New("preview frames differ in size")
		}
		animation.Image = append(animation.Image, dst)
		animation.Delay = append(animation.Delay, 50)
	}
	if len(animation.Image) == 0 {
		return errors.New("no preview frames decoded")
	}
	for i := range animation.Delay {
		animation.Delay[i] = (i+1)*300/len(animation.Delay) - i*300/len(animation.Delay)
	}
	if reader.Len() != 0 {
		return errors.New("unexpected preview data")
	}
	dest := clip + ".gif"
	f, err := os.OpenFile(dest+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = gif.EncodeAll(f, animation)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err == nil {
		// Retention may have removed the clip while Android decoded its frames.
		if _, statErr := os.Stat(clip); statErr != nil {
			err = statErr
		} else {
			err = os.Rename(dest+".part", dest)
		}
	}
	if err != nil {
		_ = os.Remove(dest + ".part")
	}
	return err
}
