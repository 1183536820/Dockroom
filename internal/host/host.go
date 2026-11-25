package host

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"dockroom/internal/api"
	"dockroom/internal/auth"
	"dockroom/internal/pki"
	"dockroom/internal/protocol"
	"dockroom/internal/store"
)

func AddUser(dir, user, password string, init bool, hostname string) error {
	if !protocol.ID(user) || len(password) < 12 || len(password) > 1024 {
		return errors.New("账号无效，或口令少于 12 字节")
	}
	if init {
		if e := os.Mkdir(dir, 0700); e != nil {
			return e
		}
		if e := pki.Init(dir, hostname); e != nil {
			return e
		}
	}
	db, e := store.Open(dir, init)
	if e != nil {
		return e
	}
	defer db.Close()
	_, e = db.Exec(`INSERT INTO users(id,password,admin) VALUES(?,?,?)`, user, auth.Password(password), init)
	return e
}

type Running struct {
	Browser               func(string) http.Handler
	RegisterLocalEndpoint func([]byte) ([]byte, error)
	Done                  chan error
	close                 func()
}

func (r *Running) Close() { r.close() }

func Start(dir, origin, browser, device string) (*Running, error) {
	u, e := url.Parse(origin)
	if e != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || origin != "https://"+u.Host {
		return nil, errors.New("origin must be an exact HTTPS origin")
	}
	x, e := net.Listen("tcp", browser)
	if e != nil {
		return nil, e
	}
	y, e := net.Listen("tcp", device)
	if e != nil {
		x.Close()
		return nil, e
	}
	ok := false
	defer func() {
		if !ok {
			x.Close()
			y.Close()
		}
	}()
	ca, e := pki.Load(dir)
	if e != nil {
		return nil, e
	}
	pair, e := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem"))
	if e != nil {
		return nil, e
	}
	cs := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{pair}}
	ds := cs.Clone()
	ds.ClientAuth = tls.RequireAndVerifyClientCert
	ds.ClientCAs = x509.NewCertPool()
	ds.ClientCAs.AddCert(ca.CA)
	db, e := store.Open(dir, false)
	if e != nil {
		return nil, e
	}
	h := api.New(db, ca, origin, y.Addr().(*net.TCPAddr).Port)
	makeServer := func(handler http.Handler, cfg *tls.Config) *http.Server {
		return &http.Server{Handler: handler, TLSConfig: cfg, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 18 * time.Second, WriteTimeout: 20 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16 << 10}
	}
	bs, as := makeServer(h.Browser(), cs), makeServer(h.Device(), ds)
	errs := make(chan error, 2)
	go func() { errs <- bs.ServeTLS(x, "", "") }()
	go func() { errs <- as.ServeTLS(y, "", "") }()
	ok = true
	return &Running{Browser: h.BrowserAt, RegisterLocalEndpoint: h.RegisterLocalEndpoint, Done: errs, close: func() {
		defer db.Close()
		h.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = bs.Shutdown(ctx)
		_ = as.Shutdown(ctx)
	}}, nil
}
