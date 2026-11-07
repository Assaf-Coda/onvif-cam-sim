package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultHTTPSPortAvoidsExplicitPorts(t *testing.T) {
	dir := t.TempDir()
	clip := filepath.Join(dir, "a.mp4")
	os.WriteFile(clip, []byte("x"), 0o644)
	cfg := filepath.Join(dir, "config.yaml")
	os.WriteFile(cfg, []byte(`
users: [{name: a, password: b}]
cameras:
  - {name: a, source: `+clip+`, onvif_port: 8081, rtsp_port: 8554}
  - {name: b, source: `+clip+`, onvif_port: 9081, rtsp_port: 9082}
  - {name: c, source: `+clip+`, onvif_https_port: 0}
`), 0o644)
	c, err := Load(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Cameras[0].HTTPSPort(); got != 9083 { // 9081 and 9082 are taken
		t.Fatalf("camera a https port = %d, want 9083", got)
	}
	if got := c.Cameras[2].HTTPSPort(); got != 0 {
		t.Fatalf("explicit 0 must stay disabled, got %d", got)
	}
	if c.AdminHTTPS() != 8443 {
		t.Fatalf("admin https default = %d", c.AdminHTTPS())
	}
}
