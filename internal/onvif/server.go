package onvif

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/assafhalevy/onvif-cam-sim/internal/camera"
	"github.com/assafhalevy/onvif-cam-sim/internal/config"
)

type handler func(*request) (string, error)

// Server serves the ONVIF services of one camera on its own HTTP port.
type Server struct {
	log      *slog.Logger
	cam      *camera.Camera
	users    []config.User
	ip       string // advertised IP
	port     int    // ONVIF HTTP port
	rtspPort int
	rtspPath string
	http     *http.Server
	handlers map[string]handler // "namespace|Action" or "Action"
	noAuth   map[string]bool
}

func New(log *slog.Logger, cam *camera.Camera, users []config.User, listenIP, advertisedIP string, rtspPath string) *Server {
	s := &Server{
		log:      log,
		cam:      cam,
		users:    users,
		ip:       advertisedIP,
		port:     cam.Cfg.OnvifPort,
		rtspPort: cam.Cfg.RTSPPort,
		rtspPath: rtspPath,
		handlers: map[string]handler{},
		noAuth: map[string]bool{
			"GetSystemDateAndTime": true, "GetCapabilities": true, "GetServices": true,
			"GetServiceCapabilities": true, "GetWsdlUrl": true, "GetHostname": true,
		},
	}
	s.registerDevice()
	s.registerMedia()
	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/", s.serveSOAP)
	mux.HandleFunc("/snapshot.jpg", s.serveSnapshot)
	s.http = &http.Server{
		Addr:              fmt.Sprintf("%s:%d", listenIP, s.port),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

func (s *Server) Start() error {
	ln, err := listen(s.http.Addr)
	if err != nil {
		return err
	}
	go func() {
		if err := s.http.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.log.Error("onvif http", "err", err)
		}
	}()
	return nil
}

func (s *Server) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.http.Shutdown(ctx)
}

func (s *Server) XAddr(service string) string {
	return fmt.Sprintf("http://%s:%d/onvif/%s", s.ip, s.port, service)
}

func (s *Server) on(ns, action string, h handler) {
	s.handlers[ns+"|"+action] = h
}

func (s *Server) serveSOAP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "SOAP endpoint", http.StatusMethodNotAllowed)
		return
	}
	req, err := parseRequest(r)
	if err != nil {
		if r.ContentLength == 0 && r.Header.Get("Authorization") == "" {
			// HTTP Digest clients (curl, some VMS) first send an empty POST to get the challenge
			challenge(w)
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		writeFault(w, "", &soapFault{400, "Sender", "ter:WellFormed", err.Error()})
		return
	}
	h, ok := s.handlers[req.ActionNS+"|"+req.Action]
	if !ok {
		// fall back to any namespace with that action name
		for k, v := range s.handlers {
			if strings.HasSuffix(k, "|"+req.Action) {
				h, ok = v, true
				break
			}
		}
	}
	s.log.Debug("soap", "action", req.Action, "from", r.RemoteAddr)
	if !ok {
		writeFault(w, req.MessageID, faultActionNotSupported(req.Action))
		return
	}
	if !s.noAuth[req.Action] && !req.authenticated(s.users) && !httpAuthorized(r, s.users) {
		if !req.hasToken && r.Header.Get("Authorization") == "" {
			challenge(w)
			writeFault(w, req.MessageID, &soapFault{401, "Sender", "ter:NotAuthorized", "Authentication required"})
			return
		}
		s.log.Warn("unauthorized", "action", req.Action, "user", req.username, "from", r.RemoteAddr)
		writeFault(w, req.MessageID, faultNotAuthorized())
		return
	}
	body, err := h(req)
	if err != nil {
		var f *soapFault
		if !errors.As(err, &f) {
			f = &soapFault{500, "Receiver", "ter:Action", err.Error()}
		}
		writeFault(w, req.MessageID, f)
		return
	}
	writeEnvelope(w, 200, req.MessageID, "", body)
}

func (s *Server) serveSnapshot(w http.ResponseWriter, r *http.Request) {
	if !httpAuthorized(r, s.users) {
		challenge(w)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	b, err := s.cam.SnapshotJPEG()
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(b)
}
