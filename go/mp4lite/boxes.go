package mp4lite

import (
	"bytes"
	"encoding/binary"
	"io"
	"time"
)

const timeScale uint32 = 90000

func u8(buf *bytes.Buffer, n byte)     { _ = buf.WriteByte(n) }
func u16(buf *bytes.Buffer, n uint16)  { _ = binary.Write(buf, binary.BigEndian, n) }
func u32(buf *bytes.Buffer, n uint32)  { _ = binary.Write(buf, binary.BigEndian, n) }
func u64(buf *bytes.Buffer, n uint64)  { _ = binary.Write(buf, binary.BigEndian, n) }
func str(buf *bytes.Buffer, s string)  { _, _ = buf.WriteString(s) }
func blob(buf *bytes.Buffer, b []byte) { _, _ = buf.Write(b) }
func zeros(buf *bytes.Buffer, n int)   { _, _ = buf.Write(make([]byte, n)) }
func box(typ string, data []byte) []byte {
	b := new(bytes.Buffer)
	u32(b, uint32(len(data)+8))
	str(b, typ)
	blob(b, data)
	return b.Bytes()
}
func fullBox(typ string, version byte, flags uint32, data []byte) []byte {
	b := new(bytes.Buffer)
	u8(b, version)
	u8(b, byte(flags>>16))
	u8(b, byte(flags>>8))
	u8(b, byte(flags))
	blob(b, data)
	return box(typ, b.Bytes())
}
func concat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
func ftyp() []byte                  { return box("ftyp", []byte("isom\x00\x00\x02\x00isomiso2avc1mp41")) }

var unityMatrix = []uint32{0x00010000, 0, 0, 0, 0x00010000, 0, 0, 0, 0x40000000}

func movieHeader(duration uint32) []byte {
	b := new(bytes.Buffer)
	u32(b, 0)
	u32(b, 0)
	u32(b, timeScale)
	u32(b, duration)
	u32(b, 0x00010000)
	u16(b, 0x0100)
	u16(b, 0)
	zeros(b, 8)
	for _, a := range unityMatrix {
		u32(b, a)
	}
	zeros(b, 24)
	u32(b, 2)
	return fullBox("mvhd", 0, 0, b.Bytes())
}
func trackHeader(duration uint32, width, height uint16) []byte {
	b := new(bytes.Buffer)
	u32(b, 0)
	u32(b, 0)
	u32(b, 1)
	u32(b, 0)
	u32(b, duration)
	zeros(b, 8)
	u16(b, 0)
	u16(b, 0)
	u16(b, 0)
	u16(b, 0)
	for _, a := range unityMatrix {
		u32(b, a)
	}
	u32(b, uint32(width)<<16)
	u32(b, uint32(height)<<16)
	return fullBox("tkhd", 0, 7, b.Bytes())
}
func mediaHeader(duration uint32) []byte {
	b := new(bytes.Buffer)
	u32(b, 0)
	u32(b, 0)
	u32(b, timeScale)
	u32(b, duration)
	// Language 'und' packed in three 5-bit letters.
	u16(b, 0x55c4)
	u16(b, 0)
	return fullBox("mdhd", 0, 0, b.Bytes())
}
func handler() []byte {
	b := new(bytes.Buffer)
	u32(b, 0)
	str(b, "vide")
	zeros(b, 12)
	str(b, "VideoHandler\x00")
	return fullBox("hdlr", 0, 0, b.Bytes())
}
func videoHeader() []byte {
	b := new(bytes.Buffer)
	u16(b, 0)
	u16(b, 0)
	u16(b, 0)
	u16(b, 0)
	return fullBox("vmhd", 0, 1, b.Bytes())
}
func dataReference() []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	blob(b, fullBox("url ", 0, 1, nil))
	return box("dinf", fullBox("dref", 0, 0, b.Bytes()))
}
func avcc(sps, pps []byte) []byte {
	b := new(bytes.Buffer)
	// configurationVersion, AVCProfileIndication, profile_compatibility, AVCLevelIndication
	u8(b, 1)
	u8(b, sps[1])
	u8(b, sps[2])
	u8(b, sps[3])
	u8(b, 0xff)
	u8(b, 0xe1)
	u16(b, uint16(len(sps)))
	blob(b, sps)
	u8(b, 1)
	u16(b, uint16(len(pps)))
	blob(b, pps)
	return box("avcC", b.Bytes())
}
func videoSampleEntry(width, height uint16, sps, pps []byte) []byte {
	b := new(bytes.Buffer)
	zeros(b, 6)
	u16(b, 1)
	zeros(b, 16)
	u16(b, width)
	u16(b, height)
	u32(b, 0x00480000)
	u32(b, 0x00480000)
	u32(b, 0)
	u16(b, 1)
	zeros(b, 32)
	u16(b, 0x0018)
	u16(b, 0xffff)
	blob(b, avcc(sps, pps))
	return box("avc1", b.Bytes())
}
func sampleDescription(width, height uint16, sps, pps []byte) []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	blob(b, videoSampleEntry(width, height, sps, pps))
	return fullBox("stsd", 0, 0, b.Bytes())
}
func sampleTimeTable(samples []entry) []byte {
	b := new(bytes.Buffer)
	type run struct{ count, delta uint32 }
	runs := make([]run, 0)
	var prior uint32 = 3000
	for i := range samples {
		delta := prior
		if i+1 < len(samples) {
			a, b2 := samples[i].pts, samples[i+1].pts
			d := b2 - a
			if d > 0 && d < time.Duration(10*time.Second) {
				delta = uint32(d * 90000 / time.Second)
			}
		}
		if delta == 0 {
			delta = 1
		}
		prior = delta
		if len(runs) > 0 && runs[len(runs)-1].delta == delta {
			runs[len(runs)-1].count++
		} else {
			runs = append(runs, run{1, delta})
		}
	}
	u32(b, uint32(len(runs)))
	for _, r := range runs {
		u32(b, r.count)
		u32(b, r.delta)
	}
	return fullBox("stts", 0, 0, b.Bytes())
}
func chunkLayout() []byte {
	b := new(bytes.Buffer)
	u32(b, 1)
	u32(b, 1)
	u32(b, 1)
	u32(b, 1)
	return fullBox("stsc", 0, 0, b.Bytes())
}
func sampleSizes(samples []entry) []byte {
	b := new(bytes.Buffer)
	u32(b, 0)
	u32(b, uint32(len(samples)))
	for _, s := range samples {
		u32(b, s.size)
	}
	return fullBox("stsz", 0, 0, b.Bytes())
}
func sampleOffsets(samples []entry) []byte {
	b := new(bytes.Buffer)
	u32(b, uint32(len(samples)))
	for _, s := range samples {
		u64(b, uint64(s.offset))
	}
	return fullBox("co64", 0, 0, b.Bytes())
}
func keyframes(samples []entry) []byte {
	b := new(bytes.Buffer)
	n := 0
	for _, s := range samples {
		if s.key {
			n++
		}
	}
	u32(b, uint32(n))
	for i, s := range samples {
		if s.key {
			u32(b, uint32(i+1))
		}
	}
	return fullBox("stss", 0, 0, b.Bytes())
}
func container(width, height uint16, sps, pps []byte, samples []entry) []byte {
	var duration uint32
	if len(samples) > 0 {
		delta := uint32(3000)
		if len(samples) > 1 {
			d := samples[len(samples)-1].pts - samples[len(samples)-2].pts
			if d > 0 && d < time.Second {
				delta = uint32(d * 90000 / time.Second)
			}
		}
		duration = uint32(samples[len(samples)-1].pts*90000/time.Second) + delta
	}
	stbl := box("stbl", concat(sampleDescription(width, height, sps, pps), sampleTimeTable(samples), chunkLayout(), sampleSizes(samples), sampleOffsets(samples), keyframes(samples)))
	minf := box("minf", concat(videoHeader(), dataReference(), stbl))
	mdia := box("mdia", concat(mediaHeader(duration), handler(), minf))
	trak := box("trak", concat(trackHeader(duration, width, height), mdia))
	return box("moov", concat(movieHeader(duration), trak))
}

// Seekable MP4 files need no transcoding and can be parsed on Android/desktop.
var _ io.Writer = (*bytes.Buffer)(nil)
