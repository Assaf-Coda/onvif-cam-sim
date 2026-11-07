// Command onvif-cam-sim presents video files or RTSP streams as ONVIF cameras.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/assafhalevy/onvif-cam-sim/internal/camera"
	"github.com/assafhalevy/onvif-cam-sim/internal/config"
	"github.com/assafhalevy/onvif-cam-sim/internal/discovery"
	"github.com/assafhalevy/onvif-cam-sim/internal/onvif"
	"github.com/assafhalevy/onvif-cam-sim/internal/rtsp"
	"github.com/assafhalevy/onvif-cam-sim/internal/source"
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

	var devices []discovery.Device
	for _, cr := range cams {
		clog := log.With("camera", cr.cfg.Name)
		cam := camera.New(cr.cfg)
		if cr.info != nil {
			cam.SetInfo(cr.info)
		}
		rs := rtsp.New(clog, cfg.ListenIP, cr.cfg.RTSPPort, cfg.Users)
		if err := rs.Start(); err != nil {
			log.Error("rtsp listen", "camera", cr.cfg.Name, "err", err)
			os.Exit(1)
		}
		defer rs.Close()
		osrv := onvif.New(clog, cam, cfg.Users, cfg.ListenIP, cfg.AdvertisedIP, rtsp.Path)
		if err := osrv.Start(); err != nil {
			log.Error("onvif listen", "camera", cr.cfg.Name, "err", err)
			stop()
			return
		}
		defer osrv.Close()
		devices = append(devices, discovery.Device{UUID: cam.UUID, XAddr: osrv.XAddr("device_service"), Scopes: osrv.Scopes()})
		opts := source.Options{
			FFmpeg:     cfg.FFmpegPath,
			FFprobe:    cfg.FFprobePath,
			Source:     cr.cfg.Source,
			IsRTSP:     cr.cfg.IsRTSPSource(),
			Loop:       cr.cfg.LoopEnabled(),
			Info:       cr.info,
			OnInfo:     cam.SetInfo,
			PublishURL: fmt.Sprintf("rtsp://127.0.0.1:%d/%s", cr.cfg.RTSPPort, rtsp.Path),
			FrameWidth: cr.cfg.Motion.AnalysisWidth,
			FrameFPS:   cr.cfg.Motion.AnalysisFPS,
		}
		go source.Run(ctx, clog, opts, cam.OnFrame)
		clog.Info("camera up", "onvif", osrv.XAddr("device_service"), "rtsp", fmt.Sprintf("rtsp://%s:%d/%s", cfg.AdvertisedIP, cr.cfg.RTSPPort, rtsp.Path))
	}

	go func() {
		if err := discovery.New(log, devices).Run(ctx); err != nil {
			log.Error("ws-discovery", "err", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")
}
