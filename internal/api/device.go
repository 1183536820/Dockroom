package api

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"net/http"
	"sync"
	"time"

	"dockroom/internal/auth"
	"dockroom/internal/pki"
	"dockroom/internal/protocol"
	"dockroom/internal/relay"
	"github.com/gorilla/websocket"
)

type peer struct {
	ws     *websocket.Conn
	epoch  uint64
	done   chan struct{}
	slot   chan struct{}
	mu     sync.Mutex
	serial string
	minor  int
	answer map[string]chan protocol.Response
}

func (h *Hub) current(id string) *peer { h.mu.Lock(); defer h.mu.Unlock(); return h.peers[id] }
func (h *Hub) Close() {
	h.stop()
	h.mu.Lock()
	for _, p := range h.peers {
		p.ws.Close()
	}
	h.mu.Unlock()
	h.worker.Wait()
}
func (p *peer) ask(ctx context.Context, c protocol.Command) (protocol.Response, error) {
	done, stop := context.WithDeadline(ctx, c.Deadline)
	defer stop()
	select {
	case p.slot <- struct{}{}:
	case <-p.done:
		return protocol.Response{}, errors.New("offline")
	case <-done.Done():
		return protocol.Response{}, done.Err()
	}
	r := make(chan protocol.Response, 1)
	p.mu.Lock()
	p.answer[c.ID] = r
	p.mu.Unlock()
	defer func() { p.mu.Lock(); delete(p.answer, c.ID); p.mu.Unlock() }()
	p.ws.SetWriteDeadline(c.Deadline)
	e := p.ws.WriteJSON(c)
	<-p.slot
	if e != nil {
		p.ws.Close()
		return protocol.Response{}, e
	}
	select {
	case a := <-r:
		return a, nil
	case <-done.Done():
		return protocol.Response{}, done.Err()
	case <-p.done:
		return protocol.Response{}, errors.New("offline")
	}
}

func (h *Hub) Device() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("GET /device/control", h.connect)
	m.HandleFunc("POST /device/workbench", h.deviceWorkbench)
	m.HandleFunc("GET /device/disk-file", h.diskFile)
	m.HandleFunc("PUT /device/disk-file", h.diskFile)
	m.Handle("/workspace/", h.workspaceHandler())
	m.Handle(relay.Route, h.relay)
	m.HandleFunc("POST /device/data/publish-permit", h.authorizePublish)
	return m
}
func (h *Hub) connect(w http.ResponseWriter, r *http.Request) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		reject(w, 403, "CERT_REQUIRED", "需要设备证书")
		return
	}
	cert := r.TLS.PeerCertificates[0]
	id, serial := cert.Subject.CommonName, cert.SerialNumber.String()
	approved := func() bool {
		var state string
		return h.db.QueryRow(`SELECT state FROM nodes WHERE id=? AND serial=?`, id, serial).Scan(&state) == nil && state == "approved"
	}
	if !approved() {
		reject(w, 403, "DEVICE_NOT_APPROVED", "设备未批准或已撤销")
		return
	}
	u := websocket.Upgrader{HandshakeTimeout: 5 * time.Second, CheckOrigin: func(r *http.Request) bool { return r.Header.Get("Origin") == "" }}
	ws, e := u.Upgrade(w, r, nil)
	if e != nil {
		return
	}
	defer ws.Close()
	ws.SetReadLimit(1 << 20)
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	var hello protocol.Hello
	if ws.ReadJSON(&hello) != nil || hello.Node != id || hello.Major != protocol.Major || hello.Minor < 0 || len(hello.Roots) > 32 || len(hello.Version) > 50 || !((hello.OS == "windows" && hello.Volume == "ntfs") || (hello.OS == "linux" && hello.Volume == "openat2")) {
		return
	}
	seen := map[string]bool{}
	for _, root := range hello.Roots {
		binding, err := hex.DecodeString(root.Binding)
		if !protocol.ID(root.ID) || seen[root.ID] || root.Label == "" || len(root.Label) > 100 || root.Mode != "readOnly" || err != nil || len(binding) != 32 {
			return
		}
		seen[root.ID] = true
	}
	p := &peer{ws: ws, epoch: epoch(), done: make(chan struct{}), slot: make(chan struct{}, 1), serial: serial, minor: hello.Minor, answer: map[string]chan protocol.Response{}}
	if !h.attach(id, serial, hello, p) {
		return
	}
	defer func() {
		close(p.done)
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.peers[id] == p {
			delete(h.peers, id)
			h.db.Exec(`UPDATE nodes SET last_seen=? WHERE id=? AND serial=?`, time.Now().Unix(), id, serial)
		}
	}()
	now := time.Now()
	ws.SetReadDeadline(minTime(now.Add(45*time.Second), cert.NotAfter))
	ws.SetPongHandler(func(_ string) error {
		if !approved() {
			return errors.New("not approved")
		}
		h.db.Exec(`UPDATE nodes SET last_seen=? WHERE id=? AND serial=?`, time.Now().Unix(), id, serial)
		return ws.SetReadDeadline(minTime(time.Now().Add(45*time.Second), cert.NotAfter))
	})
	go func() {
		t := time.NewTicker(15 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-p.done:
				return
			case <-t.C:
				if ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second)) != nil {
					ws.Close()
					return
				}
			}
		}
	}()
	for {
		var a protocol.Response
		if ws.ReadJSON(&a) != nil {
			return
		}
		p.mu.Lock()
		if ch := p.answer[a.ID]; ch != nil {
			select {
			case ch <- a:
			default:
			}
		}
		p.mu.Unlock()
	}
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func (h *Hub) attach(id, serial string, hello protocol.Hello, p *peer) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	tx, e := h.db.Begin()
	if e != nil {
		return false
	}
	defer tx.Rollback()
	res, e := tx.Exec(`UPDATE nodes SET os=?,version=?,last_seen=? WHERE id=? AND serial=? AND state='approved'`, hello.OS, hello.Version, time.Now().Unix(), id, serial)
	if e != nil {
		return false
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		return false
	}
	if _, e = tx.Exec(`UPDATE roots SET available=0 WHERE node=?`, id); e != nil {
		return false
	}
	for _, r := range hello.Roots {
		// Never reactivate grants for a physical root replaced under the old ID.
		if _, e = tx.Exec(`INSERT INTO roots(node,id,label,available,binding) VALUES(?,?,?,1,?) ON CONFLICT(node,id) DO UPDATE SET label=excluded.label,available=1 WHERE roots.binding=excluded.binding`, id, r.ID, r.Label, r.Binding); e != nil {
			return false
		}
		if _, e = tx.Exec(`UPDATE roots SET copy_write=? WHERE node=? AND id=? AND binding=?`, r.CanWrite && hello.OS == "windows" && hello.Minor >= 1, id, r.ID, r.Binding); e != nil {
			return false
		}
	}
	// Desktop clients use fresh root IDs for explicit additions. Install only once.
	if _, e = tx.Exec(`INSERT OR IGNORE INTO grants(owner,node,root,copy_write)
		SELECT p.owner,r.node,r.id,r.copy_write FROM paired_devices p JOIN roots r ON r.node=p.node JOIN users u ON u.id=p.owner
		WHERE p.node=? AND (r.id='files' OR r.id LIKE 'folder-%') AND r.available=1 AND u.disabled=0 AND u.admin=1
		AND NOT EXISTS(SELECT 1 FROM paired_root_once k WHERE k.node=r.node AND k.root=r.id)`, id); e != nil {
		return false
	}
	if _, e = tx.Exec(`INSERT OR IGNORE INTO paired_root_once SELECT r.node,r.id FROM roots r JOIN paired_devices p ON p.node=r.node WHERE r.node=? AND r.available=1 AND (r.id='files' OR r.id LIKE 'folder-%')`, id); e != nil {
		return false
	}
	if _, e = tx.Exec(`UPDATE paired_devices SET auto_grant=0 WHERE node=? AND auto_grant=1`, id); e != nil {
		return false
	}
	if e = syncDeviceGrants(tx); e != nil {
		return false
	}
	if tx.Commit() != nil {
		return false
	}
	if old := h.peers[id]; old != nil {
		old.ws.Close()
	}
	h.peers[id] = p
	return true
}

func (h *Hub) enroll(w http.ResponseWriter, r *http.Request) {
	var b protocol.Enrollment
	if !decode(w, r, &b) {
		return
	}
	if !protocol.ID(b.Node) || len(b.Code) != 43 || len(b.CSR) > 8192 {
		reject(w, 422, "INVALID_ENROLLMENT", "接入请求无效")
		return
	}
	tx, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	defer tx.Rollback()
	now := time.Now().Unix()
	res, e := tx.Exec(`UPDATE enrollments SET used=1 WHERE hash=? AND node=? AND used=0 AND expires>?`, auth.HashSecret(b.Code), b.Node, now)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	n, e := res.RowsAffected()
	if e != nil || n != 1 {
		reject(w, 403, "ENROLLMENT_DENIED", "接入码无效或过期")
		return
	}
	bundle, cert, e := h.ca.Issue(b.Node, []byte(b.CSR))
	if e != nil {
		reject(w, 422, "INVALID_CSR", "证书请求无效")
		return
	}
	fp := pki.Fingerprint(cert)
	res, e = tx.Exec(`INSERT INTO nodes(id,serial,fingerprint,state) VALUES(?,?,?,'pending') ON CONFLICT(id) DO UPDATE SET serial=excluded.serial,fingerprint=excluded.fingerprint,state='pending' WHERE nodes.state='revoked'`, b.Node, cert.SerialNumber.String(), fp)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	n, e = res.RowsAffected()
	if e != nil || n != 1 {
		reject(w, 409, "DEVICE_EXISTS", "设备已经接入或待批准")
		return
	}
	if _, e = tx.Exec(`DELETE FROM paired_devices WHERE node=?`, b.Node); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	if e = audit(tx, b.Node, "enroll", b.Node); e != nil || tx.Commit() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	out(w, 201, protocol.Issued{Certificate: string(bundle), Fingerprint: fp})
}
func audit(tx *sql.Tx, actor, action, ref string) error {
	_, e := tx.Exec(`INSERT INTO audit(utc,actor,action,ref,result) VALUES(?,?,?,?,'ok')`, time.Now().Unix(), actor, action, ref)
	return e
}
