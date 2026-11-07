package source

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// Options controls the ffmpeg command for one camera.
type Options struct {
	FFmpeg     string
	FFprobe    string
	Source     string
	IsRTSP     bool
	Loop       bool
	Info       *Info // nil: probed by Run before the first start (RTSP sources that may be offline)
	OnInfo     func(*Info)
	PublishURL string // rtsp://127.0.0.1:port/path on the local server
	FrameWidth int    // analysis frame width, 0 disables the raw frame output
	FrameFPS   int

	frameW, frameH int
}

// Args builds the ffmpeg argument list. Video is passed through untouched; audio is
// copied when the codec is RTP-friendly and transcoded to AAC otherwise.
func Args(o Options) []string {
	a := []string{"-hide_banner", "-loglevel", "warning", "-nostdin"}
	if o.IsRTSP {
		a = append(a, "-rtsp_transport", "tcp")
	} else {
		a = append(a, "-re")
		if o.Loop {
			a = append(a, "-stream_loop", "-1")
		}
	}
	a = append(a, "-i", o.Source)

	// output 1: RTSP publish
	a = append(a, "-map", "0:v:0", "-c:v", "copy")
	if o.Info.HasAudio {
		a = append(a, "-map", "0:a:0")
		switch o.Info.AudioCodec {
		case "aac", "pcm_mulaw", "pcm_alaw", "opus":
			a = append(a, "-c:a", "copy")
		default:
			a = append(a, "-c:a", "aac", "-b:a", "96k")
		}
	}
	a = append(a, "-f", "rtsp", "-rtsp_transport", "tcp", o.PublishURL)

	// output 2: small raw RGB frames for motion detection and snapshots
	if o.FrameWidth > 0 {
		a = append(a, "-map", "0:v:0", "-an",
			"-vf", fmt.Sprintf("fps=%d,scale=%d:%d", o.FrameFPS, o.frameW, o.frameH),
			"-pix_fmt", "rgb24", "-f", "rawvideo", "pipe:1")
	}
	return a
}

// FrameFunc receives one rgb24 frame of w x h pixels. The slice is reused; copy it if kept.
type FrameFunc func(rgb []byte, w, h int)

// Run supervises ffmpeg until ctx is cancelled, restarting it with backoff.
func Run(ctx context.Context, log *slog.Logger, o Options, onFrame FrameFunc) {
	backoff := time.Second
	for {
		start := time.Now()
		var err error
		if o.Info == nil {
			o.Info, err = Probe(ctx, o.FFprobe, o.Source)
			if err == nil && o.OnInfo != nil {
				o.OnInfo(o.Info)
			}
		}
		if err == nil {
			o.frameW, o.frameH = o.Info.AnalysisSize(o.FrameWidth)
			err = runOnce(ctx, log, o, onFrame)
		}
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) > time.Minute {
			backoff = time.Second
		}
		log.Warn("source down, retrying", "err", err, "in", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

func runOnce(ctx context.Context, log *slog.Logger, o Options, onFrame FrameFunc) error {
	cmd := exec.CommandContext(ctx, o.FFmpeg, Args(o)...)
	cmd.Cancel = func() error { return cmd.Process.Kill() }
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	var stdout io.ReadCloser
	if o.FrameWidth > 0 {
		if stdout, err = cmd.StdoutPipe(); err != nil {
			return err
		}
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	log.Info("ffmpeg started", "pid", cmd.Process.Pid)
	go func() {
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line != "" {
				log.Warn("ffmpeg: " + line)
			}
		}
	}()
	if stdout != nil {
		frameSize := o.frameW * o.frameH * 3
		buf := make([]byte, frameSize)
		r := bufio.NewReaderSize(stdout, frameSize*2)
		for {
			if _, err := io.ReadFull(r, buf); err != nil {
				break
			}
			onFrame(buf, o.frameW, o.frameH)
		}
	}
	return cmd.Wait()
}
