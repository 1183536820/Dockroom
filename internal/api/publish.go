package api

import (
	"dockroom/internal/auth"
	"dockroom/internal/protocol"
	"net/http"
	"strings"
	"time"
)

type publishTicket struct {
	job   string
	lease uint64
	peer  *peer
	until time.Time
}

func (h *Hub) makePublishTicket(j *job, p *peer) string {
	t := protocol.Token()
	h.publishMu.Lock()
	defer h.publishMu.Unlock()
	h.publishTickets[auth.HashSecret(t)] = publishTicket{j.ID, j.Lease, p, time.Now().Add(45 * time.Second)}
	return t
}
func (h *Hub) clearPublishTicket(t string) {
	h.publishMu.Lock()
	defer h.publishMu.Unlock()
	delete(h.publishTickets, auth.HashSecret(t))
}

// A one-shot authorization immediately before rename, not before hashing.
// Cancellation after authorization races with the actual endpoint commit.
func (h *Hub) authorizePublish(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.ContentLength != 0 || r.URL.RawQuery != "" || r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		w.WriteHeader(403)
		return
	}
	method, t, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || method != "Bearer" || !token(t) {
		w.WriteHeader(403)
		return
	}
	b := r.TLS.PeerCertificates[0]
	k := auth.HashSecret(t)
	h.publishMu.Lock()
	defer h.publishMu.Unlock()
	p, ok := h.publishTickets[k]
	if !ok || p.peer.serial != b.SerialNumber.String() || time.Now().After(p.until) || time.Now().After(b.NotAfter) {
		w.WriteHeader(403)
		return
	}
	j, e := readJob(h.db.QueryRowContext(r.Context(), `SELECT `+jobColumns+` FROM copy_jobs WHERE id=?`, p.job))
	if e != nil || j.State != "committing" || !j.Commit || j.Lease != p.lease || j.Stopped || b.Subject.CommonName != j.Plan.Input.Dest.NodeID || h.current(b.Subject.CommonName) != p.peer || !h.allowed(r.Context(), j.Owner, j.Plan) {
		w.WriteHeader(403)
		return
	}
	delete(h.publishTickets, k)
	w.WriteHeader(204)
}
