// Package camera holds the runtime state of one simulated camera shared by the
// ffmpeg pipeline, the ONVIF services and the web UI.
package camera

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"image"
	"image/jpeg"
	"sync"
	"sync/atomic"
	"time"

	"github.com/assafhalevy/onvif-cam-sim/internal/config"
	"github.com/assafhalevy/onvif-cam-sim/internal/motion"
	"github.com/assafhalevy/onvif-cam-sim/internal/source"
)

type Camera struct {
	Cfg    config.Camera
	UUID   string
	Motion *motion.Detector

	info atomic.Pointer[source.Info]

	mu      sync.Mutex
	rgb     []byte
	w, h    int
	frameAt time.Time
}

func New(cfg config.Camera) *Camera {
	return &Camera{Cfg: cfg, UUID: newUUID(), Motion: motion.New(cfg.Motion)}
}

// SetInfo records the probed source description.
func (c *Camera) SetInfo(i *source.Info) { c.info.Store(i) }

// Info returns the probed source description, or a plausible default while the
// source is still offline.
func (c *Camera) Info() *source.Info {
	if i := c.info.Load(); i != nil {
		return i
	}
	return &source.Info{VideoCodec: "h264", Width: 1920, Height: 1080, FPS: 25}
}

// OnFrame stores the latest decoded frame. Called from the ffmpeg reader goroutine.
func (c *Camera) OnFrame(rgb []byte, w, h int) {
	c.mu.Lock()
	if len(c.rgb) != len(rgb) {
		c.rgb = make([]byte, len(rgb))
	}
	copy(c.rgb, rgb)
	c.w, c.h = w, h
	c.frameAt = time.Now()
	c.mu.Unlock()
	if c.Cfg.Motion.IsEnabled() {
		c.Motion.Feed(rgb, w, h, time.Now())
	}
}

// RunMotionTicker expires the motion hold time while no frames arrive.
func (c *Camera) RunMotionTicker(ctx context.Context) {
	t := time.NewTicker(500 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c.Motion.Tick(now)
		}
	}
}

// SnapshotJPEG encodes the latest frame. Returns an error when no frame has arrived yet.
func (c *Camera) SnapshotJPEG() ([]byte, error) {
	c.mu.Lock()
	if c.rgb == nil {
		c.mu.Unlock()
		return nil, fmt.Errorf("no frame yet")
	}
	img := image.NewRGBA(image.Rect(0, 0, c.w, c.h))
	for i, j := 0, 0; i < len(c.rgb); i, j = i+3, j+4 {
		img.Pix[j], img.Pix[j+1], img.Pix[j+2], img.Pix[j+3] = c.rgb[i], c.rgb[i+1], c.rgb[i+2], 255
	}
	c.mu.Unlock()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// LastFrameAge reports how long ago the last decoded frame arrived (0 if none).
func (c *Camera) LastFrameAge() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.frameAt.IsZero() {
		return 0
	}
	return time.Since(c.frameAt)
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
