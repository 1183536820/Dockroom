package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dockroom/internal/agent/fs"
	"dockroom/internal/agent/journal"
	"dockroom/internal/pki"
	"dockroom/internal/protocol"
	"dockroom/internal/relay"
	"github.com/gorilla/websocket"
)

func tlsFor(dir string) (*tls.Config, error) {
	b, e := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if e != nil {
		return nil, e
	}
	p := x509.NewCertPool()
	if !p.AppendCertsFromPEM(b) {
		return nil, errors.New("invalid Hub CA")
	}
	return &tls.Config{RootCAs: p, MinVersion: tls.VersionTLS13}, nil
}
func endpoint(base, scheme, path string) (string, error) {
	u, e := url.Parse(base)
	if e != nil || u.Scheme != scheme || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return "", errors.New("invalid TLS Hub URL")
	}
	u.Path = path
	return u.String(), nil
}

func Enroll(dir string, c Config, code string) (string, error) {
	return EnrollContext(context.Background(), dir, c, code)
}

func EnrollContext(ctx context.Context, dir string, c Config, code string) (string, error) {
	if !protocol.ID(c.Node) || !protocol.ValidPairKey(code) {
		return "", errors.New("invalid enrollment parameters")
	}
	trust, e := tlsFor(dir)
	if e != nil {
		return "", e
	}
	csr, key, e := pki.CSR(c.Node)
	if e != nil {
		return "", e
	}
	path, field := "/device/enroll", "code"
	if c.ReusablePairing {
		path, field = "/device/join", "key"
	}
	issued, _, e := requestIdentity(ctx, c, trust, path, 201, map[string]string{"nodeId": c.Node, field: code, "csr": string(csr)}, key)
	if e != nil {
		return "", e
	}
	if e = pki.WriteNew(dir, "agent-key.pem", key); e != nil {
		return "", e
	}
	return issued.Fingerprint, pki.WriteNew(dir, "agent.pem", []byte(issued.Certificate))
}

type IdentityError struct{ Status int }

func (e *IdentityError) Error() string { return fmt.Sprintf("enrollment denied (HTTP %d)", e.Status) }

func requestIdentity(ctx context.Context, c Config, trust *tls.Config, path string, status int, fields any, key []byte) (protocol.Issued, *tls.Certificate, error) {
	var issued protocol.Issued
	u, e := endpoint(c.Hub, "https", path)
	if e != nil {
		return issued, nil, e
	}
	b, e := json.Marshal(fields)
	if e != nil {
		return issued, nil, e
	}
	serverOnly := &tls.Config{RootCAs: trust.RootCAs, MinVersion: tls.VersionTLS13}
	client := &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{TLSClientConfig: serverOnly}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	defer client.CloseIdleConnections()
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(b))
	if e != nil {
		return issued, nil, e
	}
	req.Header.Set("Content-Type", "application/json")
	r, e := client.Do(req)
	if e != nil {
		return issued, nil, e
	}
	defer r.Body.Close()
	if r.StatusCode != status {
		return issued, nil, &IdentityError{r.StatusCode}
	}
	if e = json.NewDecoder(io.LimitReader(r.Body, 16<<10)).Decode(&issued); e != nil {
		return issued, nil, e
	}
	cert, e := pki.Cert([]byte(issued.Certificate))
	if e != nil || cert.Subject.CommonName != c.Node || issued.Fingerprint != pki.Fingerprint(cert) {
		return issued, nil, errors.New("unexpected issued identity")
	}
	pair, e := tls.X509KeyPair([]byte(issued.Certificate), key)
	if e != nil {
		return issued, nil, e
	}
	if _, e = cert.Verify(x509.VerifyOptions{Roots: trust.RootCAs, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); e != nil {
		return issued, nil, e
	}
	pair.Leaf = cert
	return issued, &pair, nil
}

func renewIdentity(ctx context.Context, dir string, c Config, trust *tls.Config) (*tls.Certificate, error) {
	key, e := os.ReadFile(filepath.Join(dir, "agent-key.pem"))
	if e != nil {
		return nil, e
	}
	csr, e := pki.RenewalCSR(c.Node, key)
	if e != nil {
		return nil, e
	}
	issued, pair, e := requestIdentity(ctx, c, trust, "/device/renew", 200, map[string]string{"nodeId": c.Node, "csr": string(csr)}, key)
	if e != nil {
		return nil, e
	}
	tmp := filepath.Join(dir, "cert-"+protocol.Token()+".tmp")
	v, e := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return nil, e
	}
	defer os.Remove(tmp)
	_, e = v.WriteString(issued.Certificate)
	if e == nil {
		e = v.Sync()
	}
	closeErr := v.Close()
	if e != nil {
		return nil, e
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if e = os.Rename(tmp, filepath.Join(dir, "agent.pem")); e != nil {
		return nil, e
	}
	return pair, nil
}

func Run(ctx context.Context, dir string, c Config) error {
	return RunStatus(ctx, dir, c, func(string) {})
}

func RunStatus(ctx context.Context, dir string, c Config, status func(string)) error {
	b, e := NewBrowser(c)
	if e != nil {
		return e
	}
	trust, e := tlsFor(dir)
	if e != nil {
		return e
	}
	cert, e := tls.LoadX509KeyPair(filepath.Join(dir, "agent.pem"), filepath.Join(dir, "agent-key.pem"))
	if e != nil {
		return e
	}
	var activeCert atomic.Pointer[tls.Certificate]
	if cert.Leaf == nil {
		cert.Leaf, e = x509.ParseCertificate(cert.Certificate[0])
		if e != nil {
			return e
		}
	}
	activeCert.Store(&cert)
	trust.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) { return activeCert.Load(), nil }
	controlURL, e := endpoint(c.Control, "wss", "/device/control")
	if e != nil {
		return e
	}
	d := websocket.Dialer{TLSClientConfig: trust, HandshakeTimeout: 10 * time.Second}
	t := &transfer{url: "https://" + strings.TrimPrefix(strings.TrimSuffix(controlURL, "/device/control"), "wss://") + relay.Route,
		client: &http.Client{Transport: &http.Transport{TLSClientConfig: trust, MaxIdleConnsPerHost: 2, DisableCompression: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	defer t.client.CloseIdleConnections()
	hello := protocol.Hello{Major: protocol.Major, Minor: protocol.Minor, Node: c.Node, OS: runtime.GOOS, Version: "m2-dev", Volume: map[string]string{"windows": "ntfs", "linux": "openat2"}[runtime.GOOS], Roots: []protocol.AdvertisedRoot{}}
	for _, r := range c.Roots {
		sum := sha256.Sum256([]byte(r.Identity))
		write := false
		if fs.CanWrite && r.Mode == "readWrite" && r.PrivateID != "" {
			if a, e := r.Root.WriteArea(nil, r.PrivateID); e == nil {
				a.Close()
				write = true
			}
			if write && t.journal == nil {
				t.journal, e = journal.Open(dir)
				if e != nil {
					return e
				}
				defer t.journal.Close()
			}
		}
		hello.Roots = append(hello.Roots, protocol.AdvertisedRoot{Root: protocol.Root{ID: r.ID, Label: r.Label, Mode: "readOnly", CanWrite: write}, Binding: fmt.Sprintf("%x", sum)})
	}
	delay := time.Second
	for ctx.Err() == nil {
		started := time.Now()
		if c.ReusablePairing && time.Until(activeCert.Load().Leaf.NotAfter) < 7*24*time.Hour {
			if fresh, e := renewIdentity(ctx, dir, c, trust); e == nil {
				activeCert.Store(fresh)
				t.client.CloseIdleConnections()
			}
		}
		ws, res, e := d.DialContext(ctx, controlURL, nil)
		if e == nil {
			status("online")
			control(ctx, ws, hello, b, t)
			status("offline")
			if time.Since(started) > 45*time.Second {
				delay = time.Second
			}
		} else if res != nil {
			if res.StatusCode == 403 {
				if c.ReusablePairing {
					status("denied")
				} else {
					status("approval")
					delay = time.Second
				}
			} else {
				status("offline")
			}
			res.Body.Close()
		} else {
			status("offline")
		}
		wait := time.NewTimer(delay + time.Duration(rand.Int64N(int64(delay/2))))
		select {
		case <-ctx.Done():
			wait.Stop()
		case <-wait.C:
		}
		delay = min(delay*2, 40*time.Second)
	}
	return nil
}
func control(ctx context.Context, ws *websocket.Conn, h protocol.Hello, b *Browser, t *transfer) {
	ctx, cancel := context.WithCancel(ctx)
	var wg sync.WaitGroup
	var write sync.Mutex
	defer func() { cancel(); ws.Close(); wg.Wait() }()
	defer ws.Close()
	stop := context.AfterFunc(ctx, func() { ws.Close() })
	defer stop()
	ws.SetReadLimit(32 << 10)
	ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if ws.WriteJSON(h) != nil {
		return
	}
	ws.SetPingHandler(func(data string) error {
		ws.SetReadDeadline(time.Now().Add(45 * time.Second))
		return ws.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(5*time.Second))
	})
	var epoch uint64
	slots := make(chan struct{}, 4)
	ws.SetReadDeadline(time.Now().Add(45 * time.Second))
	for {
		var cmd protocol.Command
		if ws.ReadJSON(&cmd) != nil {
			return
		}
		now := time.Now()
		if cmd.Protocol != protocol.Major || cmd.Node != h.Node || cmd.Payload.Ref.NodeID != h.Node || cmd.ID == "" || cmd.Attempt != 1 || cmd.Epoch == 0 || (epoch != 0 && epoch != cmd.Epoch) || cmd.Deadline.Before(now) || cmd.Deadline.After(now.Add(time.Minute)) {
			return
		}
		epoch = cmd.Epoch
		select {
		case slots <- struct{}{}:
		default:
			return
		}
		wg.Go(func() {
			defer func() { <-slots }()
			defer func() {
				if recover() != nil {
					ws.Close()
				}
			}()
			work, stop := context.WithDeadline(ctx, cmd.Deadline)
			defer stop()
			var v any
			var f *protocol.Failure
			if cmd.Payload.Copy != nil {
				v, f = t.run(work, b, cmd)
			} else {
				v, f = b.Execute(cmd)
			}
			r := protocol.Response{ID: cmd.ID, Error: f}
			if f == nil {
				r.Value, _ = json.Marshal(v)
			}
			write.Lock()
			defer write.Unlock()
			ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if ws.WriteJSON(r) != nil {
				ws.Close()
			}
		})
	}
}
