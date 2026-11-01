package mp4lite

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// CanonicalParameterSet ignores Annex-B trailing_zero_8bits, which can be
// present after an SPS/PPS at the end of an access unit. The RBSP stop bit
// ensures the final byte of a valid parameter set itself is not zero.
func CanonicalParameterSet(nal []byte) []byte {
	if len(nal) == 0 || (nal[0]&0x1f != 7 && nal[0]&0x1f != 8) {
		return nal
	}
	return bytes.TrimRight(nal, "\x00")
}

// AnnexBUnits splits H.264 access-unit bytes into individual NAL units.
func AnnexBUnits(data []byte) [][]byte {
	var nals [][]byte
	end := len(data)
	start := -1
	for i := 0; i+3 <= end; {
		prefix := 0
		if i+3 <= end && data[i] == 0 && data[i+1] == 0 && data[i+2] == 1 {
			prefix = 3
		} else if i+4 <= end && data[i] == 0 && data[i+1] == 0 && data[i+2] == 0 && data[i+3] == 1 {
			prefix = 4
		}
		if prefix == 0 {
			i++
			continue
		}
		if start >= 0 && i > start {
			nal := data[start:i]
			for len(nal) > 0 && nal[len(nal)-1] == 0 {
				nal = nal[:len(nal)-1]
			}
			if len(nal) > 0 {
				nals = append(nals, nal)
			}
		}
		i += prefix
		start = i
	}
	if start >= 0 && start < end {
		nal := data[start:]
		if len(nal) > 0 {
			nals = append(nals, nal)
		}
	}
	return nals
}

// NALInfo collects the AVC config and flags without retaining caller buffers.
func NALInfo(annexB []byte) (sps, pps []byte, idr, vcl bool) {
	for _, n := range AnnexBUnits(annexB) {
		switch n[0] & 0x1f {
		case 7:
			sps = append([]byte(nil), CanonicalParameterSet(n)...)
		case 8:
			pps = append([]byte(nil), CanonicalParameterSet(n)...)
		case 5:
			idr = true
			vcl = true
		case 1:
			vcl = true
		}
	}
	return
}

// AVCFrame converts a complete Annex-B H.264 access unit into MP4 length-prefixed NALs.
func AVCFrame(annexB []byte) ([]byte, error) {
	return avcFrame(annexB, nil, nil)
}

// avcFrame holds avc1's out-of-band parameter sets stable. Compatible changes
// only affect HRD bitrate metadata; rewrite those SPS NALs to the clip's
// original SPS, leaving pictures and SEI untouched. Incompatible changes are
// rejected rather than silently producing an MP4 with mismatched settings.
func avcFrame(annexB, initialSPS, initialPPS []byte) ([]byte, error) {
	nals := AnnexBUnits(annexB)
	if len(nals) == 0 {
		return nil, errors.New("not Annex-B H264")
	}
	buf := make([]byte, 0, len(annexB)+len(nals)*4)
	for _, nal := range nals {
		if len(initialSPS) > 0 && nal[0]&31 == 7 {
			if !SPSCompatible(nal, initialSPS) {
				return nil, errors.New("incompatible SPS in MP4 sample")
			}
			nal = initialSPS
		}
		if len(initialPPS) > 0 && nal[0]&31 == 8 {
			if !bytes.Equal(CanonicalParameterSet(nal), initialPPS) {
				return nil, errors.New("incompatible PPS in MP4 sample")
			}
			nal = initialPPS
		}
		if len(nal) >= 1<<28 {
			return nil, errors.New("H264 NAL too large")
		}
		b := make([]byte, 4)
		binary.BigEndian.PutUint32(b, uint32(len(nal)))
		buf = append(buf, b...)
		buf = append(buf, nal...)
	}
	return buf, nil
}
