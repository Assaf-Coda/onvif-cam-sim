// Package config loads, validates and saves the simulator's YAML configuration.
package config

import (
	"fmt"
	"net"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenIP     string   `yaml:"listen_ip"`
	AdvertisedIP string   `yaml:"advertised_ip"`
	AdminPort    int      `yaml:"admin_port"`
	FFmpegPath   string   `yaml:"ffmpeg_path"`
	FFprobePath  string   `yaml:"ffprobe_path"`
	Users        []User   `yaml:"users"`
	Cameras      []Camera `yaml:"cameras"`
}

type User struct {
	Name     string `yaml:"name"`
	Password string `yaml:"password"`
}

type Camera struct {
	Name         string `yaml:"name"`
	Source       string `yaml:"source"` // file path or rtsp:// URL
	Loop         *bool  `yaml:"loop,omitempty"`
	OnvifPort    int    `yaml:"onvif_port"`
	RTSPPort     int    `yaml:"rtsp_port"`
	Manufacturer string `yaml:"manufacturer,omitempty"`
	Model        string `yaml:"model,omitempty"`
	Firmware     string `yaml:"firmware,omitempty"`
	Serial       string `yaml:"serial,omitempty"`
	Motion       Motion `yaml:"motion"`
}

// Point is a normalized (0..1) x,y coordinate.
type Point [2]float64

type Motion struct {
	Enabled        *bool     `yaml:"enabled,omitempty"`
	Sensitivity    float64   `yaml:"sensitivity"`     // changed-pixel ratio that triggers motion
	PixelThreshold int       `yaml:"pixel_threshold"` // 0-255 gray delta per pixel
	HoldSeconds    float64   `yaml:"hold_seconds"`    // keep motion on after last change
	AnalysisWidth  int       `yaml:"analysis_width"`  // width of decoded frames used for analysis and snapshots
	AnalysisFPS    int       `yaml:"analysis_fps"`
	Include        [][]Point `yaml:"include"`
	Exclude        [][]Point `yaml:"exclude"`
}

func (c Camera) LoopEnabled() bool  { return c.Loop == nil || *c.Loop }
func (c Camera) IsRTSPSource() bool { return strings.HasPrefix(strings.ToLower(c.Source), "rtsp://") }
func (m Motion) IsEnabled() bool    { return m.Enabled == nil || *m.Enabled }

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Save writes the config back. Comments in the original file are not preserved.
func (c *Config) Save(path string) error {
	b, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (c *Config) applyDefaults() {
	if c.ListenIP == "" {
		c.ListenIP = "0.0.0.0"
	}
	if c.AdvertisedIP == "" {
		c.AdvertisedIP = outboundIP()
	}
	if c.AdminPort == 0 {
		c.AdminPort = 8000
	}
	if c.FFmpegPath == "" {
		c.FFmpegPath = "ffmpeg"
	}
	if c.FFprobePath == "" {
		c.FFprobePath = "ffprobe"
	}
	for i := range c.Cameras {
		cam := &c.Cameras[i]
		if cam.OnvifPort == 0 {
			cam.OnvifPort = 8081 + i
		}
		if cam.RTSPPort == 0 {
			cam.RTSPPort = 8554 + i
		}
		if cam.Manufacturer == "" {
			cam.Manufacturer = "SimCam"
		}
		if cam.Model == "" {
			cam.Model = "S-1"
		}
		if cam.Firmware == "" {
			cam.Firmware = "1.0.0"
		}
		if cam.Serial == "" {
			cam.Serial = fmt.Sprintf("SIM-%04d", i+1)
		}
		m := &cam.Motion
		if m.Sensitivity == 0 {
			m.Sensitivity = 0.02
		}
		if m.PixelThreshold == 0 {
			m.PixelThreshold = 25
		}
		if m.HoldSeconds == 0 {
			m.HoldSeconds = 3
		}
		if m.AnalysisWidth == 0 {
			m.AnalysisWidth = 320
		}
		if m.AnalysisFPS == 0 {
			m.AnalysisFPS = 5
		}
		if len(m.Include) == 0 {
			m.Include = [][]Point{{{0, 0}, {1, 0}, {1, 1}, {0, 1}}}
		}
	}
}

func (c *Config) Validate() error {
	if len(c.Cameras) == 0 {
		return fmt.Errorf("no cameras configured")
	}
	if len(c.Users) == 0 {
		return fmt.Errorf("at least one user is required")
	}
	names := map[string]bool{}
	ports := map[int]string{}
	usePort := func(p int, what string) error {
		if p <= 0 || p > 32767 {
			return fmt.Errorf("%s: port %d out of range 1-32767", what, p)
		}
		if prev, ok := ports[p]; ok {
			return fmt.Errorf("%s: port %d already used by %s", what, p, prev)
		}
		ports[p] = what
		return nil
	}
	if err := usePort(c.AdminPort, "admin_port"); err != nil {
		return err
	}
	for _, cam := range c.Cameras {
		if cam.Name == "" || strings.ContainsAny(cam.Name, "/ \t?#") {
			return fmt.Errorf("camera name %q must be non-empty and contain no spaces or '/'", cam.Name)
		}
		if names[cam.Name] {
			return fmt.Errorf("duplicate camera name %q", cam.Name)
		}
		names[cam.Name] = true
		if cam.Source == "" {
			return fmt.Errorf("camera %s: source is required", cam.Name)
		}
		if !cam.IsRTSPSource() {
			if _, err := os.Stat(cam.Source); err != nil {
				return fmt.Errorf("camera %s: source file: %w", cam.Name, err)
			}
		}
		if err := usePort(cam.OnvifPort, cam.Name+" onvif_port"); err != nil {
			return err
		}
		if err := usePort(cam.RTSPPort, cam.Name+" rtsp_port"); err != nil {
			return err
		}
		for _, zs := range [][][]Point{cam.Motion.Include, cam.Motion.Exclude} {
			for _, poly := range zs {
				if len(poly) < 3 {
					return fmt.Errorf("camera %s: zone polygon needs at least 3 points", cam.Name)
				}
				for _, p := range poly {
					if p[0] < 0 || p[0] > 1 || p[1] < 0 || p[1] > 1 {
						return fmt.Errorf("camera %s: zone coordinates must be within 0..1", cam.Name)
					}
				}
			}
		}
	}
	return nil
}

// outboundIP returns the local IP used for the default route; no packet is sent.
func outboundIP() string {
	conn, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	return conn.LocalAddr().(*net.UDPAddr).IP.String()
}
