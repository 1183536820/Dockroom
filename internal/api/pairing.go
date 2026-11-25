package api

import (
	"crypto/subtle"
	"database/sql"
	"errors"
	"net"
	"net/http"
	"time"

	"dockroom/internal/auth"
	"dockroom/internal/pki"
	"dockroom/internal/protocol"
)

func (h *Hub) joinCode() string {
	return (protocol.JoinCode{Hub: h.origin, Port: h.devicePort, CA: h.ca.CA.Raw}).Encode()
}

func (h *Hub) pairingStatus(w http.ResponseWriter, r *http.Request, _ identity) {
	var active bool
	if e := h.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM pairing_key k JOIN users u ON u.id=k.owner WHERE u.admin=1 AND u.disabled=0)`).Scan(&active); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	var disk bool
	if e := h.db.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM disk_join_keys d JOIN pairing_key p ON p.hash=d.hash)`).Scan(&disk); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "无法读取连接状态")
		return
	}
	out(w, 200, map[string]any{"active": active, "code": h.joinCode(), "disk": disk})
}

func (h *Hub) changePairing(w http.ResponseWriter, r *http.Request, u identity) {
	var b struct {
		Action string `json:"action"`
		Disk   bool   `json:"disk"`
	}
	if !decode(w, r, &b) {
		return
	}
	op := b.Action
	if op != "enable" && op != "rotate" && op != "disable" {
		reject(w, 422, "INVALID_ACTION", "无效的连接密钥操作")
		return
	}
	k := protocol.Token()
	hashed := auth.HashSecret(k)
	if h.write(w, r, u.User, "pairing."+op, "windows", func(t *sql.Tx) error {
		if _, e := t.Exec(`DELETE FROM disk_join_keys`); e != nil {
			return e
		}
		if b.Disk && op != "disable" {
			if _, e := t.Exec(`INSERT INTO disk_join_keys VALUES(?)`, hashed); e != nil {
				return e
			}
		}
		switch op {
		case "enable":
			return affected(t.Exec(`INSERT OR IGNORE INTO pairing_key(slot,hash,owner) VALUES(1,?,?)`, hashed, u.User))
		case "rotate":
			return affected(t.Exec(`UPDATE pairing_key SET hash=?,owner=? WHERE slot=1`, hashed, u.User))
		default:
			_, e := t.Exec(`DELETE FROM pairing_key WHERE slot=1`)
			return e
		}
	}) {
		if op == "disable" {
			w.WriteHeader(204)
			return
		}
		out(w, 201, map[string]string{"code": h.joinCode(), "key": k})
	}
}

func (h *Hub) pairLimit(w http.ResponseWriter, r *http.Request, kind string) bool {
	// The browser-only local management handler never mounts these entry points.
	if r.TLS == nil || r.Header.Get("Origin") != "" {
		reject(w, 403, "DEVICE_TLS_REQUIRED", "此入口仅接受设备 HTTPS 请求")
		return false
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !h.throttle("device:" + kind + ":" + ip) {
		reject(w, 429, "RATE_LIMIT", "设备请求过于频繁，请稍后重试")
		return false
	}
	return true
}

func (h *Hub) joinDevice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Node string `json:"nodeId"`
		Key  string `json:"key"`
		CSR  string `json:"csr"`
	}
	if !h.pairLimit(w, r, "join") || !decode(w, r, &b) {
		return
	}
	if !protocol.ID(b.Node) || b.Node == HostEndpoint || !protocol.ValidPairKey(b.Key) || len(b.CSR) > 8192 {
		reject(w, 422, "INVALID_PAIR", "连接参数无效")
		return
	}
	tx, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	defer tx.Rollback()
	var owner, expected string
	e = tx.QueryRow(`SELECT k.owner,k.hash FROM pairing_key k JOIN users u ON u.id=k.owner WHERE u.disabled=0 AND u.admin=1`).Scan(&owner, &expected)
	if errors.Is(e, sql.ErrNoRows) || e == nil && subtle.ConstantTimeCompare([]byte(expected), []byte(auth.HashSecret(b.Key))) != 1 {
		reject(w, 403, "PAIR_KEY_DENIED", "密钥错误或主机已更换、停用密钥")
		return
	}
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	key, e := pki.CSRKey(b.Node, []byte(b.CSR))
	if e != nil {
		reject(w, 422, "INVALID_CSR", "证书请求无效")
		return
	}
	bundle, cert, e := h.ca.Issue(b.Node, []byte(b.CSR))
	if e != nil {
		reject(w, 503, "CERT_UNAVAILABLE", "设备证书暂不可签发")
		return
	}
	fp := pki.Fingerprint(cert)
	res, e := tx.Exec(`INSERT OR IGNORE INTO nodes(id,serial,fingerprint,state) VALUES(?,?,?,'approved')`, b.Node, cert.SerialNumber.String(), fp)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	if n, e := res.RowsAffected(); e != nil || n != 1 {
		reject(w, 409, "DEVICE_EXISTS", "该设备编号不可使用")
		return
	}
	if _, e = tx.Exec(`INSERT INTO disk_devices SELECT ? FROM disk_join_keys WHERE hash=?`, b.Node, expected); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "无法保存设备")
		return
	}
	if _, e = tx.Exec(`INSERT INTO paired_devices(node,owner,public_key,cert,renew_at) VALUES(?,?,?,?,?)`, b.Node, owner, auth.HashSecret(string(key)), bundle, cert.NotAfter.Add(-7*24*time.Hour).Unix()); e != nil || audit(tx, owner, "pairing.join", b.Node) != nil || tx.Commit() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	out(w, 201, protocol.Issued{Certificate: string(bundle), Fingerprint: fp})
}

func (h *Hub) renewDevice(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Node string `json:"nodeId"`
		CSR  string `json:"csr"`
	}
	if !h.pairLimit(w, r, "renew") || !decode(w, r, &b) {
		return
	}
	if !protocol.ID(b.Node) || len(b.CSR) > 8192 {
		reject(w, 422, "INVALID_CSR", "证书请求无效")
		return
	}
	key, e := pki.CSRKey(b.Node, []byte(b.CSR))
	if e != nil {
		reject(w, 422, "INVALID_CSR", "证书请求无效")
		return
	}
	tx, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	defer tx.Rollback()
	var bundle []byte
	var renewAt int64
	e = tx.QueryRow(`SELECT p.cert,p.renew_at FROM paired_devices p JOIN nodes n ON n.id=p.node WHERE p.node=? AND p.public_key=? AND n.state='approved'`, b.Node, auth.HashSecret(string(key))).Scan(&bundle, &renewAt)
	if errors.Is(e, sql.ErrNoRows) {
		reject(w, 403, "RENEW_DENIED", "设备身份未知或已撤销")
		return
	}
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	cert, e := pki.Cert(bundle)
	if e != nil {
		reject(w, 503, "CERT_UNAVAILABLE", "设备证书暂不可用")
		return
	}
	if time.Now().Unix() >= renewAt {
		bundle, cert, e = h.ca.Issue(b.Node, []byte(b.CSR))
		if e != nil {
			reject(w, 503, "CERT_UNAVAILABLE", "设备证书暂不可签发")
			return
		}
		if _, e = tx.Exec(`UPDATE nodes SET serial=?,fingerprint=? WHERE id=?`, cert.SerialNumber.String(), pki.Fingerprint(cert), b.Node); e != nil {
			reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
			return
		}
		if _, e = tx.Exec(`UPDATE paired_devices SET cert=?,renew_at=? WHERE node=?`, bundle, cert.NotAfter.Add(-7*24*time.Hour).Unix(), b.Node); e != nil || audit(tx, b.Node, "pairing.renew", b.Node) != nil {
			reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
			return
		}
	}
	if tx.Commit() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	out(w, 200, protocol.Issued{Certificate: string(bundle), Fingerprint: pki.Fingerprint(cert)})
}
