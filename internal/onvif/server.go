package onvif

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/camera"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/httpauth"
	"github.com/Assaf-Coda/onvif-cam-sim/internal/httpsrv"
)

type handler func(*request) (string, error)

// Server serves the ONVIF services of one camera on its own HTTP port.
type Server struct {
	log       *slog.Logger
	cam       *camera.Camera
	users     []config.User
	ip        string // advertised IP
	port      int    // ONVIF HTTP port
	httpsPort int    // 0 = no HTTPS listener
	rtspPort  int
	rtspPath  string
	srv       *httpsrv.Dual
	handlers  map[string]handler // "namespace|Action" or "Action"
	noAuth    map[string]bool
	events    *eventBus
}

// New builds the ONVIF server for cam. When tlsCfg is non-nil and the camera has
// an onvif_https_port, the same services are also served over HTTPS; XAddrs and
// URIs in responses use the scheme and port the request arrived on.
func New(log *slog.Logger, cam *camera.Camera, users []config.User, listenIP, advertisedIP string, rtspPath string, tlsCfg *tls.Config) *Server {
	s := &Server{
		log:      log,
		cam:      cam,
		users:    users,
		ip:       advertisedIP,
		port:     cam.Cfg.OnvifPort,
		rtspPort: cam.Cfg.RTSPPort,
		rtspPath: rtspPath,
		handlers: map[string]handler{},
		events:   newEventBus(),
		noAuth: map[string]bool{
			"GetSystemDateAndTime": true, "GetCapabilities": true, "GetServices": true,
			"GetServiceCapabilities": true, "GetWsdlUrl": true, "GetHostname": true,
		},
	}
	s.registerDevice()
	s.registerMedia()
	s.registerEvents()
	s.registerAnalytics()
	cam.Motion.OnChange(func(active bool) {
		log.Info("motion", "active", active)
		s.events.setMotion(active)
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/onvif/", s.serveSOAP)
	mux.HandleFunc("/snapshot.jpg", s.serveSnapshot)
	if tlsCfg != nil {
		s.httpsPort = cam.Cfg.HTTPSPort()
	}
	s.srv = httpsrv.New(log, "onvif", listenIP, s.port, s.httpsPort, mux, tlsCfg)
	return s
}

func (s *Server) Start() error { return s.srv.Start() }
func (s *Server) Close()       { s.srv.Close() }

// XAddr is the HTTP address of a service, used for WS-Discovery and logs.
func (s *Server) XAddr(service string) string {
	return fmt.Sprintf("http://%s:%d/onvif/%s", s.ip, s.port, service)
}

// XAddrs lists the HTTP and, when enabled, HTTPS device service addresses for WS-Discovery.
func (s *Server) XAddrs() string {
	out := s.XAddr("device_service")
	if s.httpsPort > 0 {
		out += fmt.Sprintf(" https://%s:%d/onvif/device_service", s.ip, s.httpsPort)
	}
	return out
}

// base returns scheme://ip:port matching how the request arrived, so a client
// that connected over HTTPS keeps getting HTTPS addresses back.
func (s *Server) base(r *request) string {
	if r != nil && r.HTTP != nil && r.HTTP.TLS != nil && s.httpsPort > 0 {
		return fmt.Sprintf("https://%s:%d", s.ip, s.httpsPort)
	}
	return fmt.Sprintf("http://%s:%d", s.ip, s.port)
}

func (s *Server) addr(r *request, service string) string { return s.base(r) + "/onvif/" + service }

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
			httpauth.Challenge(w)
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
	if !s.noAuth[req.Action] && !req.authenticated(s.users) && !httpauth.Authorized(r, s.users) {
		if !req.hasToken {
			// HTTP-level auth: answer with a fresh challenge (stale=true lets a Digest
			// client whose nonce expired retry without asking the user again)
			if httpauth.HasDigest(r) {
				httpauth.ChallengeStale(w)
			} else {
				httpauth.Challenge(w)
			}
			if r.Header.Get("Authorization") != "" {
				s.log.Warn("unauthorized", "action", req.Action, "from", r.RemoteAddr)
			}
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
	if !httpauth.Authorized(r, s.users) {
		httpauth.Challenge(w)
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
