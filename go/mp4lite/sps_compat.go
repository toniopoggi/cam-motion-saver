package mp4lite

import (
	"bytes"
	"errors"
)

type bitRange struct{ start, end int }

// SPSCompatible is conservative: only bitrate/CPB value changes inside VUI HRD
// metadata are tolerated. All other SPS bits must match, and parse errors fail
// closed so a genuine format change still starts a new file.
func SPSCompatible(a, b []byte) bool {
	a, b = CanonicalParameterSet(a), CanonicalParameterSet(b)
	if bytes.Equal(a, b) {
		return true
	}
	if len(a) < 5 || len(b) < 5 || !bytes.Equal(a[:4], b[:4]) {
		return false
	}
	x, err := spsHRDSignature(a)
	if err != nil {
		return false
	}
	y, err := spsHRDSignature(b)
	if err != nil {
		return false
	}
	return bytes.Equal(x, y)
}

func spsHRDSignature(sps []byte) ([]byte, error) {
	_, _, r, err := parseSPS(sps)
	if err != nil {
		return nil, err
	}
	var omit []bitRange
	flag := func() (bool, error) { v, e := r.bit(); return v == 1, e }
	skip := func(n int) error { _, e := r.bits(n); return e }
	vui, e := flag()
	if e != nil {
		return nil, e
	}
	if !vui {
		return nil, errors.New("no VUI HRD")
	}
	aspect, e := flag()
	if e != nil {
		return nil, e
	}
	if aspect {
		id, e := r.bits(8)
		if e != nil {
			return nil, e
		}
		if id == 255 {
			if e = skip(32); e != nil {
				return nil, e
			}
		}
	}
	over, e := flag()
	if e != nil {
		return nil, e
	}
	if over {
		if e = skip(1); e != nil {
			return nil, e
		}
	}
	video, e := flag()
	if e != nil {
		return nil, e
	}
	if video {
		if e = skip(4); e != nil {
			return nil, e
		}
		colour, e := flag()
		if e != nil {
			return nil, e
		}
		if colour {
			if e = skip(24); e != nil {
				return nil, e
			}
		}
	}
	chroma, e := flag()
	if e != nil {
		return nil, e
	}
	if chroma {
		if _, e = r.ue(); e != nil {
			return nil, e
		}
		if _, e = r.ue(); e != nil {
			return nil, e
		}
	}
	timing, e := flag()
	if e != nil {
		return nil, e
	}
	if timing {
		if e = skip(32); e != nil {
			return nil, e
		}
		if e = skip(32); e != nil {
			return nil, e
		}
		if e = skip(1); e != nil {
			return nil, e
		}
	}
	parseHRD := func() error {
		count, e := r.ue()
		if e != nil {
			return e
		}
		if count > 31 {
			return errSPS
		}
		if e = skip(8); e != nil {
			return e
		}
		for i := uint64(0); i <= count; i++ {
			for j := 0; j < 2; j++ {
				start := r.pos
				if _, e = r.ue(); e != nil {
					return e
				}
				omit = append(omit, bitRange{start, r.pos})
			}
			if e = skip(1); e != nil {
				return e
			}
		}
		return skip(20)
	}
	nal, e := flag()
	if e != nil {
		return nil, e
	}
	if nal {
		if e = parseHRD(); e != nil {
			return nil, e
		}
	}
	vcl, e := flag()
	if e != nil {
		return nil, e
	}
	if vcl {
		if e = parseHRD(); e != nil {
			return nil, e
		}
	}
	if !nal && !vcl {
		return nil, errors.New("no VUI HRD")
	}
	// Exact remaining bits include delay-field lengths, colour, timing and all
	// other information. Store one byte per bit to preserve Exp-Golomb lengths.
	out := make([]byte, 0, len(r.data)*8)
	j := 0
	for i := 0; i < len(r.data)*8; i++ {
		for j < len(omit) && i >= omit[j].end {
			j++
		}
		if j < len(omit) && i >= omit[j].start && i < omit[j].end {
			continue
		}
		out = append(out, (r.data[i/8]>>uint(7-i%8))&1)
	}
	for len(out) > 0 && out[len(out)-1] == 0 {
		out = out[:len(out)-1]
	}
	return out, nil
}
