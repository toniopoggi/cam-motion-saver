// Package localmotion detects changes in downsampled luminance frames.
package localmotion

import (
	"errors"
	"math"
)

// Region contains normalized coordinates relative to the complete video frame.
type Region struct {
	X      float64
	Y      float64
	Width  float64
	Height float64
}

func (r Region) Valid() bool {
	for _, v := range []float64{r.X, r.Y, r.Width, r.Height} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return false
		}
	}
	return r.X >= 0 && r.Y >= 0 && r.Width >= 0.02 && r.Height >= 0.02 && r.X+r.Width <= 1.0000001 && r.Y+r.Height <= 1.0000001
}
func FullRegion() Region { return Region{0, 0, 1, 1} }

type Detector struct {
	width, height, sensitivity int
	previous                   []byte
	region                     Region
}

func New(width, height, sensitivity int) (*Detector, error) {
	if width < 8 || height < 8 || width > 512 || height > 512 {
		return nil, errors.New("invalid dimensions")
	}
	d := &Detector{width: width, height: height, region: FullRegion()}
	if err := d.SetSensitivity(sensitivity); err != nil {
		return nil, err
	}
	return d, nil
}
func (d *Detector) SetSensitivity(s int) error {
	if s < 1 || s > 100 {
		return errors.New("sensitivity must be 1 to 100")
	}
	d.sensitivity = s
	return nil
}
func (d *Detector) SetRegion(r Region) error {
	if !r.Valid() {
		return errors.New("invalid region")
	}
	if r != d.region {
		d.region = r
		d.Reset()
	}
	return nil
}
func (d *Detector) Reset() { d.previous = nil }
func (d *Detector) Observe(frame []byte) (bool, float64, error) {
	if len(frame) != d.width*d.height {
		return false, 0, errors.New("unexpected frame size")
	}
	if d.previous == nil {
		d.previous = append([]byte(nil), frame...)
		return false, 0, nil
	}
	x0 := int(math.Floor(d.region.X * float64(d.width)))
	y0 := int(math.Floor(d.region.Y * float64(d.height)))
	x1 := int(math.Ceil((d.region.X + d.region.Width) * float64(d.width)))
	y1 := int(math.Ceil((d.region.Y + d.region.Height) * float64(d.height)))
	if x1 > d.width {
		x1 = d.width
	}
	if y1 > d.height {
		y1 = d.height
	}
	count := (x1 - x0) * (y1 - y0)
	delta := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := y*d.width + x
			delta += int(frame[i]) - int(d.previous[i])
		}
	}
	shift := delta / count
	if shift > 35 || shift < -35 {
		copy(d.previous, frame)
		return false, 0, nil
	}
	threshold := 22 - (d.sensitivity * 18 / 100)
	changed := 0
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			i := y*d.width + x
			n := int(frame[i]) - int(d.previous[i]) - shift
			if n < 0 {
				n = -n
			}
			if n >= threshold {
				changed++
			}
		}
	}
	copy(d.previous, frame)
	fraction := float64(changed) / float64(count)
	minimum := 0.024 - float64(d.sensitivity)*0.00020
	return fraction >= minimum, fraction, nil
}
