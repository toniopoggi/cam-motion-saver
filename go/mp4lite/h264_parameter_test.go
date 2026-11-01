package mp4lite

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"testing"
)

func TestSPSCompatibleBitrateChanges(t *testing.T) {
	// These SPS headers differ only in VUI HRD bitrate/CPB values.
	a, _ := hex.DecodeString("674d001f9a6602802dff80b7010101400000fa00001d4c3a1803a5000e92aef2e3430074a001d255de5c28")
	b, _ := hex.DecodeString("674d001f9a6602802dff80b7010101400000fa00001d4c3a1803cc000f2f2ef2e34300798001e5e5de5c28")
	if !SPSCompatible(a, b) {
		t.Fatal("equivalent HRD bitrate changes cause a split")
	}
	c := append([]byte(nil), b...)
	c[3]++
	if SPSCompatible(a, c) {
		t.Fatal("different profile/level treated as equivalent")
	}
	c = append([]byte(nil), b...)
	c[7] ^= 1
	if SPSCompatible(a, c) {
		t.Fatal("different image structure treated as equivalent")
	}
	pps := []byte{0x68, 0xee, 0x3c, 0x80}
	sample := append([]byte{0, 0, 0, 1}, b...)
	sample = append(sample, 0, 0, 0, 1)
	sample = append(sample, pps...)
	sample = append(sample, 0, 0, 0, 1, 0x65, 0x88)
	out, err := avcFrame(sample, a, pps)
	if err != nil {
		t.Fatal(err)
	}
	size := int(binary.BigEndian.Uint32(out[:4]))
	if size != len(a) || !bytes.Equal(out[4:4+size], a) {
		t.Fatal("MP4 sample did not retain original SPS")
	}
	if _, err = avcFrame(sample, c, pps); err == nil {
		t.Fatal("incompatible SPS accepted")
	}
}

func TestParameterSetPaddingDoesNotChangeCodec(t *testing.T) {
	sps := []byte{0x67, 0x42, 0xc0, 0x1e, 0xd9, 0x80}
	standalone := append(append([]byte{0, 0, 0, 1}, sps...), 0, 0)
	beforeFrame := append(append([]byte(nil), standalone...), 0, 0, 0, 1, 0x65, 0x80)
	a, _, _, _ := NALInfo(standalone)
	b, _, _, _ := NALInfo(beforeFrame)
	if !bytes.Equal(a, sps) || !bytes.Equal(a, b) {
		t.Fatalf("same SPS parsed inconsistently: %x vs %x", a, b)
	}
	changed := append([]byte(nil), sps...)
	changed[3]++
	if bytes.Equal(CanonicalParameterSet(changed), a) {
		t.Fatal("different parameter set incorrectly treated as same")
	}
}
