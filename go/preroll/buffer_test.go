package preroll

import (
	"bytes"
	"testing"
	"time"
)

var base = time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)

func mk(i int, idr bool, epoch uint64) Unit {
	return Unit{At: base.Add(time.Duration(i) * time.Second), PTS: time.Duration(i) * time.Second, Data: []byte{byte(i + 1), 0x55}, IsKeyframe: idr, Epoch: epoch}
}

func TestFiveSecondPreRollStartsFromPriorIDR(t *testing.T) {
	b, _ := New(15*time.Second, 1024)
	b.SetCodec(1, []byte{0x67, 1}, []byte{0x68, 2})
	for i := 0; i <= 16; i++ {
		if e := b.Push(mk(i, i%4 == 0, 1)); e != nil {
			t.Fatal(e)
		}
	}
	s, ok := b.Get(base.Add(16*time.Second), 5*time.Second)
	if !ok || s.Truncated || len(s.Units) != 9 || !s.Units[0].IsKeyframe || s.Units[0].At != base.Add(8*time.Second) {
		t.Fatalf("bad keyframe-aligned snapshot: %#v", s)
	}
	if !bytes.Equal(s.Codec.SPS, []byte{0x67, 1}) {
		t.Fatal("no SPS")
	}
}

func TestTruncatedWhenNoEarlierIDR(t *testing.T) {
	b, _ := New(7*time.Second, 1024)
	for i := 0; i <= 10; i++ {
		if err := b.Push(mk(i, i == 0 || i == 8, 1)); err != nil {
			t.Fatal(err)
		}
	}
	s, ok := b.Get(base.Add(10*time.Second), 5*time.Second)
	if !ok || !s.Truncated || s.ActualStart != base.Add(8*time.Second) {
		t.Fatalf("unexpected snapshot: %#v", s)
	}
}

func TestNoIDRDoesNotReturnCorruptClip(t *testing.T) {
	b, _ := New(5*time.Second, 1024)
	for i := 0; i <= 6; i++ {
		if e := b.Push(mk(i, false, 1)); e != nil {
			t.Fatal(e)
		}
	}
	if _, ok := b.Get(base.Add(6*time.Second), 5*time.Second); ok {
		t.Fatal("must wait for IDR")
	}
}

func TestNoOldSessionsOrStaleTimestamps(t *testing.T) {
	b, _ := New(10*time.Second, 1024)
	if e := b.Push(mk(0, true, 1)); e != nil {
		t.Fatal(e)
	}
	if e := b.Push(mk(3, true, 2)); e != nil {
		t.Fatal(e)
	}
	if e := b.Push(mk(2, false, 2)); e == nil {
		t.Fatal("accepted out-of-order frame")
	}
	s, ok := b.Get(base.Add(3*time.Second), 5*time.Second)
	if !ok || len(s.Units) != 1 || s.Units[0].Epoch != 2 {
		t.Fatal("mixed two WebRTC sessions")
	}
}

func TestMemoryLimitAndCopies(t *testing.T) {
	b, _ := New(20*time.Second, 6)
	x := mk(0, true, 1)
	if e := b.Push(x); e != nil {
		t.Fatal(e)
	}
	x.Data[0] = 99
	if e := b.Push(mk(1, false, 1)); e != nil {
		t.Fatal(e)
	}
	if e := b.Push(mk(2, true, 1)); e != nil {
		t.Fatal(e)
	}
	if e := b.Push(mk(3, false, 1)); e != nil {
		t.Fatal(e)
	}
	n, size, _ := b.Stats()
	if size > 6 || n != 3 {
		t.Fatalf("memory limit failed: count=%d bytes=%d", n, size)
	}
	s, ok := b.Get(base.Add(3*time.Second), 2*time.Second)
	if !ok || s.ActualStart != base.Add(2*time.Second) {
		t.Fatalf("wrong IDR start: %#v", s)
	}
	s.Units[0].Data[0] = 0x55
	s2, _ := b.Get(base.Add(3*time.Second), 2*time.Second)
	if s2.Units[0].Data[0] == 0x55 {
		t.Fatal("leaked mutable frame buffer")
	}
}

func TestRetentionAndByteCaps(t *testing.T) {
	b, _ := New(3*time.Second, 4)
	for i := 0; i < 10; i++ {
		if err := b.Push(mk(i, i%3 == 0, 7)); err != nil {
			t.Fatal(err)
		}
	}
	n, bytes, age := b.Stats()
	if n != 2 || bytes != 4 || age > 3*time.Second {
		t.Fatalf("cap failed: %d %d %v", n, bytes, age)
	}
}

func TestInvalidInput(t *testing.T) {
	if _, e := New(0, 100); e == nil {
		t.Fatal("invalid retention")
	}
	b, _ := New(time.Second, 2)
	if e := b.Push(Unit{}); e == nil {
		t.Fatal("accepted invalid sample")
	}
	if _, ok := b.Get(time.Time{}, 5*time.Second); ok {
		t.Fatal("invalid trigger")
	}
}
