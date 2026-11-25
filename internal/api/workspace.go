package api

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dockroom/internal/auth"
	"dockroom/internal/pki"
	"dockroom/internal/protocol"
)

const HostEndpoint = "dockroom-host"

// RegisterLocalEndpoint is called only inside the desktop host, never by HTTP.
// It creates a new identity, or verifies a previous attempt used the same key.
func (h *Hub) RegisterLocalEndpoint(csr []byte) ([]byte, error) {
	key, e := pki.CSRKey(HostEndpoint, csr)
	if e != nil {
		return nil, e
	}
	hash := auth.HashSecret(string(key))
	tx, e := h.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var known string
	var saved []byte
	e = tx.QueryRow(`SELECT public_key,cert FROM paired_devices WHERE node=?`, HostEndpoint).Scan(&known, &saved)
	if e == nil {
		if known != hash {
			return nil, fmt.Errorf("本机收件夹已绑定另一设备身份")
		}
		return saved, nil
	}
	if e != sql.ErrNoRows {
		return nil, e
	}
	pem, cert, e := h.ca.Issue(HostEndpoint, csr)
	if e != nil {
		return nil, e
	}
	if _, e = tx.Exec(`INSERT INTO nodes(id,serial,fingerprint,state) VALUES(?,?,?,'approved')`, HostEndpoint, cert.SerialNumber.String(), pki.Fingerprint(cert)); e != nil {
		return nil, e
	}
	if _, e = tx.Exec(`INSERT INTO paired_devices(node,owner,public_key,cert,renew_at) VALUES(?,'admin',?,?,?)`, HostEndpoint, hash, pem, cert.NotAfter.Add(-7*24*time.Hour).Unix()); e != nil {
		return nil, e
	}
	return pem, tx.Commit()
}

// Device operators inherit only their own and the reserved local host endpoint's
// grants, and no grant broader than the corresponding desktop owner's grant.
func syncDeviceGrants(t *sql.Tx) error {
	if _, e := t.Exec(`DELETE FROM grants WHERE owner IN (SELECT owner FROM device_operators)`); e != nil {
		return e
	}
	_, e := t.Exec(`INSERT INTO grants(owner,node,root,copy_write)
 SELECT d.owner,g.node,g.root,g.copy_write FROM device_operators d
 JOIN paired_devices p ON p.node=d.node JOIN nodes n ON n.id=p.node AND n.state='approved'
 JOIN users a ON a.id=p.owner AND a.disabled=0 AND a.admin=1
 JOIN grants g ON g.owner=p.owner AND (g.node=d.node OR g.node=?)
 JOIN nodes target ON target.id=g.node AND target.state='approved'`, HostEndpoint)
	return e
}

func (h *Hub) workspaceNode(r *http.Request) (string, error) {
	if r.TLS == nil || len(r.TLS.VerifiedChains) == 0 || len(r.TLS.PeerCertificates) == 0 {
		return "", sql.ErrNoRows
	}
	leaf := r.TLS.PeerCertificates[0]
	if time.Now().After(leaf.NotAfter) {
		return "", sql.ErrNoRows
	}
	var node string
	e := h.db.QueryRowContext(r.Context(), `SELECT n.id FROM nodes n JOIN paired_devices p ON p.node=n.id JOIN users a ON a.id=p.owner WHERE n.id=? AND n.serial=? AND n.state='approved' AND a.admin=1 AND a.disabled=0`, leaf.Subject.CommonName, leaf.SerialNumber.String()).Scan(&node)
	return node, e
}

func (h *Hub) deviceWorkbench(w http.ResponseWriter, r *http.Request) {
	node, e := h.workspaceNode(r)
	if e != nil || node == HostEndpoint || r.Header.Get("Origin") != "" || r.ContentLength != 0 {
		reject(w, 403, "NO_DEVICE_WORKBENCH", "此设备无法打开双栏")
		return
	}
	if !h.throttle("workspace:" + node) {
		reject(w, 429, "RATE_LIMIT", "请稍后再打开双栏")
		return
	}
	owner := "ws-" + node
	token := protocol.Token()
	csrf := protocol.Token()
	tx, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`INSERT OR IGNORE INTO users(id,password,admin) VALUES(?, '', 0)`, owner); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	if _, e = tx.Exec(`INSERT OR IGNORE INTO device_operators(node,owner) SELECT ?,id FROM users WHERE id=? AND password='' AND admin=0`, node, owner); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	var existing string
	if e = tx.QueryRow(`SELECT owner FROM device_operators WHERE node=?`, node).Scan(&existing); e != nil || existing != owner {
		reject(w, 409, "WORKBENCH_CONFLICT", "无法建立此设备的本机会话")
		return
	}
	if e = syncDeviceGrants(tx); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	if _, e = tx.Exec(`DELETE FROM sessions WHERE owner=? AND expires<=?`, owner, time.Now().Unix()); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	if _, e = tx.Exec(`INSERT INTO sessions VALUES(?,?,?,?)`, auth.HashSecret(token), owner, csrf, time.Now().Add(8*time.Hour).Unix()); e != nil || tx.Commit() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	out(w, 200, map[string]string{"session": token})
}

func (h *Hub) workspaceHandler() http.Handler {
	browser := h.Browser()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		node, e := h.workspaceNode(r)
		if e != nil || node == HostEndpoint {
			reject(w, 403, "NO_DEVICE_WORKBENCH", "此设备无法打开双栏")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/workspace")
		r = r.Clone(context.WithValue(r.Context(), workspaceNodeKey{}, node))
		// Keep management and any future unrelated endpoints unavailable.
		view := r.Method == http.MethodGet && (path == "/" || strings.HasPrefix(path, "/assets/"))
		allowed := path == "/api/v1/catalog" || path == "/api/v1/catalog/login" || path == "/api/v1/me" || path == "/api/v1/nodes" || strings.HasPrefix(path, "/api/v1/nodes/") && strings.HasSuffix(path, "/roots") || path == "/api/v1/fs/list" || path == "/api/v1/fs/resolve" || path == "/api/v1/plans" || strings.HasPrefix(path, "/api/v1/plans/") || path == "/api/v1/jobs" || strings.HasPrefix(path, "/api/v1/jobs/")
		if !view {
			if !allowed {
				http.NotFound(w, r)
				return
			}
			u, e := h.session(r)
			if e != nil || u.Admin || u.User != "ws-"+node {
				reject(w, 401, "UNAUTHENTICATED", "请重新打开双栏")
				return
			}
		}
		r.URL.Path = path
		r.URL.RawPath = ""
		browser.ServeHTTP(w, r)
	})
}

type workspaceNodeKey struct{}
