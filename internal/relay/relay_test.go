package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"dockroom/internal/pki"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func TestMTLSRoleLengthAndDigest(t *testing.T) {
	dir := t.TempDir()
	must(t, pki.Init(dir, "127.0.0.1"))
	ca, e := pki.Load(dir)
	must(t, e)
	pool := x509.NewCertPool()
	pool.AddCert(ca.CA)
	pair, e := tls.LoadX509KeyPair(filepath.Join(dir, "server.pem"), filepath.Join(dir, "server-key.pem"))
	must(t, e)
	client := func(node string) (Identity, *http.Client) {
		t.Helper()
		csr, key, e := pki.CSR(node)
		must(t, e)
		pem, c, e := ca.Issue(node, csr)
		must(t, e)
		pair, e := tls.X509KeyPair(pem, key)
		must(t, e)
		tr := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS13}}
		t.Cleanup(tr.CloseIdleConnections)
		return Identity{node, c.SerialNumber.String()}, &http.Client{Transport: tr, Timeout: 5 * time.Second}
	}
	s, src := client("studio")
	d, dst := client("archive")
	const length = 1<<20 + 31
	p := Permit{Block: Block{Job: "j1", Item: "i1", File: "f1", Offset: 8 << 20, Length: length, Epoch: 7}, Source: s, Target: d, TTL: 5 * time.Second}
	r := New(func(_ context.Context, who Identity, b Block) bool { return b == p.Block && (who == s || who == d) })
	srv := httptest.NewUnstartedServer(r)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{pair}, ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS13}
	srv.StartTLS()
	defer srv.Close()
	x, e := r.Reserve(p)
	must(t, e)
	defer x.Stop()
	request := func(c *http.Client, method, token string, body io.Reader) (*http.Response, error) {
		q, e := http.NewRequest(method, srv.URL+Route, body)
		if e != nil {
			return nil, e
		}
		q.Header.Set("Authorization", "Bearer "+token)
		return c.Do(q)
	}
	check := func(c *http.Client, method, token string, body io.Reader, status int) {
		t.Helper()
		a, e := request(c, method, token, body)
		must(t, e)
		a.Body.Close()
		if a.StatusCode != status {
			t.Fatalf("%s wanted %d got %d", method, status, a.StatusCode)
		}
	}
	input := bytes.Repeat([]byte("Z"), length)
	check(src, "PUT", x.SourceToken, bytes.NewReader(input), 409)
	check(src, "GET", x.TargetToken, nil, 403)
	check(dst, "PUT", x.SourceToken, bytes.NewReader(input), 403)
	check(src, "PUT", x.SourceToken, bytes.NewReader(input[:2]), 400)
	check(dst, "GET", x.SourceToken, nil, 403)
	res, e := request(dst, "GET", x.TargetToken, nil)
	must(t, e)
	defer res.Body.Close()
	if res.StatusCode != 200 || res.ContentLength != length {
		t.Fatalf("bad GET: %d %d", res.StatusCode, res.ContentLength)
	}
	must(t, x.WaitReady(context.Background()))
	check(dst, "GET", x.TargetToken, nil, 403)
	got := make(chan []byte, 1)
	go func() {
		h := sha256.New()
		n, e := io.Copy(h, res.Body)
		if e == nil && n == length {
			got <- h.Sum(nil)
			return
		}
		got <- nil
	}()
	check(src, "PUT", x.SourceToken, bytes.NewReader(input), 204)
	sum := sha256.Sum256(input)
	if !bytes.Equal(<-got, sum[:]) {
		t.Fatal("TLS reassembly SHA-256 mismatch")
	}
	check(src, "PUT", x.SourceToken, bytes.NewReader(input), 403)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS13}}
	defer transport.CloseIdleConnections()
	if a, e := request(&http.Client{Transport: transport}, "GET", x.TargetToken, nil); e == nil {
		a.Body.Close()
		t.Fatal("uncredentialed TLS connection accepted")
	}

	x.Stop()
	y, e := r.Reserve(p)
	must(t, e)
	defer y.Stop()
	truncated, e := request(dst, "GET", y.TargetToken, nil)
	must(t, e)
	defer truncated.Body.Close()
	must(t, y.WaitReady(context.Background()))
	put, e := http.NewRequest("PUT", srv.URL+Route, io.LimitReader(bytes.NewReader(input), Buffer+7))
	must(t, e)
	put.ContentLength = length
	put.Header.Set("Authorization", "Bearer "+y.SourceToken)
	cut, broken := src.Do(put)
	if broken == nil {
		cut.Body.Close()
		if cut.StatusCode == 204 {
			t.Fatal("truncated source was acknowledged")
		}
	}
	n, broken := io.Copy(io.Discard, truncated.Body)
	if broken == nil || n >= length {
		t.Fatalf("receiver did not detect incomplete delivery: %d %v", n, broken)
	}
}

func TestReservationCancellationCapacityAndExpiry(t *testing.T) {
	r := New(func(context.Context, Identity, Block) bool { return true })
	var live []*Transfer
	for i := range 4 {
		suffix := string(rune('a' + i))
		p := Permit{Block: Block{Job: "j", Item: "i", File: "f", Length: 1, Epoch: 1}, Source: Identity{"s" + suffix, "1"}, Target: Identity{"t" + suffix, "2"}, TTL: time.Second}
		x, e := r.Reserve(p)
		must(t, e)
		live = append(live, x)
		if _, e = r.Reserve(p); e == nil {
			t.Fatal("overbooked same endpoints")
		}
	}
	over := Permit{Block: Block{Job: "j", Item: "i", File: "f", Length: 1, Epoch: 1}, Source: Identity{"source", "1"}, Target: Identity{"target", "2"}, TTL: time.Second}
	if _, e := r.Reserve(over); e == nil {
		t.Fatal("overbooked Hub capacity")
	}
	live[0].Stop()
	over.TTL = 10 * time.Millisecond
	x, e := r.Reserve(over)
	must(t, e)
	if e = x.WaitReady(context.Background()); e == nil {
		t.Fatal("no target readiness but reservation succeeded")
	}
	x.Stop()
	for _, active := range live {
		active.Stop()
	}
}
