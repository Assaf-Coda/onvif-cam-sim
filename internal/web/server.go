// Package web serves the admin page (camera list, zone editor, add/remove camera)
// and its small JSON API, over HTTP and optionally HTTPS.
package web

import (
	"crypto/tls"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/camera"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/httpauth"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/httpsrv"
)

//go:embed index.html
var indexHTML []byte

// maxUpload caps a clip upload. ponytail: fixed 8 GiB; make it a config key if anyone hits it.
const maxUpload = 8 << 30

// Controller is what main provides: the running cameras and the operations on them.
type Controller interface {
	Cameras() []*camera.Camera
	Status(c *camera.Camera) (live bool, readers int)
	Add(c config.Camera) (*camera.Camera, error)
	Remove(name string) error
	SaveZones(c *camera.Camera, m config.Motion) error
}

type Server struct {
	log *slog.Logger
	cfg *config.Config
	ctl Controller
	srv *httpsrv.Dual
}

func New(log *slog.Logger, cfg *config.Config, ctl Controller, tlsCfg *tls.Config) *Server {
	s := &Server{log: log, cfg: cfg, ctl: ctl}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(indexHTML)
	}))
	mux.HandleFunc("GET /api/cameras", s.auth(s.listCameras))
	mux.HandleFunc("POST /api/cameras", s.auth(s.addCamera))
	mux.HandleFunc("DELETE /api/cameras/{name}", s.auth(s.removeCamera))
	mux.HandleFunc("GET /api/cameras/{name}/snapshot.jpg", s.auth(s.snapshot))
	mux.HandleFunc("PUT /api/cameras/{name}/zones", s.auth(s.putZones))
	s.srv = httpsrv.New(log, "admin", cfg.ListenIP, cfg.AdminPort, cfg.AdminHTTPS(), mux, tlsCfg)
	return s
}

func (s *Server) Start() error { return s.srv.Start() }
func (s *Server) Close()       { s.srv.Close() }

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !httpauth.Authorized(r, s.cfg.Users) {
			if httpauth.HasDigest(r) {
				httpauth.ChallengeStale(w)
			} else {
				httpauth.Challenge(w)
			}
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method != http.MethodGet && !httpauth.SameOrigin(r) {
			http.Error(w, "cross-site request refused", http.StatusForbidden)
			return
		}
		h(w, r)
	}
}

func (s *Server) find(name string) *camera.Camera {
	for _, c := range s.ctl.Cameras() {
		if c.Cfg.Name == name {
			return c
		}
	}
	return nil
}

type cameraJSON struct {
	Name           string         `json:"name"`
	Source         string         `json:"source"`
	OnvifPort      int            `json:"onvif_port"`
	OnvifHTTPSPort int            `json:"onvif_https_port"`
	RTSPPort       int            `json:"rtsp_port"`
	Codec          string         `json:"codec"`
	Width          int            `json:"width"`
	Height         int            `json:"height"`
	Live           bool           `json:"live"`
	Readers        int            `json:"readers"`
	FrameAgeMs     int64          `json:"frame_age_ms"`
	Motion         bool           `json:"motion"`
	MotionRatio    float64        `json:"motion_ratio"`
	Zones          zonesJSON      `json:"zones"`
	Settings       map[string]any `json:"settings"`
}

type zonesJSON struct {
	Include [][]config.Point `json:"include"`
	Exclude [][]config.Point `json:"exclude"`
}

func (s *Server) toJSON(c *camera.Camera) cameraJSON {
	live, readers := s.ctl.Status(c)
	active, ratio := c.Motion.State()
	info := c.Info()
	m := c.MotionCfg()
	return cameraJSON{
		Name: c.Cfg.Name, Source: c.Cfg.Source, OnvifPort: c.Cfg.OnvifPort, OnvifHTTPSPort: c.Cfg.HTTPSPort(), RTSPPort: c.Cfg.RTSPPort,
		Codec: info.VideoCodec, Width: info.Width, Height: info.Height,
		Live: live, Readers: readers, FrameAgeMs: c.LastFrameAge().Milliseconds(),
		Motion: active, MotionRatio: ratio,
		Zones:    zonesJSON{Include: nonNil(m.Include), Exclude: nonNil(m.Exclude)},
		Settings: map[string]any{"enabled": m.IsEnabled(), "sensitivity": m.Sensitivity, "pixel_threshold": m.PixelThreshold, "hold_seconds": m.HoldSeconds},
	}
}

func (s *Server) listCameras(w http.ResponseWriter, _ *http.Request) {
	cams := s.ctl.Cameras()
	out := make([]cameraJSON, 0, len(cams))
	for _, c := range cams {
		out = append(out, s.toJSON(c))
	}
	writeJSON(w, http.StatusOK, out)
}

func nonNil(z [][]config.Point) [][]config.Point {
	if z == nil {
		return [][]config.Point{}
	}
	return z
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// addCamera accepts multipart/form-data: name, and either an rtsp:// "source" or
// an uploaded "file"; optional onvif_port, rtsp_port, manufacturer, model, loop.
// The upload is streamed into media_dir under a temporary name and renamed once
// the whole form has been read.
func (s *Server) addCamera(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUpload)
	mr, err := r.MultipartReader()
	if err != nil {
		http.Error(w, "expected multipart/form-data: "+err.Error(), http.StatusBadRequest)
		return
	}
	fields := map[string]string{}
	var tmpPath, origName string
	cleanup := func() {
		if tmpPath != "" {
			_ = os.Remove(tmpPath)
		}
	}
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			cleanup()
			http.Error(w, "upload: "+err.Error(), http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" {
			b, _ := io.ReadAll(io.LimitReader(part, 4096))
			fields[part.FormName()] = strings.TrimSpace(string(b))
			continue
		}
		if part.FileName() == "" {
			continue // empty file input
		}
		if tmpPath != "" {
			cleanup()
			http.Error(w, "only one file per camera", http.StatusBadRequest)
			return
		}
		origName = filepath.Base(part.FileName())
		if err := os.MkdirAll(s.cfg.MediaDir, 0o755); err != nil {
			http.Error(w, "media dir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		tmp, err := os.CreateTemp(s.cfg.MediaDir, ".upload-*.tmp")
		if err != nil {
			http.Error(w, "media dir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		tmpPath = tmp.Name()
		_, err = io.Copy(tmp, part)
		tmp.Close()
		if err != nil {
			cleanup()
			http.Error(w, "upload: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	c := config.Camera{
		Name:         fields["name"],
		Source:       fields["source"],
		Manufacturer: fields["manufacturer"],
		Model:        fields["model"],
	}
	if c.Name == "" {
		cleanup()
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	if v := fields["loop"]; v == "false" {
		f := false
		c.Loop = &f
	}
	var httpsPort int
	for _, f := range []struct {
		key string
		dst *int
	}{{"onvif_port", &c.OnvifPort}, {"onvif_https_port", &httpsPort}, {"rtsp_port", &c.RTSPPort}} {
		if v := fields[f.key]; v != "" {
			n, err := strconv.Atoi(v)
			if err != nil {
				cleanup()
				http.Error(w, f.key+": not a number", http.StatusBadRequest)
				return
			}
			*f.dst = n
			if f.key == "onvif_https_port" {
				c.OnvifHTTPSPort = &httpsPort // 0 disables, see config.Camera
			}
		}
	}
	if tmpPath != "" {
		if c.Source != "" {
			cleanup()
			http.Error(w, "give either a file or an RTSP URL, not both", http.StatusBadRequest)
			return
		}
		dest := filepath.Join(s.cfg.MediaDir, safeName(c.Name)+"-"+safeName(origName))
		if _, err := os.Stat(dest); err == nil {
			cleanup()
			http.Error(w, "a file named "+filepath.Base(dest)+" already exists in "+s.cfg.MediaDir, http.StatusConflict)
			return
		}
		if err := os.Rename(tmpPath, dest); err != nil {
			cleanup()
			http.Error(w, "store upload: "+err.Error(), http.StatusInternalServerError)
			return
		}
		tmpPath = dest // removed below if Add fails
		c.Source = dest
		c.Uploaded = true
	} else if !strings.HasPrefix(strings.ToLower(c.Source), "rtsp://") {
		http.Error(w, "source must be an rtsp:// URL, or upload a file", http.StatusBadRequest)
		return
	}

	cam, err := s.ctl.Add(c)
	if err != nil {
		if cam == nil {
			cleanup()
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// camera is running but the config could not be saved
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, s.toJSON(cam))
}

func (s *Server) removeCamera(w http.ResponseWriter, r *http.Request) {
	err := s.ctl.Remove(r.PathValue("name"))
	switch {
	case errors.Is(err, os.ErrNotExist):
		http.NotFound(w, r)
	case err != nil:
		http.Error(w, err.Error(), http.StatusInternalServerError)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

// safeName keeps letters, digits, dot, dash and underscore.
func safeName(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '.', r == '-', r == '_':
			return r
		}
		return '_'
	}, s)
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
	m := c.MotionCfg()
	m.Include, m.Exclude = z.Include, z.Exclude
	if err := s.ctl.SaveZones(c, m); err != nil {
		s.log.Error("save config", "err", err)
		http.Error(w, "zones applied but config save failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	s.log.Info("zones updated", "camera", c.Cfg.Name, "include", len(z.Include), "exclude", len(z.Exclude))
	w.WriteHeader(http.StatusNoContent)
}
