package api

import (
	"database/sql"
	"dockroom/internal/auth"
	"dockroom/internal/protocol"
	"fmt"
	"math/rand/v2"
	"net/http"
	"time"
)

type nodeRow struct {
	ID          string `json:"nodeId"`
	OS          string `json:"os"`
	Version     string `json:"version"`
	Online      bool   `json:"online"`
	Last        int64  `json:"lastSeenUnix"`
	State       string `json:"state,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}
type nodeRoot struct {
	Node string `json:"nodeId"`
	protocol.Root
}

func (h *Hub) nodes(w http.ResponseWriter, r *http.Request, u identity) { h.readNodes(w, r, u, false) }
func (h *Hub) adminNodes(w http.ResponseWriter, r *http.Request, u identity) {
	h.readNodes(w, r, u, true)
}
func (h *Hub) readNodes(w http.ResponseWriter, r *http.Request, u identity, admin bool) {
	query := `SELECT id,os,version,last_seen,'','' FROM nodes n WHERE state='approved' AND EXISTS(SELECT 1 FROM grants g JOIN roots t ON t.node=g.node AND t.id=g.root WHERE g.node=n.id AND g.owner=? AND t.available=1) ORDER BY id`
	args := []any{u.User}
	if admin {
		query = `SELECT id,os,version,last_seen,state,fingerprint FROM nodes ORDER BY id`
		args = nil
	}
	rows, e := h.db.QueryContext(r.Context(), query, args...)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	defer rows.Close()
	data := []nodeRow{}
	for rows.Next() {
		var n nodeRow
		if rows.Scan(&n.ID, &n.OS, &n.Version, &n.Last, &n.State, &n.Fingerprint) != nil {
			reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
			return
		}
		data = append(data, n)
	}
	if rows.Err() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	rows.Close()
	for i := range data {
		data[i].Online = h.current(data[i].ID) != nil
	}
	out(w, 200, data)
}
func (h *Hub) roots(w http.ResponseWriter, r *http.Request, u identity) {
	rows, e := h.db.QueryContext(r.Context(), `SELECT t.id,t.label,(t.copy_write AND g.copy_write) FROM roots t JOIN grants g ON g.node=t.node AND g.root=t.id JOIN nodes n ON n.id=t.node WHERE g.owner=? AND t.node=? AND t.available=1 AND n.state='approved' ORDER BY t.id`, u.User, r.PathValue("id"))
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	defer rows.Close()
	data := []protocol.Root{}
	for rows.Next() {
		v := protocol.Root{Mode: "readOnly"}
		if rows.Scan(&v.ID, &v.Label, &v.CanWrite) != nil {
			reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
			return
		}
		if v.CanWrite {
			v.Mode = "readWrite"
		}
		data = append(data, v)
	}
	if rows.Err() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	if len(data) == 0 {
		reject(w, 404, "NOT_FOUND", "没有已授权的根")
		return
	}
	out(w, 200, data)
}
func (h *Hub) adminUsers(w http.ResponseWriter, r *http.Request, _ identity) {
	type user struct {
		ID    string `json:"user"`
		Admin bool   `json:"admin"`
	}
	rows, e := h.db.QueryContext(r.Context(), `SELECT id,admin FROM users WHERE disabled=0 AND id NOT IN (SELECT owner FROM device_operators) ORDER BY id`)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	defer rows.Close()
	users := []user{}
	for rows.Next() {
		var u user
		if rows.Scan(&u.ID, &u.Admin) != nil {
			reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
			return
		}
		users = append(users, u)
	}
	if rows.Err() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	rows.Close()
	rr, e := h.db.QueryContext(r.Context(), `SELECT t.node,t.id,t.label,t.copy_write FROM roots t JOIN nodes n ON n.id=t.node WHERE t.available=1 AND n.state='approved' ORDER BY t.node,t.id`)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	defer rr.Close()
	roots := []nodeRoot{}
	for rr.Next() {
		v := nodeRoot{Root: protocol.Root{Mode: "readOnly"}}
		if rr.Scan(&v.Node, &v.ID, &v.Label, &v.CanWrite) != nil {
			reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
			return
		}
		roots = append(roots, v)
	}
	if rr.Err() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	out(w, 200, map[string]any{"users": users, "roots": roots})
}
func (h *Hub) write(w http.ResponseWriter, r *http.Request, actor, action, ref string, fn func(*sql.Tx) error) bool {
	t, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return false
	}
	defer t.Rollback()
	e = fn(t)
	if e == sql.ErrNoRows {
		reject(w, 409, "CONFLICT", "当前状态不允许此操作")
		return false
	}
	if e != nil || audit(t, actor, action, ref) != nil || t.Commit() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return false
	}
	return true
}
func affected(res sql.Result, e error) error {
	if e != nil {
		return e
	}
	n, e := res.RowsAffected()
	if e == nil && n == 0 {
		return sql.ErrNoRows
	}
	return e
}
func (h *Hub) newEnrollment(w http.ResponseWriter, r *http.Request, u identity) {
	var b struct {
		Node   string `json:"nodeId"`
		Invite bool   `json:"invite,omitempty"`
	}
	if !decode(w, r, &b) {
		return
	}
	if b.Invite && b.Node == "" {
		b.Node = "pc-" + fmt.Sprintf("%x", rand.Uint64())
	}
	if !protocol.ID(b.Node) || b.Node == HostEndpoint || b.Invite && h.devicePort == 0 {
		reject(w, 422, "INVALID_NODE", "设备编号格式无效")
		return
	}
	code := protocol.Token()
	exp := time.Now().Add(10 * time.Minute).Unix()
	if h.write(w, r, u.User, "enrollment.create", b.Node, func(t *sql.Tx) error {
		if _, e := t.Exec(`DELETE FROM enrollments WHERE expires<=? OR node=?`, time.Now().Unix(), b.Node); e != nil {
			return e
		}
		return affected(t.Exec(`INSERT INTO enrollments(hash,node,expires) SELECT ?,?,? WHERE NOT EXISTS (SELECT 1 FROM nodes WHERE id=? AND state<>'revoked')`, auth.HashSecret(code), b.Node, exp, b.Node))
	}) {
		resp := map[string]any{"code": code, "expiresUnix": exp, "nodeId": b.Node}
		if b.Invite {
			delete(resp, "code")
			resp["invite"] = (protocol.Invite{Hub: h.origin, Port: h.devicePort, Node: b.Node, Code: code, Expires: exp, CA: h.ca.CA.Raw}).Encode()
		}
		out(w, 201, resp)
	}
}
func (h *Hub) approve(w http.ResponseWriter, r *http.Request, u identity) {
	var b struct {
		Fingerprint string `json:"fingerprint"`
	}
	if !decode(w, r, &b) {
		return
	}
	id := r.PathValue("id")
	if h.write(w, r, u.User, "node.approve", id, func(t *sql.Tx) error {
		return affected(t.Exec(`UPDATE nodes SET state='approved' WHERE id=? AND fingerprint=? AND state='pending'`, id, b.Fingerprint))
	}) {
		w.WriteHeader(204)
	}
}
func (h *Hub) revoke(w http.ResponseWriter, r *http.Request, u identity) {
	id := r.PathValue("id")
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.write(w, r, u.User, "node.revoke", id, func(t *sql.Tx) error {
		if e := affected(t.Exec(`UPDATE nodes SET state='revoked' WHERE id=? AND state<>'revoked'`, id)); e != nil {
			return e
		}
		if _, e := t.Exec(`DELETE FROM grants WHERE node=?`, id); e != nil {
			return e
		}
		_, e := t.Exec(`UPDATE roots SET available=0 WHERE node=?`, id)
		if e != nil {
			return e
		}
		return syncDeviceGrants(t)
	}) {
		if p := h.peers[id]; p != nil {
			delete(h.peers, id)
			p.ws.Close()
		}
		w.WriteHeader(204)
	}
}
func (h *Hub) grant(w http.ResponseWriter, r *http.Request, u identity) {
	id := r.PathValue("id")
	var b struct {
		Node  string `json:"nodeId"`
		Root  string `json:"rootId"`
		Read  *bool  `json:"read"`
		Write bool   `json:"write,omitempty"`
	}
	if !decode(w, r, &b) {
		return
	}
	if !protocol.ID(id) || !protocol.ID(b.Node) || !protocol.ID(b.Root) || b.Read == nil || (!*b.Read && b.Write) {
		reject(w, 422, "INVALID_GRANT", "授权字段无效")
		return
	}
	if h.write(w, r, u.User, "grant.read", id+"/"+b.Node+"/"+b.Root, func(t *sql.Tx) error {
		if !*b.Read {
			_, e := t.Exec(`DELETE FROM grants WHERE owner=? AND node=? AND root=?`, id, b.Node, b.Root)
			if e != nil {
				return e
			}
			return syncDeviceGrants(t)
		}
		if e := affected(t.Exec(`INSERT INTO grants(owner,node,root,copy_write) SELECT u.id,r.node,r.id,? FROM users u, roots r JOIN nodes n ON n.id=r.node WHERE u.id=? AND u.disabled=0 AND r.node=? AND r.id=? AND r.available=1 AND n.state='approved' AND (NOT ? OR r.copy_write=1) AND NOT EXISTS(SELECT 1 FROM device_operators WHERE owner=u.id) ON CONFLICT(owner,node,root) DO UPDATE SET copy_write=excluded.copy_write`, b.Write, id, b.Node, b.Root, b.Write)); e != nil {
			return e
		}
		return syncDeviceGrants(t)
	}) {
		w.WriteHeader(204)
	}
}
