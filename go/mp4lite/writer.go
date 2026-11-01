package mp4lite

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Writer builds an H.264 AVC MP4 without transcoding. It writes encoded samples
// directly to mdat and appends the movie index after recording ends. It expects
// monotonically increasing presentation times and no H.264 B-frame reordering
// (Nest/WebRTC typically uses constrained baseline / no B frames).
type Writer struct {
	file          *os.File
	name          string
	sps, pps      []byte
	width, height uint16
	entries       []entry
	origin        time.Duration
	originSet     bool
	closed        bool
}

type entry struct {
	offset int64
	size   uint32
	pts    time.Duration
	key    bool
}

func New(path string, sps, pps []byte) (*Writer, error) {
	if len(sps) < 4 || len(pps) < 2 || sps[0]&31 != 7 || pps[0]&31 != 8 {
		return nil, errors.New("MP4 requires complete SPS and PPS")
	}
	w, h, err := Dimensions(sps)
	if err != nil {
		return nil, fmt.Errorf("MP4 invalid SPS dimensions: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if _, err = f.Write(ftyp()); err != nil {
		f.Close()
		return nil, err
	}
	// MP4 64-bit mdat header allows a 32-bit compatible movie atom at the end.
	hdr := make([]byte, 16)
	binary.BigEndian.PutUint32(hdr, 1)
	copy(hdr[4:8], []byte("mdat"))
	if _, err = f.Write(hdr); err != nil {
		f.Close()
		return nil, err
	}
	return &Writer{file: f, name: path, sps: append([]byte(nil), sps...), pps: append([]byte(nil), pps...), width: w, height: h}, nil
}

func (w *Writer) Write(annexB []byte, pts time.Duration) error {
	if w.closed {
		return errors.New("MP4 already closed")
	}
	_, _, idr, vcl := NALInfo(annexB)
	if !vcl {
		return nil
	} // No standalone SPS/PPS or SEI as an MP4 sample.
	if len(w.entries) == 0 && !idr {
		return errors.New("MP4 first sample must be IDR")
	}
	if !w.originSet {
		w.origin = pts
		w.originSet = true
	}
	normalized := pts - w.origin
	if normalized < 0 {
		return errors.New("negative MP4 timestamp")
	}
	if len(w.entries) > 0 && normalized <= w.entries[len(w.entries)-1].pts {
		return errors.New("non-monotonic MP4 timestamps")
	}
	payload, err := avcFrame(annexB, w.sps, w.pps)
	if err != nil {
		return err
	}
	off, err := w.file.Seek(0, 1)
	if err != nil {
		return err
	}
	if _, err = w.file.Write(payload); err != nil {
		return err
	}
	w.entries = append(w.entries, entry{offset: off, size: uint32(len(payload)), pts: normalized, key: idr})
	return nil
}

func (w *Writer) Samples() int { return len(w.entries) }

// Close finalizes atom tables. Caller must rename .part into a public clip on
// success; a failed/empty clip is deliberately not exposed as a completed MP4.
func (w *Writer) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	if w.file == nil {
		return errors.New("closed MP4 without file")
	}
	if len(w.entries) == 0 {
		_ = w.file.Close()
		return errors.New("empty MP4")
	}
	end, err := w.file.Seek(0, 1)
	if err == nil {
		var sz [8]byte
		binary.BigEndian.PutUint64(sz[:], uint64(end-int64(len(ftyp()))))
		_, err = w.file.WriteAt(sz[:], int64(len(ftyp())+8))
	}
	if err == nil {
		_, err = w.file.Write(container(w.width, w.height, w.sps, w.pps, w.entries))
	}
	if err == nil {
		err = w.file.Sync()
	}
	if e := w.file.Close(); err == nil {
		err = e
	}
	return err
}

func (w *Writer) Abort() {
	if !w.closed && w.file != nil {
		_ = w.file.Close()
		w.closed = true
	}
	_ = os.Remove(w.name)
}
