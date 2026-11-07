// Package source probes media sources and runs the ffmpeg process that feeds a camera.
package source

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Info describes the first video and audio stream of a source.
type Info struct {
	VideoCodec  string // h264 | hevc
	Width       int
	Height      int
	FPS         float64
	HasAudio    bool
	AudioCodec  string
	SampleRate  int
	Channels    int
	DurationSec float64
}

// Probe runs ffprobe on a file path or RTSP URL.
func Probe(ctx context.Context, ffprobe, src string) (*Info, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	args := []string{"-v", "error", "-print_format", "json", "-show_streams", "-show_format"}
	if strings.HasPrefix(strings.ToLower(src), "rtsp://") {
		args = append(args, "-rtsp_transport", "tcp")
	}
	args = append(args, src)
	out, err := exec.CommandContext(ctx, ffprobe, args...).Output()
	if err != nil {
		msg := err.Error()
		if ee, ok := err.(*exec.ExitError); ok && len(ee.Stderr) > 0 {
			msg = strings.TrimSpace(string(ee.Stderr))
		}
		return nil, fmt.Errorf("ffprobe %s: %s", src, msg)
	}
	var raw struct {
		Streams []struct {
			CodecType    string `json:"codec_type"`
			CodecName    string `json:"codec_name"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			RFrameRate   string `json:"r_frame_rate"`
			AvgFrameRate string `json:"avg_frame_rate"`
			SampleRate   string `json:"sample_rate"`
			Channels     int    `json:"channels"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("ffprobe output: %w", err)
	}
	info := &Info{}
	for _, s := range raw.Streams {
		switch {
		case s.CodecType == "video" && info.VideoCodec == "":
			info.VideoCodec = s.CodecName
			info.Width, info.Height = s.Width, s.Height
			info.FPS = parseRate(s.AvgFrameRate)
			if info.FPS == 0 {
				info.FPS = parseRate(s.RFrameRate)
			}
		case s.CodecType == "audio" && !info.HasAudio:
			info.HasAudio = true
			info.AudioCodec = s.CodecName
			info.SampleRate, _ = strconv.Atoi(s.SampleRate)
			info.Channels = s.Channels
		}
	}
	info.DurationSec, _ = strconv.ParseFloat(raw.Format.Duration, 64)
	if info.VideoCodec == "" {
		return nil, fmt.Errorf("%s: no video stream", src)
	}
	if info.VideoCodec != "h264" && info.VideoCodec != "hevc" {
		return nil, fmt.Errorf("%s: video codec %s not supported, source must be H.264 or H.265", src, info.VideoCodec)
	}
	if info.FPS <= 0 || info.FPS > 120 {
		info.FPS = 25
	}
	return info, nil
}

func parseRate(s string) float64 {
	num, den, ok := strings.Cut(s, "/")
	n, err1 := strconv.ParseFloat(num, 64)
	if !ok {
		if err1 != nil {
			return 0
		}
		return n
	}
	d, err2 := strconv.ParseFloat(den, 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// AnalysisSize returns the even-height analysis frame size for a given width.
func (i *Info) AnalysisSize(width int) (int, int) {
	if i.Width == 0 || i.Height == 0 {
		return width, width * 9 / 16
	}
	h := int(float64(width)*float64(i.Height)/float64(i.Width) + 0.5)
	if h%2 == 1 {
		h++
	}
	return width, h
}
