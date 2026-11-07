// Package motion detects motion by frame differencing inside include zones and
// outside exclude zones, with an on/off hold time.
package motion

import (
	"sync"
	"time"

	"github.com/assafhalevy/onvif-cam-sim/internal/config"
)

type Detector struct {
	mu          sync.Mutex
	sensitivity float64
	threshold   int
	hold        time.Duration
	include     [][]config.Point
	exclude     [][]config.Point

	w, h      int
	mask      []bool
	maskCount int
	prev      []uint8
	gray      []uint8

	active    bool
	lastSeen  time.Time
	lastRatio float64
	listeners []func(bool)
}

func New(cfg config.Motion) *Detector {
	d := &Detector{}
	d.Configure(cfg)
	return d
}

// Configure applies sensitivity, threshold, hold time and zones; the mask is rebuilt on the next frame.
func (d *Detector) Configure(cfg config.Motion) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.sensitivity = cfg.Sensitivity
	d.threshold = cfg.PixelThreshold
	d.hold = time.Duration(cfg.HoldSeconds * float64(time.Second))
	d.include = cfg.Include
	d.exclude = cfg.Exclude
	d.mask = nil
}

// OnChange registers a callback for motion state transitions.
func (d *Detector) OnChange(f func(active bool)) {
	d.mu.Lock()
	d.listeners = append(d.listeners, f)
	d.mu.Unlock()
}

// State returns the current motion flag and the last changed-pixel ratio.
func (d *Detector) State() (bool, float64) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.active, d.lastRatio
}

// Feed analyses one rgb24 frame.
func (d *Detector) Feed(rgb []byte, w, h int, now time.Time) {
	d.mu.Lock()
	if d.mask == nil || d.w != w || d.h != h {
		d.w, d.h = w, h
		d.buildMask()
		d.prev = nil
	}
	if len(d.gray) != w*h {
		d.gray = make([]uint8, w*h)
	}
	for i, j := 0, 0; j < len(d.gray) && i+2 < len(rgb); i, j = i+3, j+1 {
		d.gray[j] = uint8((int(rgb[i])*77 + int(rgb[i+1])*150 + int(rgb[i+2])*29) >> 8)
	}
	changed := 0
	if d.prev != nil {
		for i, m := range d.mask {
			if !m {
				continue
			}
			diff := int(d.gray[i]) - int(d.prev[i])
			if diff < 0 {
				diff = -diff
			}
			if diff > d.threshold {
				changed++
			}
		}
	}
	d.prev, d.gray = d.gray, d.prev
	if d.gray == nil {
		d.gray = make([]uint8, w*h)
	}
	ratio := 0.0
	if d.maskCount > 0 {
		ratio = float64(changed) / float64(d.maskCount)
	}
	d.lastRatio = ratio
	if ratio > d.sensitivity {
		d.lastSeen = now
	}
	fire := d.update(now)
	d.mu.Unlock()
	fire()
}

// Tick re-evaluates the hold timer without a new frame (stream stalled or stopped).
func (d *Detector) Tick(now time.Time) {
	d.mu.Lock()
	fire := d.update(now)
	d.mu.Unlock()
	fire()
}

// update computes the new state; caller holds the lock. Returned func notifies listeners.
func (d *Detector) update(now time.Time) func() {
	want := !d.lastSeen.IsZero() && now.Sub(d.lastSeen) <= d.hold
	if want == d.active {
		return func() {}
	}
	d.active = want
	ls := append([]func(bool){}, d.listeners...)
	return func() {
		for _, f := range ls {
			f(want)
		}
	}
}

func (d *Detector) buildMask() {
	d.mask = make([]bool, d.w*d.h)
	d.maskCount = 0
	for y := 0; y < d.h; y++ {
		py := (float64(y) + 0.5) / float64(d.h)
		for x := 0; x < d.w; x++ {
			px := (float64(x) + 0.5) / float64(d.w)
			in := false
			for _, poly := range d.include {
				if inPolygon(px, py, poly) {
					in = true
					break
				}
			}
			if in {
				for _, poly := range d.exclude {
					if inPolygon(px, py, poly) {
						in = false
						break
					}
				}
			}
			if in {
				d.mask[y*d.w+x] = true
				d.maskCount++
			}
		}
	}
}

// inPolygon is the even-odd ray casting test.
func inPolygon(x, y float64, poly []config.Point) bool {
	in := false
	n := len(poly)
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := poly[i][0], poly[i][1]
		xj, yj := poly[j][0], poly[j][1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}
