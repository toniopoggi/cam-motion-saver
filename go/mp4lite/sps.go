package mp4lite

import "errors"

var errSPS = errors.New("corrupt or unsupported H264 SPS")

type bitReader struct {
	data []byte
	pos  int
}

func (r *bitReader) bit() (uint64, error) {
	if r.pos >= len(r.data)*8 {
		return 0, errSPS
	}
	i := r.pos / 8
	n := 7 - r.pos%8
	r.pos++
	return uint64((r.data[i] >> n) & 1), nil
}
func (r *bitReader) bits(n int) (uint64, error) {
	if n > 32 {
		return 0, errSPS
	}
	var v uint64
	for i := 0; i < n; i++ {
		bit, e := r.bit()
		if e != nil {
			return 0, e
		}
		v = (v << 1) | bit
	}
	return v, nil
}
func (r *bitReader) ue() (uint64, error) {
	leading := 0
	for {
		bit, e := r.bit()
		if e != nil {
			return 0, e
		}
		if bit == 1 {
			break
		}
		leading++
		if leading > 31 {
			return 0, errSPS
		}
	}
	v, e := r.bits(leading)
	return ((uint64(1) << leading) - 1) + v, e
}
func (r *bitReader) se() (int64, error) {
	u, e := r.ue()
	if e != nil {
		return 0, e
	}
	if u%2 == 0 {
		return -int64(u / 2), nil
	}
	return int64((u + 1) / 2), nil
}
func (r *bitReader) skipScaling(size int) error {
	last, next := int64(8), int64(8)
	for i := 0; i < size; i++ {
		if next != 0 {
			v, e := r.se()
			if e != nil {
				return e
			}
			next = (last + v + 256) % 256
		}
		if next != 0 {
			last = next
		}
	}
	return nil
}
func unescapeEBSP(data []byte) []byte {
	out := make([]byte, 0, len(data))
	zeros := 0
	for _, b := range data {
		if zeros >= 2 && b == 3 {
			zeros = 0
			continue
		}
		out = append(out, b)
		if b == 0 {
			zeros++
		} else {
			zeros = 0
		}
	}
	return out
}

// Dimensions extracts H.264 frame width/height from a sequence parameter set.
// It handles baseline, main and high profiles plus the standard crop rules.
func Dimensions(sps []byte) (uint16, uint16, error) {
	w, h, _, err := parseSPS(sps)
	return w, h, err
}

func parseSPS(sps []byte) (uint16, uint16, *bitReader, error) {
	if len(sps) < 5 || sps[0]&0x1f != 7 {
		return 0, 0, nil, errSPS
	}
	profile := sps[1]
	r := bitReader{data: unescapeEBSP(sps[4:])}
	if _, e := r.ue(); e != nil {
		return 0, 0, nil, e
	} // seq_parameter_set_id
	chroma := uint64(1)
	switch profile {
	case 100, 110, 122, 244, 44, 83, 86, 118, 128, 138, 139, 134, 135:
		var e error
		chroma, e = r.ue()
		if e != nil {
			return 0, 0, nil, e
		}
		if chroma > 3 {
			return 0, 0, nil, errSPS
		}
		if chroma == 3 {
			if _, e = r.bit(); e != nil {
				return 0, 0, nil, e
			}
		}
		if _, e = r.ue(); e != nil {
			return 0, 0, nil, e
		}
		if _, e = r.ue(); e != nil {
			return 0, 0, nil, e
		}
		if _, e = r.bit(); e != nil {
			return 0, 0, nil, e
		}
		scaling, e := r.bit()
		if e != nil {
			return 0, 0, nil, e
		}
		if scaling == 1 {
			n := 8
			if chroma == 3 {
				n = 12
			}
			for i := 0; i < n; i++ {
				present, e := r.bit()
				if e != nil {
					return 0, 0, nil, e
				}
				if present == 1 {
					size := 16
					if i >= 6 {
						size = 64
					}
					if e = r.skipScaling(size); e != nil {
						return 0, 0, nil, e
					}
				}
			}
		}
	}
	if _, e := r.ue(); e != nil {
		return 0, 0, nil, e
	} // log2_max_frame_num_minus4
	order, e := r.ue()
	if e != nil {
		return 0, 0, nil, e
	}
	switch order {
	case 0:
		_, e = r.ue()
		if e != nil {
			return 0, 0, nil, e
		}
	case 1:
		if _, e = r.bit(); e != nil {
			return 0, 0, nil, e
		}
		if _, e = r.se(); e != nil {
			return 0, 0, nil, e
		}
		if _, e = r.se(); e != nil {
			return 0, 0, nil, e
		}
		count, x := r.ue()
		if x != nil || count > 256 {
			return 0, 0, nil, errSPS
		}
		for i := uint64(0); i < count; i++ {
			if _, e = r.se(); e != nil {
				return 0, 0, nil, e
			}
		}
	case 2:
	default:
		return 0, 0, nil, errSPS
	}
	if _, e = r.ue(); e != nil {
		return 0, 0, nil, e
	}
	if _, e = r.bit(); e != nil {
		return 0, 0, nil, e
	}
	picWidth, e := r.ue()
	if e != nil {
		return 0, 0, nil, e
	}
	picHeight, e := r.ue()
	if e != nil {
		return 0, 0, nil, e
	}
	frameOnly, e := r.bit()
	if e != nil {
		return 0, 0, nil, e
	}
	if frameOnly == 0 {
		if _, e = r.bit(); e != nil {
			return 0, 0, nil, e
		}
	}
	if _, e = r.bit(); e != nil {
		return 0, 0, nil, e
	}
	cropFlag, e := r.bit()
	if e != nil {
		return 0, 0, nil, e
	}
	var left, right, top, bottom uint64
	if cropFlag == 1 {
		if left, e = r.ue(); e != nil {
			return 0, 0, nil, e
		}
		if right, e = r.ue(); e != nil {
			return 0, 0, nil, e
		}
		if top, e = r.ue(); e != nil {
			return 0, 0, nil, e
		}
		if bottom, e = r.ue(); e != nil {
			return 0, 0, nil, e
		}
	}
	width := int64(picWidth+1) * 16
	height := int64(picHeight+1) * 16 * int64(2-frameOnly)
	subWidth, subHeight := int64(1), int64(1)
	if chroma == 1 {
		subWidth = 2
		subHeight = 2
	} else if chroma == 2 {
		subWidth = 2
	}
	width -= int64(left+right) * subWidth
	height -= int64(top+bottom) * subHeight * int64(2-frameOnly)
	if width < 1 || height < 1 || width > 65535 || height > 65535 {
		return 0, 0, nil, errSPS
	}
	return uint16(width), uint16(height), &r, nil
}
