package relay

import (
	"bytes"
	"crypto/sha256"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type metered struct {
	io.Reader
	read atomic.Int64
}

func (m *metered) Read(p []byte) (int, error) {
	n, e := m.Reader.Read(p)
	m.read.Add(int64(n))
	return n, e
}

type repeated struct{ remaining int }

func (r *repeated) Read(p []byte) (int, error) {
	n := min(len(p), r.remaining)
	if n == 0 {
		return 0, io.EOF
	}
	for i := range p[:n] {
		p[i] = byte((r.remaining - i) % 251)
	}
	r.remaining -= n
	return n, nil
}

func TestBoundedRelayProbe(t *testing.T) {
	const size = 8 << 20
	for _, id := range []string{"A-to-B", "B-to-A"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			src := &metered{Reader: &repeated{remaining: size}}
			r, w := io.Pipe()
			defer r.Close()
			defer w.Close()
			went := make(chan error, 1)
			go func() { _, e := io.CopyBuffer(w, src, make([]byte, 32<<10)); w.CloseWithError(e); went <- e }()
			first := make([]byte, 1024)
			if _, e := io.ReadFull(r, first); e != nil {
				t.Fatal(e)
			}
			time.Sleep(25 * time.Millisecond)
			if src.read.Load() > 32<<10 {
				t.Fatalf("unbounded read ahead %d", src.read.Load())
			}
			h := sha256.New()
			h.Write(first)
			n, e := io.Copy(h, r)
			if e != nil || n+int64(len(first)) != size {
				t.Fatalf("streamed %d: %v", n, e)
			}
			expected := sha256.New()
			io.Copy(expected, &repeated{remaining: size})
			if !bytes.Equal(h.Sum(nil), expected.Sum(nil)) {
				t.Fatal("digest mismatch")
			}
			if e := <-went; e != nil {
				t.Fatal(e)
			}
		})
	}
}
