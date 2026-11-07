package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/camera"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/discovery"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/onvif"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/rtsp"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/source"
)

// running is one camera with everything that serves it.
type running struct {
	cam    *camera.Camera
	rs     *rtsp.Server
	osrv   *onvif.Server
	cancel context.CancelFunc // stops ffmpeg and the motion ticker
}

// fleet owns the running cameras and the config file. It implements web.Controller
// so cameras can be added and removed from the admin page at runtime.
type fleet struct {
	log     *slog.Logger
	ctx     context.Context
	cfg     *config.Config
	cfgPath string
	tlsCfg  *tls.Config
	disc    *discovery.Service // set once discovery is up; nil before

	mu   sync.Mutex
	cams []*running
}

// start brings up RTSP, ONVIF, the ffmpeg supervisor and the motion ticker for c.
// info may be nil for an RTSP source that is offline; the supervisor keeps probing.
// The caller appends the result to f.cams.
func (f *fleet) start(c config.Camera, info *source.Info) (*running, error) {
	clog := f.log.With("camera", c.Name)
	cam := camera.New(c)
	if info != nil {
		cam.SetInfo(info)
	}
	rs := rtsp.New(clog, f.cfg.ListenIP, c.RTSPPort, f.cfg.Users)
	if err := rs.Start(); err != nil {
		return nil, fmt.Errorf("rtsp listen: %w", err)
	}
	osrv := onvif.New(clog, cam, f.cfg.Users, f.cfg.ListenIP, f.cfg.AdvertisedIP, rtsp.Path, f.tlsCfg)
	if err := osrv.Start(); err != nil {
		rs.Close()
		return nil, fmt.Errorf("onvif listen: %w", err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	opts := source.Options{
		FFmpeg:     f.cfg.FFmpegPath,
		FFprobe:    f.cfg.FFprobePath,
		Source:     c.Source,
		IsRTSP:     c.IsRTSPSource(),
		Loop:       c.LoopEnabled(),
		Info:       info,
		OnInfo:     cam.SetInfo,
		PublishURL: fmt.Sprintf("rtsp://127.0.0.1:%d/%s", c.RTSPPort, rtsp.Path),
		FrameWidth: c.Motion.AnalysisWidth,
		FrameFPS:   c.Motion.AnalysisFPS,
	}
	go source.Run(ctx, clog, opts, cam.OnFrame)
	go cam.RunMotionTicker(ctx)
	clog.Info("camera up", "onvif", osrv.XAddrs(), "rtsp", fmt.Sprintf("rtsp://%s:%d/%s", f.cfg.AdvertisedIP, c.RTSPPort, rtsp.Path))
	return &running{cam: cam, rs: rs, osrv: osrv, cancel: cancel}, nil
}

func (r *running) stop() {
	r.cancel()
	r.osrv.Close()
	r.rs.Close()
}

func (r *running) device() discovery.Device {
	return discovery.Device{UUID: r.cam.UUID, XAddr: r.osrv.XAddrs(), Scopes: r.osrv.Scopes()}
}

func (f *fleet) closeAll() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.cams {
		r.stop()
	}
}

func (f *fleet) devices() []discovery.Device {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]discovery.Device, 0, len(f.cams))
	for _, r := range f.cams {
		out = append(out, r.device())
	}
	return out
}

// ---- web.Controller ----

func (f *fleet) Cameras() []*camera.Camera {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*camera.Camera, 0, len(f.cams))
	for _, r := range f.cams {
		out = append(out, r.cam)
	}
	return out
}

func (f *fleet) Status(c *camera.Camera) (bool, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.cams {
		if r.cam == c {
			return r.rs.Live(), r.rs.Readers()
		}
	}
	return false, 0
}

// Add validates, probes, starts and persists a new camera. Empty ports are
// assigned the next free ones. A file source must probe successfully; an RTSP
// source that is offline is accepted and retried by the supervisor.
func (f *fleet) Add(c config.Camera) (*camera.Camera, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	used := map[int]bool{f.cfg.AdminPort: true, f.cfg.AdminHTTPSPort: true}
	for _, e := range f.cfg.Cameras {
		used[e.OnvifPort], used[e.OnvifHTTPSPort], used[e.RTSPPort] = true, true, true
	}
	if c.OnvifPort == 0 {
		c.OnvifPort = nextFree(used, 8081)
	}
	if c.OnvifHTTPSPort == 0 {
		c.OnvifHTTPSPort = nextFree(used, c.OnvifPort+1000)
	}
	if c.RTSPPort == 0 {
		c.RTSPPort = nextFree(used, 8554)
	}
	c.ApplyDefaults(len(f.cfg.Cameras))
	trial := *f.cfg
	trial.Cameras = append(append([]config.Camera{}, f.cfg.Cameras...), c)
	if err := trial.Validate(); err != nil {
		return nil, err
	}
	info, err := source.Probe(f.ctx, f.cfg.FFprobePath, c.Source)
	if err != nil {
		if !c.IsRTSPSource() {
			return nil, fmt.Errorf("probe: %w", err)
		}
		f.log.Warn("source offline, will retry", "camera", c.Name, "err", err)
	}
	r, err := f.start(c, info)
	if err != nil {
		return nil, err
	}
	f.cams = append(f.cams, r)
	f.cfg.Cameras = trial.Cameras
	if err := f.cfg.Save(f.cfgPath); err != nil {
		f.log.Error("save config", "err", err)
		return r.cam, fmt.Errorf("camera started but config save failed: %w", err)
	}
	if f.disc != nil {
		f.disc.Add(r.device())
	}
	f.log.Info("camera added", "camera", c.Name, "source", c.Source)
	return r.cam, nil
}

// Remove stops the camera, deletes it from the config and, when its source is a
// clip uploaded into media_dir, deletes that file too.
func (f *fleet) Remove(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	idx := -1
	for i, r := range f.cams {
		if r.cam.Cfg.Name == name {
			idx = i
		}
	}
	if idx < 0 {
		return os.ErrNotExist
	}
	r := f.cams[idx]
	r.stop()
	if f.disc != nil {
		f.disc.Remove(r.cam.UUID)
	}
	f.cams = append(f.cams[:idx], f.cams[idx+1:]...)
	for i, e := range f.cfg.Cameras {
		if e.Name == name {
			f.cfg.Cameras = append(f.cfg.Cameras[:i], f.cfg.Cameras[i+1:]...)
			break
		}
	}
	if err := f.cfg.Save(f.cfgPath); err != nil {
		return fmt.Errorf("camera stopped but config save failed: %w", err)
	}
	if src := r.cam.Cfg.Source; !r.cam.Cfg.IsRTSPSource() && insideDir(src, f.cfg.MediaDir) {
		if err := os.Remove(src); err == nil {
			f.log.Info("uploaded clip deleted", "file", src)
		}
	}
	f.log.Info("camera removed", "camera", name)
	return nil
}

func (f *fleet) SaveZones(c *camera.Camera, m config.Motion) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	c.SetMotionCfg(m)
	for i := range f.cfg.Cameras {
		if f.cfg.Cameras[i].Name == c.Cfg.Name {
			f.cfg.Cameras[i].Motion = m
		}
	}
	return f.cfg.Save(f.cfgPath)
}

func nextFree(used map[int]bool, from int) int {
	p := from
	for used[p] {
		p++
	}
	used[p] = true
	return p
}

func insideDir(path, dir string) bool {
	ap, err1 := filepath.Abs(path)
	ad, err2 := filepath.Abs(dir)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(ad, ap)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
