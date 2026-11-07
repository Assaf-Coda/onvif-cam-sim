# onvif-cam-sim

A small service that presents video files or live RTSP streams as ONVIF Profile S cameras, with real video motion detection on configurable include and exclude zones. It is meant for testing a VMS or VSaaS platform without physical cameras: discovery, authentication, RTSP streaming with audio, snapshots and motion events all behave like a camera would.

One process can simulate many cameras. Each camera gets its own ONVIF port and RTSP port on the host, so a VMS sees them as separate devices.

## What it does

- Takes an MP4 or MOV file (H.264 or H.265 video, any audio) and loops it as an RTSP stream, or relays an existing RTSP stream. Video is never re-encoded. AAC, G.711 and Opus audio pass through; other audio codecs are transcoded to AAC.
- Answers WS-Discovery probes and implements the ONVIF Device, Media, Events (PullPoint) and Analytics services needed by Profile S clients. WS-UsernameToken and HTTP Digest authentication are both supported.
- Detects motion by frame differencing on a downscaled decode of the stream, counted only inside the include polygons and outside the exclude polygons. State changes are delivered as `tns1:RuleEngine/CellMotionDetector/Motion` and `tns1:VideoSource/MotionAlarm` events.
- Serves a JPEG snapshot per camera through `GetSnapshotUri`.
- Provides an admin page where zones are drawn over the live snapshot and saved back to the config file. Changes apply immediately without a restart.

## Requirements

- Go 1.26 or newer to build (prebuilt binaries can be produced with `make release`).
- ffmpeg and ffprobe on the host. They run as separate processes and are not bundled.
  - macOS: `brew install ffmpeg`
  - Debian/Ubuntu: `sudo apt install ffmpeg`
  - Windows: `winget install ffmpeg` or a build from https://ffmpeg.org/download.html

## Quick start

```bash
git clone <repo> onvif-cam-sim && cd onvif-cam-sim
make build
cp config.example.yaml config.yaml      # edit sources, ports and credentials
./bin/onvif-cam-sim -check              # probes every source and exits
./bin/onvif-cam-sim                     # runs all cameras
```

Then open `http://<host>:8000/` or `https://<host>:8443/` with the configured user to see the cameras, their motion state and the zone editor. The same page adds and removes cameras at runtime: upload an MP4 or MOV clip or enter an RTSP URL with its port, and the camera starts immediately, is announced on WS-Discovery, and is written to `config.yaml`. Uploaded clips are stored in `media_dir` and deleted again when the camera is removed. Add a camera to your VMS either by ONVIF discovery or manually with the host IP, the camera's `onvif_port` (or `onvif_https_port`), and the configured user and password.

The config file may start with an empty `cameras` list if you prefer to add everything from the page.

Flags: `-config path` (default `config.yaml`), `-check`, `-debug`.

## Configuration

See `config.example.yaml`; every key is commented there. The essentials per camera:

| Key | Meaning |
|---|---|
| `source` | File path (MP4/MOV, H.264 or H.265) or an `rtsp://` URL to relay |
| `onvif_port`, `rtsp_port` | Unique ports per camera. UDP RTP uses `2*rtsp_port` and `2*rtsp_port+1` |
| `onvif_https_port` | HTTPS port for the same ONVIF services, default `onvif_port + 1000`; set to `-1` to disable |
| `motion.sensitivity` | Fraction of zone pixels that must change between analysed frames to trigger motion (0.02 = 2 percent) |
| `motion.pixel_threshold` | Per-pixel gray difference (0 to 255) that counts as a change |
| `motion.hold_seconds` | How long motion stays active after the last detected change |
| `motion.include`, `motion.exclude` | Polygons in normalized 0..1 coordinates. Include defaults to the full frame |

Saving zones or adding and removing cameras from the admin page rewrites `config.yaml`; comments in that file are not preserved.

### HTTPS

Every ONVIF port has an HTTPS twin (`onvif_https_port`) and the admin page has `admin_https_port` (default 8443, `-1` disables). Authentication is the same as over HTTP: WS-UsernameToken or HTTP Digest with the configured users. Responses follow the scheme the request came in on, so a VMS that connects over HTTPS receives HTTPS XAddrs, snapshot URI and subscription addresses, and WS-Discovery lists both addresses. On first start a self-signed certificate is generated with the standard library and written to `tls_cert` and `tls_key` (default `cert.pem` and `key.pem` next to the config); its subject alternative names cover `advertised_ip`, `127.0.0.1`, `localhost` and the hostname, and it is marked as a CA so a VMS can import `cert.pem` directly as a trust anchor. Point `tls_cert` and `tls_key` at your own PEM files to use a different certificate. RTSP itself stays plain, as Profile S specifies.

## What a VMS sees

| Item | Value |
|---|---|
| Device service | `http://<ip>:<onvif_port>/onvif/device_service` or `https://<ip>:<onvif_https_port>/onvif/device_service` (Media, Events and Analytics services on the same port) |
| Stream | `rtsp://<ip>:<rtsp_port>/stream`, RTSP over TCP or UDP, Basic or Digest auth |
| Snapshot | `http://<ip>:<onvif_port>/snapshot.jpg` (or the HTTPS equivalent), Basic or Digest auth |
| Profile | One fixed profile `prof_main` with video, audio (if the source has it) and a video analytics configuration |
| Events | PullPoint subscription; properties `IsMotion` and `State` with Initialized and Changed operations |
| Analytics | `GetRules` returns the cell motion rule and one `MotionRegionDetector` rule per zone (exclude zones are reported as disarmed) |

H.265 sources are advertised through the Media service with encoding `H265`. This is outside the Media v1 enumeration, which only knows JPEG, MPEG4 and H264, but it is what most VMS products accept today. Media2 (Profile T) is not implemented yet.

## Verification tools

`tools/onvif_check.py` is an end-to-end client check built on onvif-zeep (MIT). It runs discovery, reads device information, profiles and stream URI, fetches a snapshot and optionally pulls events:

```bash
python3 -m venv .venv && .venv/bin/pip install onvif-zeep requests
.venv/bin/python tools/onvif_check.py 192.168.1.50 8081 admin admin --discover --events 30
```

`go test ./...` runs the motion detector tests (zone inclusion, exclusion and hold time on synthetic frames).

## Notes and limits

- The source file's keyframe interval becomes the stream's keyframe interval. A VMS waits for a keyframe before showing video, so prefer sources with a GOP of one or two seconds. To re-encode a long-GOP file once: `ffmpeg -i in.mp4 -c:v libx264 -g 50 -c:a copy out.mp4`.
- Sources that are not H.264 or H.265 (for example ProRes MOV) are rejected at startup.
- The analysis and snapshot resolution is `analysis_width` wide (320 by default). Snapshots are therefore small; raise the width if your VMS shows them prominently.
- Profile T (Media2), Profile M, WS-BaseNotification push subscriptions, PTZ and multiple profiles per camera are not implemented.
- Authentication has no nonce replay protection and no clock skew check; this is a test tool for lab networks.

## License

MIT. See `LICENSE` and `NOTICE` for third-party components, including the LGPL ffmpeg runtime dependency.
