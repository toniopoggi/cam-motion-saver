package localmotion

import "testing"

func TestBaselineLocalizedMotionAndReset(t *testing.T) {
	d, e := New(48, 27, 75)
	if e != nil {
		t.Fatal(e)
	}
	a := make([]byte, 48*27)
	for i := range a {
		a[i] = 100
	}
	if hit, _, e := d.Observe(a); hit || e != nil {
		t.Fatal("baseline", hit, e)
	}
	b := append([]byte(nil), a...)
	for y := 10; y < 14; y++ {
		for x := 20; x < 24; x++ {
			b[y*48+x] = 25
		}
	}
	if hit, _, e := d.Observe(b); !hit || e != nil {
		t.Fatal("moving object", hit, e)
	}
	if hit, _, _ := d.Observe(b); hit {
		t.Fatal("static scene")
	}
	d.Reset()
	if hit, _, _ := d.Observe(b); hit {
		t.Fatal("reconnect must reset baseline")
	}
}
func TestExposureShiftAndSensitivity(t *testing.T) {
	a := make([]byte, 48*27)
	for i := range a {
		a[i] = 80
	}
	b := append([]byte(nil), a...)
	for i := range b {
		b[i] += 15
	}
	d, _ := New(48, 27, 75)
	d.Observe(a)
	if hit, _, _ := d.Observe(b); hit {
		t.Fatal("uniform exposure change")
	}
	c := append([]byte(nil), b...)
	for i := 0; i < 14; i++ {
		c[i] = 20
	}
	low, _ := New(48, 27, 1)
	low.Observe(b)
	l, _, _ := low.Observe(c)
	high, _ := New(48, 27, 100)
	high.Observe(b)
	h, _, _ := high.Observe(c)
	if l || !h {
		t.Fatalf("sensitivity low=%v high=%v", l, h)
	}
	if d.SetSensitivity(0) == nil {
		t.Fatal("accepted invalid sensitivity")
	}
	if _, _, e := d.Observe([]byte{1}); e == nil {
		t.Fatal("accepted invalid frame")
	}
}

func TestDimMovementAndAreaExclusion(t *testing.T) {
	a := make([]byte, 96*54)
	for i := range a {
		a[i] = 10
	}
	d, _ := New(96, 54, 100)
	if e := d.SetRegion(Region{X: 0, Y: 0, Width: 0.5, Height: 1}); e != nil {
		t.Fatal(e)
	}
	d.Observe(a)
	outside := append([]byte(nil), a...)
	for y := 10; y < 18; y++ {
		for x := 70; x < 78; x++ {
			outside[y*96+x] = 30
		}
	}
	if hit, _, _ := d.Observe(outside); hit {
		t.Fatal("outside region triggered")
	}
	inside := append([]byte(nil), outside...)
	for y := 10; y < 16; y++ {
		for x := 10; x < 16; x++ {
			inside[y*96+x] = 15
		}
	}
	if hit, _, _ := d.Observe(inside); !hit {
		t.Fatal("dim 5-level change not detected")
	}
	if d.SetRegion(Region{X: 0.9, Y: 0, Width: 0.2, Height: 1}) == nil {
		t.Fatal("invalid bounds accepted")
	}
}
