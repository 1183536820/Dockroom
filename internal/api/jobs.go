package api

import (
	"context"
	"database/sql"
	"dockroom/internal/protocol"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

type plan struct {
	ID      string              `json:"planId"`
	State   string              `json:"state"`
	Expires int64               `json:"expiresUnix"`
	Input   protocol.PlanInput  `json:"input"`
	From    protocol.CopyResult `json:"source"`
	To      protocol.CopyResult `json:"target"`
}
type job struct {
	ID      string `json:"jobId"`
	State   string `json:"state"`
	Plan    plan   `json:"plan"`
	Owner   string `json:"owner"`
	Created int64  `json:"createdUnix"`
	Stopped bool   `json:"cancelRequested"`
	Bytes   int64  `json:"confirmedBytes"`
	Digest  string `json:"sha256,omitempty"`
	Problem string `json:"errorCode,omitempty"`
	Tries   int    `json:"attemptsWithoutProgress"`
	Commit  bool   `json:"commitDispatched"`
	Lease   uint64 `json:"leaseEpoch"`
}

func copyFailure(code, msg string, retry bool) *protocol.Failure {
	return &protocol.Failure{Code: code, Message: msg, Retryable: retry}
}
func validRef(r protocol.Ref) bool {
	return protocol.ID(r.NodeID) && protocol.ID(r.RootID) && r.Parts != nil && len(r.Parts) <= 128
}
func token(s string) bool {
	return len(s) == 43 && !strings.ContainsFunc(s, func(r rune) bool {
		return !(r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_')
	})
}
func (h *Hub) copyAllowed(ctx context.Context, owner string, r protocol.Ref, write bool) bool {
	var yes int
	return h.db.QueryRowContext(ctx, `SELECT 1 FROM grants g JOIN roots t ON t.node=g.node AND t.id=g.root JOIN nodes n ON n.id=t.node JOIN users u ON u.id=g.owner WHERE g.owner=? AND g.node=? AND g.root=? AND u.disabled=0 AND t.available=1 AND n.state='approved' AND n.os='windows' AND (NOT ? OR (g.copy_write=1 AND t.copy_write=1))`, owner, r.NodeID, r.RootID, write).Scan(&yes) == nil
}
func (h *Hub) allowed(ctx context.Context, owner string, p plan) bool {
	return h.copyAllowed(ctx, owner, p.Input.Sources[0].Ref, false) && h.copyAllowed(ctx, owner, p.Input.Dest, true)
}
func (h *Hub) rpc(ctx context.Context, r protocol.Ref, op string, d protocol.Copy) (protocol.CopyResult, *protocol.Failure) {
	p := h.current(r.NodeID)
	if p == nil || p.minor < 1 {
		return protocol.CopyResult{}, copyFailure("NODE_OFFLINE", "设备离线或需升级", true)
	}
	return h.copyRPC(ctx, p, r, op, d)
}
func (h *Hub) copyRPC(ctx context.Context, p *peer, r protocol.Ref, op string, d protocol.Copy) (protocol.CopyResult, *protocol.Failure) {
	res, e := p.ask(ctx, protocol.Command{Protocol: protocol.Major, ID: protocol.Token(), Node: r.NodeID, Epoch: p.epoch, Attempt: 1, Deadline: time.Now().Add(45 * time.Second), Op: op, Payload: protocol.Control{Browse: protocol.Browse{Ref: r}, Copy: &d}})
	if e != nil || h.current(r.NodeID) != p {
		return protocol.CopyResult{}, copyFailure("NODE_OFFLINE", "设备连接中断", true)
	}
	if res.Error != nil {
		return protocol.CopyResult{}, res.Error
	}
	var out protocol.CopyResult
	if json.Unmarshal(res.Value, &out) != nil {
		return out, copyFailure("BAD_AGENT_RESPONSE", "端点回执无效", false)
	}
	return out, nil
}
func copyReject(w http.ResponseWriter, f *protocol.Failure) {
	s := 422
	if f.Retryable {
		s = 503
	}
	if f.Code == "SOURCE_CHANGED" {
		s = 409
	}
	if f.Code == "DISK_FULL" {
		s = 507
	}
	reject(w, s, f.Code, f.Message)
}
func (h *Hub) createPlan(w http.ResponseWriter, r *http.Request, u identity) {
	var in protocol.PlanInput
	if !decode(w, r, &in) {
		return
	}
	if in.Action != "copy" || in.Conflict != "skip" || len(in.Sources) != 1 || !validRef(in.Dest) {
		reject(w, 422, "M2_LIMIT", "仅支持单普通文件，冲突策略为跳过")
		return
	}
	s := in.Sources[0]
	if !validRef(s.Ref) || len(s.Ref.Parts) == 0 || len(s.Version) != 64 {
		reject(w, 422, "INVALID_SOURCE", "请选择普通文件的当前版本")
		return
	}
	ctx := r.Context()
	p := plan{ID: protocol.Token(), State: "ready", Input: in, Expires: time.Now().Add(10 * time.Minute).Unix()}
	if !h.allowed(ctx, u.User, p) {
		reject(w, 403, "COPY_FORBIDDEN", "源根不可读或目标根未授予写权限")
		return
	}
	from, f := h.rpc(ctx, s.Ref, "src_info", protocol.Copy{Version: s.Version})
	if f != nil {
		copyReject(w, f)
		return
	}
	to, f := h.rpc(ctx, in.Dest, "dst_info", protocol.Copy{Name: s.Ref.Parts[len(s.Ref.Parts)-1], Size: from.Size})
	if f != nil {
		copyReject(w, f)
		return
	}
	if !h.allowed(ctx, u.User, p) {
		reject(w, 403, "COPY_FORBIDDEN", "授权已变化")
		return
	}
	if s.Ref.NodeID == in.Dest.NodeID && from.Parent == to.Parent {
		reject(w, 409, "SAME_DIRECTORY", "不能复制到源所在目录")
		return
	}
	if from.Parent == "" || to.Parent == "" || from.Native == "" || to.Native == "" || from.Size < 0 {
		reject(w, 503, "BAD_AGENT_RESPONSE", "端点预检结果无效")
		return
	}
	p.From, p.To = from, to
	b, _ := json.Marshal(p)
	if _, e := h.db.ExecContext(ctx, `INSERT INTO copy_plans(id,owner,expires,payload) VALUES(?,?,?,?)`, p.ID, u.User, p.Expires, b); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "无法保存预检")
		return
	}
	out(w, 202, p)
}
func (h *Hub) readPlan(ctx context.Context, owner, id string) (plan, error) {
	var p plan
	var b []byte
	e := h.db.QueryRowContext(ctx, `SELECT payload FROM copy_plans WHERE owner=? AND id=?`, owner, id).Scan(&b)
	if e == nil {
		e = json.Unmarshal(b, &p)
	}
	return p, e
}
func (h *Hub) getPlan(w http.ResponseWriter, r *http.Request, u identity) {
	p, e := h.readPlan(r.Context(), u.User, r.PathValue("id"))
	if e != nil || !h.allowed(r.Context(), u.User, p) {
		reject(w, 404, "NOT_FOUND", "找不到可访问的预检")
		return
	}
	if p.Expires <= time.Now().Unix() {
		p.State = "expired"
	}
	out(w, 200, p)
}

const jobColumns = `id, owner, state, created, stopped, payload`

func readJob(row interface{ Scan(...any) error }) (job, error) {
	var j job
	var b []byte
	var id, owner, state string
	var created int64
	var stop bool
	e := row.Scan(&id, &owner, &state, &created, &stop, &b)
	if e == nil {
		e = json.Unmarshal(b, &j)
	}
	j.ID, j.Owner, j.State, j.Created, j.Stopped = id, owner, state, created, stop
	return j, e
}
func (h *Hub) visibleJob(ctx context.Context, u identity, id string) (job, bool) {
	j, e := readJob(h.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM copy_jobs WHERE id=? AND (owner=? OR ?)`, id, u.User, u.Admin))
	return j, e == nil && (u.Admin || h.allowed(ctx, u.User, j.Plan))
}
func (h *Hub) job(w http.ResponseWriter, r *http.Request, u identity) {
	j, ok := h.visibleJob(r.Context(), u, r.PathValue("id"))
	if !ok {
		reject(w, 404, "NOT_FOUND", "找不到可访问的任务")
		return
	}
	out(w, 200, j)
}
func (h *Hub) jobItems(w http.ResponseWriter, r *http.Request, u identity) {
	j, ok := h.visibleJob(r.Context(), u, r.PathValue("id"))
	if !ok {
		reject(w, 404, "NOT_FOUND", "找不到可访问的任务")
		return
	}
	out(w, 200, []job{j})
}
func (h *Hub) jobs(w http.ResponseWriter, r *http.Request, u identity) {
	rows, e := h.db.QueryContext(r.Context(), `SELECT `+jobColumns+` FROM copy_jobs WHERE owner=? OR ? ORDER BY created DESC,rowid DESC LIMIT 100`, u.User, u.Admin)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "无法读取任务")
		return
	}
	defer rows.Close()
	all := []job{}
	for rows.Next() {
		j, e := readJob(rows)
		if e != nil {
			reject(w, 503, "DB_UNAVAILABLE", "无法读取任务")
			return
		}
		all = append(all, j)
	}
	if rows.Err() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "无法读取任务")
		return
	}
	rows.Close()
	visible := []job{}
	for _, j := range all {
		if u.Admin || h.allowed(r.Context(), u.User, j.Plan) {
			visible = append(visible, j)
		}
	}
	out(w, 200, visible)
}
func (h *Hub) enqueue(w http.ResponseWriter, r *http.Request, u identity) {
	var in struct {
		Plan string `json:"planId"`
	}
	if !decode(w, r, &in) {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if len(key) < 8 || len(key) > 128 || !token(in.Plan) {
		reject(w, 422, "INVALID_SUBMISSION", "缺少有效的确认编号")
		return
	}
	ctx := r.Context()
	p, e := h.readPlan(ctx, u.User, in.Plan)
	if e != nil || !h.allowed(ctx, u.User, p) {
		reject(w, 404, "NOT_FOUND", "找不到可访问的预检")
		return
	}
	tx, e := h.db.BeginTx(ctx, nil)
	if e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "无法建立任务")
		return
	}
	defer tx.Rollback()
	var id, old, state string
	e = tx.QueryRow(`SELECT id,plan,state FROM copy_jobs WHERE owner=? AND idem=?`, u.User, key).Scan(&id, &old, &state)
	if e == nil && old != in.Plan {
		reject(w, 409, "IDEMPOTENCY_CONFLICT", "确认编号已用于另一预检")
		return
	}
	if errors.Is(e, sql.ErrNoRows) {
		e = tx.QueryRow(`SELECT id,plan,state FROM copy_jobs WHERE owner=? AND plan=?`, u.User, in.Plan).Scan(&id, &old, &state)
	}
	if e == nil {
		out(w, 202, map[string]string{"jobId": id, "state": state})
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		reject(w, 503, "DB_UNAVAILABLE", "无法建立任务")
		return
	}
	now := time.Now().Unix()
	if p.Expires <= now {
		reject(w, 409, "PLAN_EXPIRED", "预检已过期，请重新确认")
		return
	}
	j := job{ID: protocol.Token(), State: "queued", Owner: u.User, Created: now, Plan: p, Lease: epoch()}
	b, _ := json.Marshal(j)
	if _, e = tx.Exec(`INSERT INTO copy_jobs(id,owner,plan,idem,created,due,state,payload) VALUES(?,?,?,?,?,?,?,?)`, j.ID, j.Owner, p.ID, key, now, now, j.State, b); e != nil || audit(tx, u.User, "copy.enqueue", j.ID) != nil || tx.Commit() != nil {
		reject(w, 503, "DB_UNAVAILABLE", "无法建立任务")
		return
	}
	out(w, 202, map[string]string{"jobId": j.ID, "state": j.State})
}
func (h *Hub) cancelJob(w http.ResponseWriter, r *http.Request, u identity) {
	j, ok := h.visibleJob(r.Context(), u, r.PathValue("id"))
	if !ok {
		reject(w, 404, "NOT_FOUND", "找不到可访问的任务")
		return
	}
	if h.write(w, r, u.User, "copy.cancel", j.ID, func(tx *sql.Tx) error {
		return affected(tx.Exec(`UPDATE copy_jobs SET stopped=1,due=0 WHERE id=? AND state IN ('queued','running','waiting_node','verifying','committing')`, j.ID))
	}) {
		w.WriteHeader(204)
	}
}
