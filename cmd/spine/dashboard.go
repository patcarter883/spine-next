package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"time"

	"github.com/patcarter883/spine-next/internal/auth"
	"github.com/patcarter883/spine-next/internal/notify"
	"github.com/patcarter883/spine-next/internal/supervisor"
	"github.com/patcarter883/spine-next/internal/ui"
)

// dashboardFlags are shared by `spine ui` and `spine supervisor`.
type dashboardFlags struct {
	readOnly bool
	tlsCert  string
	tlsKey   string
}

func (d *dashboardFlags) register(fs *flag.FlagSet) {
	fs.BoolVar(&d.readOnly, "read-only", false, "reject every change (approve, create, settings…), whoever asks")
	fs.StringVar(&d.tlsCert, "tls-cert", "", "serve HTTPS with this certificate (with --tls-key)")
	fs.StringVar(&d.tlsKey, "tls-key", "", "TLS private key")
}

func loopback(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// newDashboard builds the server with users, notifications and the audit
// log under the per-user home, and checks the address is safe to serve.
func newDashboard(src ui.Source, multi bool, admin ui.Admin, addr string, d dashboardFlags) (*ui.Server, error) {
	home := supervisor.Home()
	a := &auth.Authenticator{Store: auth.Store{Path: filepath.Join(home, "users.yaml")}, Secure: d.tlsCert != ""}
	if !loopback(addr) && !a.Enabled() {
		return nil, fmt.Errorf("%s is not a loopback address: add a user first (`spine users add NAME --role viewer|operator|admin`), so every request is authenticated", addr)
	}
	if (d.tlsCert == "") != (d.tlsKey == "") {
		return nil, errors.New("--tls-cert and --tls-key go together")
	}
	if !loopback(addr) && d.tlsCert == "" {
		logf("warning: serving %s over plain HTTP; tokens and session cookies can be read on the network (use --tls-cert/--tls-key or a TLS proxy)", addr)
	}
	return &ui.Server{Source: src, Multi: multi, Admin: admin, Auth: a, ReadOnly: d.readOnly, Home: home,
		Notifier: &notify.Notifier{Logf: logf}, Logf: logf}, nil
}

// serveDashboard runs srv until ctx ends.
func serveDashboard(ctx context.Context, s *ui.Server, addr string, d dashboardFlags, extra http.Handler) error {
	h := s.Handler()
	if extra != nil {
		h = extra
	}
	srv := &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: 10 * time.Second}
	go s.Start(ctx)
	go func() { <-ctx.Done(); srv.Close() }()
	scheme := "http"
	if d.tlsCert != "" {
		scheme = "https"
	}
	mode := ""
	if d.readOnly {
		mode = " (read-only)"
	}
	if s.Auth.Enabled() {
		mode += " (sign-in required)"
	}
	fmt.Printf("spine dashboard on %s://%s%s\n", scheme, addr, mode)
	var err error
	if d.tlsCert != "" {
		err = srv.ListenAndServeTLS(d.tlsCert, d.tlsKey)
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
