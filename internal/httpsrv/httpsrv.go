// Package httpsrv runs one handler on an HTTP listener and, optionally, an HTTPS
// listener. Shared by the ONVIF services and the admin page.
package httpsrv

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"
)

type Dual struct {
	log   *slog.Logger
	name  string
	http  *http.Server
	https *http.Server // nil when HTTPS is off
}

// New prepares the servers. httpsPort <= 0 or a nil tlsCfg disables HTTPS.
func New(log *slog.Logger, name, listenIP string, httpPort, httpsPort int, h http.Handler, tlsCfg *tls.Config) *Dual {
	d := &Dual{log: log, name: name}
	d.http = &http.Server{Addr: net.JoinHostPort(listenIP, strconv.Itoa(httpPort)), Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	if tlsCfg != nil && httpsPort > 0 {
		d.https = &http.Server{Addr: net.JoinHostPort(listenIP, strconv.Itoa(httpsPort)), Handler: h, TLSConfig: tlsCfg, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	}
	return d
}

// Start binds both listeners before serving, so a bind failure on either port
// leaves nothing running and is returned to the caller.
func (d *Dual) Start() error {
	ln, err := net.Listen("tcp", d.http.Addr)
	if err != nil {
		return err
	}
	var tln net.Listener
	if d.https != nil {
		tln, err = net.Listen("tcp", d.https.Addr)
		if err != nil {
			ln.Close()
			return err
		}
		tln = tls.NewListener(tln, d.https.TLSConfig)
	}
	go d.serve(d.http, ln, " http")
	if tln != nil {
		go d.serve(d.https, tln, " https")
	}
	return nil
}

func (d *Dual) serve(srv *http.Server, ln net.Listener, kind string) {
	if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		d.log.Error(d.name+kind, "err", err)
	}
}

func (d *Dual) Close() {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = d.http.Shutdown(ctx)
	if d.https != nil {
		_ = d.https.Shutdown(ctx)
	}
}
