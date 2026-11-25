package api

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"dockroom/internal/auth"
	"dockroom/internal/pki"
	"dockroom/internal/protocol"
	"dockroom/internal/relay"
)

//go:embed all:webdist
var web embed.FS

// WorkbenchAssets serves the locally embedded client, also used by the
// desktop's connected view. An upstream Hub never supplies executable UI
// code to the private desktop loopback origin.
func WorkbenchAssets() http.Handler {
	d, _ := fs.Sub(web, "webdist")
	static := http.FileServerFS(d)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && r.Method != "HEAD" {
			http.NotFound(w, r)
			return
		}
		if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			http.NotFound(w, r)
			return
		}
		static.ServeHTTP(w, r)
	})
}

const cookie = "__Host-dockroom"

type Hub struct {
	db             *sql.DB
	ca             pki.Authority
	origin         string
	devicePort     int
	mu             sync.Mutex
	peers          map[string]*peer
	login          map[string]limit
	hashing        chan struct{}
	relay          *relay.Relay
	stop           context.CancelFunc
	worker         sync.WaitGroup
	publishMu      sync.Mutex
	publishTickets map[string]publishTicket
}
type limit struct {
	until time.Time
	count int
}
type identity struct {
	User      string `json:"user"`
	Admin     bool   `json:"admin"`
	CSRF      string `json:"csrf"`
	LocalNode string `json:"localNode,omitempty"`
	hash      string
}

func New(db *sql.DB, ca pki.Authority, origin string, devicePort int) *Hub {
	h := &Hub{db: db, ca: ca, origin: origin, peers: map[string]*peer{}, login: map[string]limit{}, hashing: make(chan struct{}, 2), publishTickets: map[string]publishTicket{}}
	h.devicePort = devicePort
	h.relay = relay.New(h.allowRelay)
	ctx, stop := context.WithCancel(context.Background())
	h.stop = stop
	h.worker.Go(func() { h.schedule(ctx) })
	return h
}
func (h *Hub) Browser() http.Handler {
	return h.BrowserAt(h.origin)
}

type browserOriginKey struct{}

func (h *Hub) originFor(r *http.Request) string {
	if origin, ok := r.Context().Value(browserOriginKey{}).(string); ok {
		return origin
	}
	return h.origin
}

// BrowserAt is also used behind the Windows launcher's private loopback gate.
// Never derive the origin from an incoming header.
func (h *Hub) BrowserAt(origin string) http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("POST /device/enroll", h.enroll)
	m.HandleFunc("POST /device/join", h.joinDevice)
	m.HandleFunc("POST /device/renew", h.renewDevice)
	m.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if h.db.PingContext(r.Context()) != nil {
			reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
			return
		}
		w.WriteHeader(204)
	})
	m.HandleFunc("POST /api/v1/session", h.signIn)
	m.HandleFunc("GET /api/v1/catalog", h.withUser(h.remoteCatalog, false))
	m.HandleFunc("POST /api/v1/catalog/login", h.withUser(h.catalogLogin, false))
	m.HandleFunc("DELETE /api/v1/session", h.withUser(h.signOut, false))
	m.HandleFunc("GET /api/v1/me", h.withUser(func(w http.ResponseWriter, _ *http.Request, u identity) { out(w, 200, u) }, false))
	m.HandleFunc("GET /api/v1/nodes", h.withUser(h.nodes, false))
	m.HandleFunc("GET /api/v1/nodes/{id}/roots", h.withUser(h.roots, false))
	m.HandleFunc("POST /api/v1/fs/{op}", h.withUser(h.browse, false))
	m.HandleFunc("GET /api/v1/admin/nodes", h.withUser(h.adminNodes, true))
	m.HandleFunc("GET /api/v1/admin/users", h.withUser(h.adminUsers, true))
	m.HandleFunc("POST /api/v1/enrollments", h.withUser(h.newEnrollment, true))
	m.HandleFunc("GET /api/v1/admin/pairing", h.withUser(h.pairingStatus, true))
	m.HandleFunc("POST /api/v1/admin/pairing", h.withUser(h.changePairing, true))
	m.HandleFunc("POST /api/v1/nodes/{id}/approve", h.withUser(h.approve, true))
	m.HandleFunc("POST /api/v1/nodes/{id}/revoke", h.withUser(h.revoke, true))
	m.HandleFunc("PUT /api/v1/users/{id}/grants", h.withUser(h.grant, true))
	m.HandleFunc("POST /api/v1/plans", h.withUser(h.createPlan, false))
	m.HandleFunc("GET /api/v1/plans/{id}", h.withUser(h.getPlan, false))
	m.HandleFunc("POST /api/v1/jobs", h.withUser(h.enqueue, false))
	m.HandleFunc("GET /api/v1/jobs", h.withUser(h.jobs, false))
	m.HandleFunc("GET /api/v1/jobs/{id}", h.withUser(h.job, false))
	m.HandleFunc("GET /api/v1/jobs/{id}/items", h.withUser(h.jobItems, false))
	m.HandleFunc("POST /api/v1/jobs/{id}/cancel", h.withUser(h.cancelJob, false))
	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { reject(w, 404, "NOT_FOUND", "接口不可用") })
	static := WorkbenchAssets()
	m.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			reject(w, 405, "METHOD", "请求方法不支持")
			return
		}
		if r.URL.Path != "/" && !strings.HasPrefix(r.URL.Path, "/assets/") {
			reject(w, 404, "NOT_FOUND", "页面不存在")
			return
		}
		static.ServeHTTP(w, r)
	})
	_, host, _ := strings.Cut(origin, "://")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != host {
			reject(w, 400, "HOST", "请求 Host 不匹配")
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
		m.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), browserOriginKey{}, origin)))
	})
}
func out(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func reject(w http.ResponseWriter, status int, code, msg string) {
	out(w, status, protocol.Failure{Code: code, Message: msg, RequestID: protocol.Token(), Retryable: status == 503 || status == 429})
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if mt, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";"); mt != "application/json" {
		reject(w, 415, "MEDIA_TYPE", "需要 application/json")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32<<10))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		reject(w, 400, "INVALID_BODY", "请求格式无效")
		return false
	}
	return true
}
func (h *Hub) session(r *http.Request) (identity, error) {
	c, e := r.Cookie(cookie)
	if e != nil || len(c.Value) != 43 {
		return identity{}, sql.ErrNoRows
	}
	u := identity{hash: auth.HashSecret(c.Value)}
	e = h.db.QueryRowContext(r.Context(), `SELECT users.id, users.admin, sessions.csrf FROM sessions JOIN users ON users.id=sessions.owner WHERE sessions.hash=? AND sessions.expires>? AND users.disabled=0`, u.hash, time.Now().Unix()).Scan(&u.User, &u.Admin, &u.CSRF)
	if e == nil && strings.HasPrefix(u.User, "ws-") {
		var bound string
		err := h.db.QueryRowContext(r.Context(), `SELECT node FROM device_operators WHERE owner=?`, u.User).Scan(&bound)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return identity{}, err
		}
		if err == nil {
			actual, _ := r.Context().Value(workspaceNodeKey{}).(string)
			if actual != bound {
				return identity{}, sql.ErrNoRows
			}
			u.LocalNode = bound
		}
	}
	return u, e
}
func (h *Hub) withUser(fn func(http.ResponseWriter, *http.Request, identity), admin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, e := h.session(r)
		if e != nil {
			if errors.Is(e, sql.ErrNoRows) {
				reject(w, 401, "UNAUTHENTICATED", "请登录")
			} else {
				reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
			}
			return
		}
		if admin && !u.Admin {
			reject(w, 403, "FORBIDDEN", "需要管理员权限")
			return
		}
		if r.Method != "GET" && (r.Header.Get("Origin") != h.originFor(r) || r.Header.Get("X-CSRF-Token") != u.CSRF) {
			reject(w, 403, "CSRF", "请求来源或安全令牌无效")
			return
		}
		fn(w, r, u)
	}
}
func (h *Hub) throttle(ip string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := time.Now()
	l := h.login[ip]
	if !now.Before(l.until) {
		l = limit{until: now.Add(time.Minute)}
	}
	if len(h.login) >= 2048 {
		for k, v := range h.login {
			if !now.Before(v.until) {
				delete(h.login, k)
			}
		}
		if len(h.login) >= 2048 {
			return false
		}
	}
	if l.count >= 8 {
		return false
	}
	l.count++
	h.login[ip] = l
	return true
}
func (h *Hub) signIn(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") != h.originFor(r) {
		reject(w, 403, "ORIGIN", "请求来源不匹配")
		return
	}
	var body struct {
		User     string `json:"user"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if !h.throttle(ip) || !h.throttle("user:"+body.User) {
		reject(w, 429, "RATE_LIMIT", "登录过于频繁，请稍后再试")
		return
	}
	if !protocol.ID(body.User) || len(body.Password) > 1024 {
		reject(w, 401, "AUTH_FAILED", "账号或口令错误")
		return
	}
	select {
	case h.hashing <- struct{}{}:
		defer func() { <-h.hashing }()
	default:
		reject(w, 429, "RATE_LIMIT", "登录繁忙，请稍后再试")
		return
	}
	u := identity{User: body.User, CSRF: protocol.Token()}
	var hash string
	e := h.db.QueryRowContext(r.Context(), `SELECT password,admin FROM users WHERE id=? AND disabled=0`, u.User).Scan(&hash, &u.Admin)
	if !auth.Verify(body.Password, hash) || e != nil {
		reject(w, 401, "AUTH_FAILED", "账号或口令错误")
		return
	}
	token := protocol.Token()
	tx, e := h.db.BeginTx(r.Context(), nil)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	defer tx.Rollback()
	if c, e := r.Cookie(cookie); e == nil {
		if _, e := tx.Exec(`DELETE FROM sessions WHERE hash=?`, auth.HashSecret(c.Value)); e != nil {
			reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
			return
		}
	}
	if _, e = tx.Exec(`DELETE FROM sessions WHERE expires<=?`, time.Now().Unix()); e == nil {
		_, e = tx.Exec(`INSERT INTO sessions VALUES (?,?,?,?)`, auth.HashSecret(token), u.User, u.CSRF, time.Now().Add(8*time.Hour).Unix())
	}
	if e != nil || tx.Commit() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookie, Value: token, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: 28800})
	out(w, 200, u)
}
func (h *Hub) signOut(w http.ResponseWriter, r *http.Request, u identity) {
	if _, e := h.db.ExecContext(r.Context(), "DELETE FROM sessions WHERE hash=?", u.hash); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "数据库不可用")
		return
	}
	http.SetCookie(w, &http.Cookie{Name: cookie, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(204)
}

func (h *Hub) permitted(u identity, ref protocol.Ref) bool {
	var ok int
	e := h.db.QueryRow(`SELECT 1 FROM grants g JOIN nodes n ON n.id=g.node JOIN roots r ON r.node=g.node AND r.id=g.root JOIN sessions s ON s.owner=g.owner JOIN users u ON u.id=g.owner WHERE g.owner=? AND g.node=? AND g.root=? AND n.state='approved' AND r.available=1 AND s.hash=? AND s.expires>? AND u.disabled=0`, u.User, ref.NodeID, ref.RootID, u.hash, time.Now().Unix()).Scan(&ok)
	return e == nil
}
func (h *Hub) browse(w http.ResponseWriter, r *http.Request, u identity) {
	op := r.PathValue("op")
	if op != "list" && op != "resolve" {
		reject(w, 404, "OP_UNSUPPORTED", "本阶段只开放目录浏览与定位")
		return
	}
	var b protocol.Browse
	if !decode(w, r, &b) {
		return
	}
	if !protocol.ID(b.Ref.NodeID) || !protocol.ID(b.Ref.RootID) || b.Ref.Parts == nil || len(b.Ref.Parts) > 128 || len(b.Filter) > 512 || len(b.Input) > 16384 || len(b.Cursor) > 128 {
		reject(w, 422, "INVALID_REF", "目录引用格式无效")
		return
	}
	if !h.permitted(u, b.Ref) {
		reject(w, 404, "NOT_FOUND", "未找到已授权的目录")
		return
	}
	p := h.current(b.Ref.NodeID)
	if p == nil {
		reject(w, 503, "NODE_OFFLINE", "节点离线，未读取目录")
		return
	}
	request := protocol.Command{Protocol: protocol.Major, ID: protocol.Token(), Node: b.Ref.NodeID, Epoch: p.epoch, Attempt: 1, Deadline: time.Now().Add(12 * time.Second), Op: op, Payload: protocol.Control{User: u.hash, Browse: b}}
	response, e := p.ask(r.Context(), request)
	if !h.permitted(u, b.Ref) {
		reject(w, 404, "NOT_FOUND", "未找到已授权的目录")
		return
	}
	if e != nil || h.current(b.Ref.NodeID) != p {
		reject(w, 503, "NODE_UNAVAILABLE", "未能读取实时目录，请重试")
		return
	}
	if response.Error != nil {
		f := response.Error
		f.RequestID = request.ID
		status := 422
		switch f.Code {
		case "LIST_EXPIRED":
			status = 409
		case "ROOT_UNAVAILABLE", "DEADLINE":
			status = 503
		case "ROOT_UNKNOWN":
			status = 404
		}
		out(w, status, f)
		return
	}
	out(w, 200, response.Value)
}

func epoch() uint64 { return uint64(rand.Uint32()) + 1 }
