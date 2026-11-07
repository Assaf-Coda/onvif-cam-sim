package motion

import (
	"testing"
	"time"

	"github.com/assafhalevy/onvif-cam-sim/internal/config"
)

// frame draws a white block at (bx,by) with size bs on a black w x h rgb24 frame.
func frame(w, h, bx, by, bs int) []byte {
	f := make([]byte, w*h*3)
	for y := by; y < by+bs && y < h; y++ {
		for x := bx; x < bx+bs && x < w; x++ {
			i := (y*w + x) * 3
			f[i], f[i+1], f[i+2] = 255, 255, 255
		}
	}
	return f
}

func run(t *testing.T, cfg config.Motion, bx, by int) (events []bool) {
	t.Helper()
	d := New(cfg)
	d.OnChange(func(a bool) { events = append(events, a) })
	w, h := 64, 32
	t0 := time.Unix(1000, 0)
	// static scene, then a block appears, then static again
	d.Feed(frame(w, h, 0, 0, 0), w, h, t0)
	d.Feed(frame(w, h, 0, 0, 0), w, h, t0.Add(200*time.Millisecond))
	d.Feed(frame(w, h, bx, by, 8), w, h, t0.Add(400*time.Millisecond))
	d.Feed(frame(w, h, bx, by, 8), w, h, t0.Add(600*time.Millisecond))
	d.Tick(t0.Add(5 * time.Second)) // past hold time
	return events
}

func TestMotionInsideIncludeZone(t *testing.T) {
	cfg := config.Motion{Sensitivity: 0.01, PixelThreshold: 25, HoldSeconds: 2,
		Include: [][]config.Point{{{0, 0}, {0.5, 0}, {0.5, 1}, {0, 1}}}} // left half
	ev := run(t, cfg, 4, 4)
	if len(ev) != 2 || !ev[0] || ev[1] {
		t.Fatalf("want [true false], got %v", ev)
	}
}

func TestMotionOutsideIncludeZone(t *testing.T) {
	cfg := config.Motion{Sensitivity: 0.01, PixelThreshold: 25, HoldSeconds: 2,
		Include: [][]config.Point{{{0, 0}, {0.5, 0}, {0.5, 1}, {0, 1}}}}
	if ev := run(t, cfg, 48, 4); len(ev) != 0 { // block in right half
		t.Fatalf("want no events, got %v", ev)
	}
}

func TestMotionInExcludeZone(t *testing.T) {
	cfg := config.Motion{Sensitivity: 0.01, PixelThreshold: 25, HoldSeconds: 2,
		Include: [][]config.Point{{{0, 0}, {1, 0}, {1, 1}, {0, 1}}},
		Exclude: [][]config.Point{{{0, 0}, {0.5, 0}, {0.5, 1}, {0, 1}}}} // exclude left half
	if ev := run(t, cfg, 4, 4); len(ev) != 0 {
		t.Fatalf("want no events, got %v", ev)
	}
	if ev := run(t, cfg, 48, 4); len(ev) != 2 {
		t.Fatalf("want events in right half, got %v", ev)
	}
}
