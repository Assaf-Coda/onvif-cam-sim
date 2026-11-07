// Package rtsp runs one RTSP server per camera: ffmpeg publishes into it from
// loopback, VMS clients read from it with Basic/Digest authentication.
package rtsp

import (
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/bluenviron/gortsplib/v5"
	"github.com/bluenviron/gortsplib/v5/pkg/base"
	"github.com/bluenviron/gortsplib/v5/pkg/description"
	"github.com/bluenviron/gortsplib/v5/pkg/format"
	"github.com/bluenviron/gortsplib/v5/pkg/liberrors"
	"github.com/pion/rtp"

	"github.com/Assaf-Coda/onvif-cam-sim/internal/config"
)

const Path = "stream"

type Server struct {
	log   *slog.Logger
	users []config.User
	srv   *gortsplib.Server

	mu        sync.RWMutex
	stream    *gortsplib.ServerStream
	publisher *gortsplib.ServerSession
	readers   int
}

// New creates a server bound to listenIP:port. UDP RTP/RTCP use ports 2*port and 2*port+1,
// which cannot collide across cameras as long as RTSP ports differ.
func New(log *slog.Logger, listenIP string, port int, users []config.User) *Server {
	s := &Server{log: log, users: users}
	s.srv = &gortsplib.Server{
		Handler:        s,
		RTSPAddress:    fmt.Sprintf("%s:%d", listenIP, port),
		UDPRTPAddress:  fmt.Sprintf("%s:%d", listenIP, 2*port),
		UDPRTCPAddress: fmt.Sprintf("%s:%d", listenIP, 2*port+1),
		ReadTimeout:    10 * time.Second,
		WriteTimeout:   10 * time.Second,
	}
	return s
}

func (s *Server) Start() error { return s.srv.Start() }
func (s *Server) Close()       { s.srv.Close() }

// Live reports whether a publisher is currently connected.
func (s *Server) Live() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.stream != nil
}

// Readers returns the number of connected playing sessions.
func (s *Server) Readers() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.readers
}

func (s *Server) OnConnOpen(ctx *gortsplib.ServerHandlerOnConnOpenCtx) {
	s.log.Debug("rtsp conn open", "remote", ctx.Conn.NetConn().RemoteAddr())
}

func (s *Server) OnConnClose(ctx *gortsplib.ServerHandlerOnConnCloseCtx) {
	s.log.Debug("rtsp conn close", "remote", ctx.Conn.NetConn().RemoteAddr(), "err", ctx.Error)
}

func (s *Server) OnSessionOpen(_ *gortsplib.ServerHandlerOnSessionOpenCtx) {}

func (s *Server) OnSessionClose(ctx *gortsplib.ServerHandlerOnSessionCloseCtx) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil && ctx.Session == s.publisher {
		s.log.Info("publisher disconnected")
		s.stream.Close()
		s.stream = nil
		s.publisher = nil
	} else if ctx.Session.State() == gortsplib.ServerSessionStatePlay {
		s.readers--
	}
}

func (s *Server) authorized(conn *gortsplib.ServerConn, req *base.Request) bool {
	for _, u := range s.users {
		if conn.VerifyCredentials(req, u.Name, u.Password) {
			return true
		}
	}
	return false
}

func (s *Server) OnDescribe(ctx *gortsplib.ServerHandlerOnDescribeCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if !s.authorized(ctx.Conn, ctx.Request) {
		return &base.Response{StatusCode: base.StatusUnauthorized}, nil, liberrors.ErrServerAuth{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *Server) OnAnnounce(ctx *gortsplib.ServerHandlerOnAnnounceCtx) (*base.Response, error) {
	host, _, _ := net.SplitHostPort(ctx.Conn.NetConn().RemoteAddr().String())
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return &base.Response{StatusCode: base.StatusForbidden}, fmt.Errorf("publish only allowed from loopback")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stream != nil {
		s.stream.Close()
		s.publisher.Close()
	}
	st := &gortsplib.ServerStream{Server: s.srv, Desc: ctx.Description}
	if err := st.Initialize(); err != nil {
		return &base.Response{StatusCode: base.StatusBadRequest}, err
	}
	s.stream = st
	s.publisher = ctx.Session
	s.log.Info("publisher connected", "formats", describe(ctx.Description))
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (s *Server) OnSetup(ctx *gortsplib.ServerHandlerOnSetupCtx) (*base.Response, *gortsplib.ServerStream, error) {
	if ctx.Session.State() == gortsplib.ServerSessionStatePreRecord {
		return &base.Response{StatusCode: base.StatusOK}, nil, nil
	}
	if !s.authorized(ctx.Conn, ctx.Request) {
		return &base.Response{StatusCode: base.StatusUnauthorized}, nil, liberrors.ErrServerAuth{}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.stream == nil {
		return &base.Response{StatusCode: base.StatusNotFound}, nil, nil
	}
	return &base.Response{StatusCode: base.StatusOK}, s.stream, nil
}

func (s *Server) OnPlay(ctx *gortsplib.ServerHandlerOnPlayCtx) (*base.Response, error) {
	s.mu.Lock()
	s.readers++
	s.mu.Unlock()
	s.log.Info("reader playing", "remote", ctx.Conn.NetConn().RemoteAddr())
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func (s *Server) OnRecord(ctx *gortsplib.ServerHandlerOnRecordCtx) (*base.Response, error) {
	ctx.Session.OnPacketRTPAny(func(medi *description.Media, _ format.Format, pkt *rtp.Packet) {
		s.mu.RLock()
		st := s.stream
		s.mu.RUnlock()
		if st != nil {
			_ = st.WritePacketRTP(medi, pkt)
		}
	})
	return &base.Response{StatusCode: base.StatusOK}, nil
}

func describe(d *description.Session) string {
	out := ""
	for _, m := range d.Medias {
		for _, f := range m.Formats {
			out += f.Codec() + " "
		}
	}
	return out
}
