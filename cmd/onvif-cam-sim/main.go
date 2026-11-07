// Command onvif-cam-sim presents video files or RTSP streams as ONVIF cameras.
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"syscall"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/discovery"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/source"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/tlscert"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/web"
)

func main() {
	cfgPath := flag.String("config", "config.yaml", "path to config file")
	check := flag.Bool("check", false, "probe sources and exit")
	debug := flag.Bool("debug", false, "debug logging")
	flag.Parse()

	lvl := slog.LevelInfo
	if *debug {
		lvl = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	slog.SetDefault(log)

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}
	for _, bin := range []string{cfg.FFmpegPath, cfg.FFprobePath} {
		if _, err := exec.LookPath(bin); err != nil {
			log.Error("ffmpeg is required: install it (brew install ffmpeg, apt install ffmpeg, winget install ffmpeg) or set ffmpeg_path/ffprobe_path in the config", "missing", bin)
			os.Exit(1)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	type camRun struct {
		cfg  config.Camera
		info *source.Info
	}
	var cams []camRun
	for _, c := range cfg.Cameras {
		info, err := source.Probe(ctx, cfg.FFprobePath, c.Source)
		if err != nil {
			// an RTSP source may simply be offline right now; the supervisor keeps probing
			if *check || !c.IsRTSPSource() {
				log.Error("probe failed", "camera", c.Name, "err", err)
				os.Exit(1)
			}
			log.Warn("source offline, will retry", "camera", c.Name, "err", err)
		} else {
			log.Info("source", "camera", c.Name, "video", info.VideoCodec,
				"size", fmt.Sprintf("%dx%d", info.Width, info.Height), "fps", fmt.Sprintf("%.2f", info.FPS),
				"audio", info.AudioCodec, "duration_s", fmt.Sprintf("%.1f", info.DurationSec))
		}
		cams = append(cams, camRun{c, info})
	}
	if *check {
		fmt.Println("all sources OK")
		return
	}

	// Always loaded: a camera added later from the admin page may enable HTTPS.
	cert, generated, err := tlscert.Load(cfg.TLSCert, cfg.TLSKey, cfg.AdvertisedIP)
	if err != nil {
		log.Error("tls", "err", err)
		os.Exit(1)
	}
	if generated {
		log.Info("self-signed certificate generated", "cert", cfg.TLSCert, "key", cfg.TLSKey)
	}
	tlsCfg := &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}

	f := &fleet{log: log, ctx: ctx, cfg: cfg, cfgPath: *cfgPath, tlsCfg: tlsCfg}
	defer f.closeAll()
	for _, cr := range cams {
		r, err := f.start(cr.cfg, cr.info)
		if err != nil {
			log.Error("camera start", "camera", cr.cfg.Name, "err", err)
			stop()
			return
		}
		f.cams = append(f.cams, r)
	}

	// discovery exists before the admin page can add cameras; Run below starts answering
	f.disc = discovery.New(log, f.devices())
	admin := web.New(log, cfg, f, tlsCfg)
	if err := admin.Start(); err != nil {
		log.Error("admin listen", "err", err)
		stop()
		return
	}
	defer admin.Close()
	log.Info("admin UI", "http", fmt.Sprintf("http://%s:%d/", cfg.AdvertisedIP, cfg.AdminPort), "https", adminHTTPS(cfg))

	go func() {
		if err := f.disc.Run(ctx); err != nil {
			log.Error("ws-discovery", "err", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
}

func adminHTTPS(cfg *config.Config) string {
	if cfg.AdminHTTPS() <= 0 {
		return "disabled"
	}
	return fmt.Sprintf("https://%s:%d/", cfg.AdvertisedIP, cfg.AdminHTTPS())
}
