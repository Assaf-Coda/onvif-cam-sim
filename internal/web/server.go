// Package web serves the admin page (zone editor) and its small JSON API.
package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/camera"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/httpauth"
)

//go:embed index.html
var indexHTML []byte

// Status is provided by main so the page can show whether the stream is live.
type Status func(cam *camera.Camera) (live bool, readers int)

type Server struct {
	log     *slog.Logger
	cfg     *config.Config
	cfgPath string
	cams    []*camera.Camera
	status  Status
	http    *http.Server
	saveMu  sync.Mutex
}

func New(log *slog.Logger, cfg *config.Config, cfgPath string, cams []*camera.Camera, status Status) *Server {
	s := &Server{log: log, cfg: cfg, cfgPath: cfgPath, cams: cams, status: status}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	}))
	mux.HandleFunc("GET /api/cameras", s.auth(s.listCameras))
	mux.HandleFunc("GET /api/cameras/{name}/snapshot.jpg", s.auth(s.snapshot))
	mux.HandleFunc("PUT /api/cameras/{name}/zones", s.auth(s.putZones))
	s.http = &http.Server{Addr: fmt.Sprintf("%s:%d", cfg.ListenIP, cfg.AdminPort), Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	return s
}

func (s *Server) Start() error {
	go func() {
		if err := s.http.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("admin http", "err", err)
		}
	}()
	return nil
}

func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.http.Shutdown(ctx)
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httpauth.Authorized(r, s.cfg.Users) {
			httpauth.Challenge(w)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *Server) find(name string) *camera.Camera {
	for _, c := range s.cams {
		if c.Cfg.Name == name {
			return c
		}
	}
	return nil
}

type cameraJSON struct {
	Name        string         `json:"name"`
	Source      string         `json:"source"`
	OnvifPort   int            `json:"onvif_port"`
	RTSPPort    int            `json:"rtsp_port"`
	Codec       string         `json:"codec"`
	Width       int            `json:"width"`
	Height      int            `json:"height"`
	Live        bool           `json:"live"`
	Readers     int            `json:"readers"`
	FrameAgeMs  int64          `json:"frame_age_ms"`
	Motion      bool           `json:"motion"`
	MotionRatio float64        `json:"motion_ratio"`
	Zones       zonesJSON      `json:"zones"`
	Settings    map[string]any `json:"settings"`
}

type zonesJSON struct {
	Include [][]config.Point `json:"include"`
	Exclude [][]config.Point `json:"exclude"`
}

func (s *Server) listCameras(w http.ResponseWriter, _ *http.Request) {
	out := make([]cameraJSON, 0, len(s.cams))
	for _, c := range s.cams {
		live, readers := s.status(c)
		active, ratio := c.Motion.State()
		info := c.Info()
		m := c.MotionCfg()
		out = append(out, cameraJSON{
			Name: c.Cfg.Name, Source: c.Cfg.Source, OnvifPort: c.Cfg.OnvifPort, RTSPPort: c.Cfg.RTSPPort,
			Codec: info.VideoCodec, Width: info.Width, Height: info.Height,
			Live: live, Readers: readers, FrameAgeMs: c.LastFrameAge().Milliseconds(),
			Motion: active, MotionRatio: ratio,
			Zones:    zonesJSON{Include: nonNil(m.Include), Exclude: nonNil(m.Exclude)},
			Settings: map[string]any{"enabled": m.IsEnabled(), "sensitivity": m.Sensitivity, "pixel_threshold": m.PixelThreshold, "hold_seconds": m.HoldSeconds},
		})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func nonNil(z [][]config.Point) [][]config.Point {
	if z == nil {
		return [][]config.Point{}
	}
	return z
}

func (s *Server) snapshot(w http.ResponseWriter, r *http.Request) {
	c := s.find(r.PathValue("name"))
	if c == nil {
		http.NotFound(w, r)
		return
	}
	b, err := c.SnapshotJPEG()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}

func (s *Server) putZones(w http.ResponseWriter, r *http.Request) {
	c := s.find(r.PathValue("name"))
	if c == nil {
		http.NotFound(w, r)
		return
	}
	var z zonesJSON
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&z); err != nil {
		http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
		return
	}
	for _, zs := range [][][]config.Point{z.Include, z.Exclude} {
		for _, poly := range zs {
			if len(poly) < 3 {
				http.Error(w, "polygon needs at least 3 points", http.StatusBadRequest)
				return
			}
			for _, p := range poly {
				if p[0] < 0 || p[0] > 1 || p[1] < 0 || p[1] > 1 {
					http.Error(w, "coordinates must be within 0..1", http.StatusBadRequest)
					return
				}
			}
		}
	}
	if len(z.Include) == 0 {
		z.Include = [][]config.Point{{{0, 0}, {1, 0}, {1, 1}, {0, 1}}}
	}
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	m := c.MotionCfg()
	m.Include, m.Exclude = z.Include, z.Exclude
	c.SetMotionCfg(m)
	for i := range s.cfg.Cameras {
		if s.cfg.Cameras[i].Name == c.Cfg.Name {
			s.cfg.Cameras[i].Motion = m
		}
	}
	if err := s.cfg.Save(s.cfgPath); err != nil {
		s.log.Error("save config", "err", err)
		http.Error(w, "zones applied but config save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.log.Info("zones updated", "camera", c.Cfg.Name, "include", len(z.Include), "exclude", len(z.Exclude))
	w.WriteHeader(http.StatusNoContent)
}
