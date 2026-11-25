// Package relay implements the M2 data-plane primitive. It is not mounted on
// the Hub until a scheduler and durable destination acknowledgements exist.
package relay

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"dockroom/internal/auth"
	"dockroom/internal/protocol"
)

const MaxBlock = 8 << 20
const Buffer = 32 << 10
const Route = "/device/data/block"
const idle = 10 * time.Second

type Identity struct{ Node, Serial string }
type Block struct {
	Job, Item, File string
	Epoch           uint64
	Offset, Length  int64
}
type Permit struct {
	Block
	Source, Target Identity
	TTL            time.Duration
}
type Authorizer func(context.Context, Identity, Block) bool

type Relay struct {
	allow   Authorizer
	mu      sync.Mutex
	tickets map[string]ticket
}
type ticket struct {
	who      Identity
	method   string
	transfer *Transfer
	used     bool
}
type Transfer struct {
	SourceToken, TargetToken string
	ready                    chan struct{}
	delivery                 chan error
	pipeR                    *io.PipeReader
	pipeW                    *io.PipeWriter
	permit                   Permit
	relay                    *Relay
	ctx                      context.Context
	cancel                   context.CancelFunc
	hashes                   [2]string
	once                     sync.Once
}

func New(allow Authorizer) *Relay { return &Relay{allow: allow, tickets: make(map[string]ticket)} }
func (r *Relay) Reserve(p Permit) (*Transfer, error) {
	if r.allow == nil || !protocol.ID(p.Source.Node) || !protocol.ID(p.Target.Node) || p.Source.Serial == "" || p.Target.Serial == "" || p.Job == "" || p.Item == "" || p.File == "" || p.Epoch == 0 || p.Offset < 0 || p.Length < 1 || p.Length > MaxBlock || p.Offset > 1<<62 || p.TTL <= 0 || p.TTL > time.Minute {
		return nil, errors.New("invalid relay reservation")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tickets) >= 8 {
		return nil, errors.New("relay capacity exhausted")
	}
	for _, t := range r.tickets {
		for _, n := range []Identity{p.Source, p.Target} {
			if n.Node == t.who.Node {
				return nil, errors.New("node already has an active stream")
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.TTL)
	x := &Transfer{SourceToken: protocol.Token(), TargetToken: protocol.Token(), ready: make(chan struct{}), delivery: make(chan error, 1), permit: p, relay: r, ctx: ctx, cancel: cancel}
	x.pipeR, x.pipeW = io.Pipe()
	x.hashes = [2]string{auth.HashSecret(x.SourceToken), auth.HashSecret(x.TargetToken)}
	r.tickets[x.hashes[0]] = ticket{who: p.Source, method: "PUT", transfer: x}
	r.tickets[x.hashes[1]] = ticket{who: p.Target, method: "GET", transfer: x}
	context.AfterFunc(ctx, x.Stop)
	return x, nil
}
func (x *Transfer) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-x.ctx.Done():
		return x.ctx.Err()
	case <-x.ready:
		return x.ctx.Err()
	}
}
func (x *Transfer) Stop() {
	x.once.Do(func() {
		x.cancel()
		x.pipeR.CloseWithError(context.Canceled)
		x.pipeW.CloseWithError(context.Canceled)
		x.relay.mu.Lock()
		defer x.relay.mu.Unlock()
		for _, h := range x.hashes {
			delete(x.relay.tickets, h)
		}
	})
}
func (r *Relay) ServeHTTP(w http.ResponseWriter, q *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if q.URL.Path != Route || q.URL.RawQuery != "" {
		http.NotFound(w, q)
		return
	}
	if q.TLS == nil || len(q.TLS.VerifiedChains) == 0 || len(q.TLS.PeerCertificates) == 0 {
		w.WriteHeader(403)
		return
	}
	c := q.TLS.PeerCertificates[0]
	who := Identity{c.Subject.CommonName, c.SerialNumber.String()}
	if !time.Now().Before(c.NotAfter) {
		w.WriteHeader(403)
		return
	}
	proto, tok, ok := strings.Cut(q.Header.Get("Authorization"), " ")
	if !ok || proto != "Bearer" || len(tok) != 43 {
		w.WriteHeader(403)
		return
	}
	h := auth.HashSecret(tok)
	r.mu.Lock()
	t, ok := r.tickets[h]
	r.mu.Unlock()
	if !ok || t.used || t.who != who || t.method != q.Method || t.transfer.ctx.Err() != nil || !r.allow(q.Context(), who, t.transfer.permit.Block) {
		w.WriteHeader(403)
		return
	}
	x := t.transfer
	if q.Header.Get("Range") != "" || q.Header.Get("Content-Encoding") != "" || (q.Method == "PUT" && q.ContentLength != x.permit.Length) || (q.Method == "GET" && q.ContentLength != 0) {
		w.WriteHeader(400)
		return
	}
	if q.Method == "PUT" {
		select {
		case <-x.ready:
		default:
			w.WriteHeader(409)
			return
		}
	}
	r.mu.Lock()
	latest, ok := r.tickets[h]
	if !ok || latest.used || x.ctx.Err() != nil {
		r.mu.Unlock()
		w.WriteHeader(403)
		return
	}
	latest.used = true
	r.tickets[h] = latest
	r.mu.Unlock()
	stop := context.AfterFunc(q.Context(), x.Stop)
	defer stop()
	ctl := http.NewResponseController(w)
	deadline, _ := x.ctx.Deadline()
	if q.Method == "GET" {
		if ctl.SetWriteDeadline(deadline) != nil {
			x.Stop()
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(x.permit.Length, 10))
		w.WriteHeader(200)
		if ctl.Flush() != nil {
			x.Stop()
			return
		}
		close(x.ready)
		e := exact(x.pipeR, w, x.permit.Length, func() error { return ctl.SetWriteDeadline(minDeadline(deadline)) }, func() error { return ctl.Flush() })
		if e != nil {
			x.Stop()
			panic(http.ErrAbortHandler)
		}
		x.delivery <- nil
		return
	}
	defer x.Stop()
	if ctl.SetWriteDeadline(deadline) != nil {
		w.WriteHeader(503)
		return
	}
	if e := exact(q.Body, x.pipeW, x.permit.Length, func() error { return ctl.SetReadDeadline(minDeadline(deadline)) }, nil); e != nil {
		x.pipeW.CloseWithError(e)
		w.WriteHeader(422)
		return
	}
	x.pipeW.Close()
	select {
	case <-x.delivery:
		w.WriteHeader(204)
	case <-x.ctx.Done():
		w.WriteHeader(504)
	case <-q.Context().Done():
	}
}

func minDeadline(total time.Time) time.Time {
	d := time.Now().Add(idle)
	if total.Before(d) {
		return total
	}
	return d
}
func exact(src io.Reader, dst io.Writer, left int64, before, after func() error) error {
	buf := make([]byte, Buffer)
	for left > 0 {
		if e := before(); e != nil {
			return e
		}
		block := buf[:min(int64(len(buf)), left)]
		n, e := io.ReadFull(src, block)
		if e != nil {
			return e
		}
		written, e := dst.Write(block[:n])
		if e != nil {
			return e
		}
		if written != n {
			return io.ErrShortWrite
		}
		left -= int64(n)
		if after != nil {
			if e = after(); e != nil {
				return e
			}
		}
	}
	return nil
}
