// Package config loads, validates and saves the simulator's YAML configuration.
package config

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

type Config struct {
	ListenIP       string   `yaml:"listen_ip"`
	AdvertisedIP   string   `yaml:"advertised_ip"`
	AdminPort      int      `yaml:"admin_port"`
	AdminHTTPSPort *int     `yaml:"admin_https_port"`   // nil = default 8443, 0 disables HTTPS for the admin page
	TLSCert        string   `yaml:"tls_cert,omitempty"` // PEM files; generated self-signed next to the config when empty
	TLSKey         string   `yaml:"tls_key,omitempty"`
	MediaDir       string   `yaml:"media_dir,omitempty"` // where uploaded clips are stored; default <config dir>/media
	FFmpegPath     string   `yaml:"ffmpeg_path"`
	FFprobePath    string   `yaml:"ffprobe_path"`
	Users          []User   `yaml:"users"`
	Cameras        []Camera `yaml:"cameras"`
}

type User struct {
	Name     string `yaml:"name"`
	Password string `yaml:"password"`
}

type Camera struct {
	Name           string `yaml:"name"`
	Source         string `yaml:"source"` // file path or rtsp:// URL
	Loop           *bool  `yaml:"loop,omitempty"`
	OnvifPort      int    `yaml:"onvif_port"`
	OnvifHTTPSPort *int   `yaml:"onvif_https_port"`   // nil = default onvif_port+1000 (or next free), 0 disables
	Uploaded       bool   `yaml:"uploaded,omitempty"` // clip was uploaded from the admin page; deleted with the camera
	RTSPPort       int    `yaml:"rtsp_port"`
	Manufacturer   string `yaml:"manufacturer,omitempty"`
	Model          string `yaml:"model,omitempty"`
	Firmware       string `yaml:"firmware,omitempty"`
	Serial         string `yaml:"serial,omitempty"`
	Motion         Motion `yaml:"motion"`
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

func (c Camera) LoopEnabled() bool { return c.Loop == nil || *c.Loop }
func (c Camera) HTTPSPort() int    { return deref(c.OnvifHTTPSPort) }
func (c Config) AdminHTTPS() int   { return deref(c.AdminHTTPSPort) }

func deref(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func ptr(v int) *int                { return &v }
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
	c.applyDefaults(filepath.Dir(path))
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

func (c *Config) applyDefaults(dir string) {
	if c.ListenIP == "" {
		c.ListenIP = "0.0.0.0"
	}
	if c.AdvertisedIP == "" {
		c.AdvertisedIP = outboundIP()
	}
	if c.AdminPort == 0 {
		c.AdminPort = 8000
	}
	if c.AdminHTTPSPort == nil {
		c.AdminHTTPSPort = ptr(8443)
	}
	if c.TLSCert == "" {
		c.TLSCert = filepath.Join(dir, "cert.pem")
	}
	if c.TLSKey == "" {
		c.TLSKey = filepath.Join(dir, "key.pem")
	}
	if c.MediaDir == "" {
		c.MediaDir = filepath.Join(dir, "media")
	}
	if c.FFmpegPath == "" {
		c.FFmpegPath = "ffmpeg"
	}
	if c.FFprobePath == "" {
		c.FFprobePath = "ffprobe"
	}
	used := c.UsedPorts()
	for i := range c.Cameras {
		c.Cameras[i].ApplyDefaults(i, used)
	}
}

// UsedPorts returns every port explicitly configured so far (admin and cameras).
func (c *Config) UsedPorts() map[int]bool {
	used := map[int]bool{}
	mark := func(p int) {
		if p > 0 {
			used[p] = true
		}
	}
	mark(c.AdminPort)
	mark(c.AdminHTTPS())
	for _, cam := range c.Cameras {
		mark(cam.OnvifPort)
		mark(cam.HTTPSPort())
		mark(cam.RTSPPort)
	}
	return used
}

// NextFree returns the first port >= from not in used, and marks it used.
func NextFree(used map[int]bool, from int) int {
	p := from
	for used[p] {
		p++
	}
	used[p] = true
	return p
}

// ApplyDefaults fills empty fields. i is the camera's index, used for the default
// serial and as the starting point for default ports; used holds the ports taken
// by the rest of the config so defaults never collide with explicit ones.
func (cam *Camera) ApplyDefaults(i int, used map[int]bool) {
	if cam.OnvifPort == 0 {
		cam.OnvifPort = NextFree(used, 8081+i)
	}
	if cam.OnvifHTTPSPort == nil {
		cam.OnvifHTTPSPort = ptr(NextFree(used, cam.OnvifPort+1000))
	}
	if cam.RTSPPort == 0 {
		cam.RTSPPort = NextFree(used, 8554+i)
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

func (c *Config) Validate() error {
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
	if p := c.AdminHTTPS(); p != 0 {
		if err := usePort(p, "admin_https_port"); err != nil {
			return err
		}
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
		if p := cam.HTTPSPort(); p != 0 {
			if err := usePort(p, cam.Name+" onvif_https_port"); err != nil {
				return err
			}
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
