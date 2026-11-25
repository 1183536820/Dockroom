package api

import (
	"context"
	"database/sql"
	"dockroom/internal/protocol"
	"dockroom/internal/relay"
	"encoding/json"
	"errors"
	"log"
	"time"
)

func (h *Hub) allowRelay(ctx context.Context, who relay.Identity, b relay.Block) bool {
	j, e := readJob(h.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM copy_jobs WHERE id=?`, b.Job))
	if e != nil || j.Stopped || j.State != "running" || j.Lease != b.Epoch || j.ID != b.Item || j.ID != b.File || j.Bytes != b.Offset || !h.allowed(ctx, j.Owner, j.Plan) {
		return false
	}
	s, p := j.Plan.Input.Sources[0].Ref, j.Plan.Input.Dest
	if who.Node != s.NodeID && who.Node != p.NodeID {
		return false
	}
	n := h.current(who.Node)
	return n != nil && n.serial == who.Serial
}
func (h *Hub) save(j *job, state string, delay time.Duration) bool {
	j.State = state
	b, _ := json.Marshal(j)
	tx, e := h.db.Begin()
	if e != nil {
		log.Printf("copy job=%s persistence failed", j.ID)
		return false
	}
	defer tx.Rollback()
	if _, e = tx.Exec(`UPDATE copy_jobs SET state=?,due=?,payload=? WHERE id=?`, state, time.Now().Add(delay).UnixMilli(), b, j.ID); e != nil {
		return false
	}
	if terminal(state) {
		if e = audit(tx, j.Owner, "copy."+state, j.ID); e != nil {
			return false
		}
	}
	return tx.Commit() == nil
}
func terminal(s string) bool {
	switch s {
	case "succeeded", "skipped", "failed", "cancelled", "needs_review":
		return true
	}
	return false
}
func (h *Hub) schedule(ctx context.Context) {
	t := time.NewTicker(250 * time.Millisecond)
	defer t.Stop()
	for ctx.Err() == nil {
		j, e := readJob(h.db.QueryRowContext(ctx, `SELECT `+jobColumns+` FROM copy_jobs WHERE state IN ('queued','running','waiting_node','verifying','committing') AND due<=? ORDER BY due,created LIMIT 1`, time.Now().UnixMilli()))
		if e == nil {
			h.run(ctx, &j)
		} else if !errors.Is(e, sql.ErrNoRows) && ctx.Err() == nil {
			log.Print("copy scheduler: cannot read queue")
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
func (h *Hub) stopped(ctx context.Context, j *job) bool {
	var stop bool
	if e := h.db.QueryRowContext(ctx, `SELECT stopped FROM copy_jobs WHERE id=?`, j.ID).Scan(&stop); e != nil {
		return true
	}
	return stop
}
func (h *Hub) end(j *job, state, code string) { j.Problem = code; h.save(j, state, 0) }
func (h *Hub) pause(ctx context.Context, j *job, f *protocol.Failure) {
	if ctx.Err() != nil {
		return
	}
	j.Problem = f.Code
	if !f.Retryable || (j.Tries >= 3 && f.Code != "NODE_OFFLINE") || time.Since(time.Unix(j.Created, 0)) > 24*time.Hour {
		if j.Commit {
			h.end(j, "needs_review", f.Code)
		} else {
			h.end(j, "failed", f.Code)
		}
		return
	}
	if f.Code != "NODE_OFFLINE" {
		j.Tries++
	}
	h.save(j, "waiting_node", time.Second)
}
func (h *Hub) run(ctx context.Context, j *job) {
	p := j.Plan
	s, d := p.Input.Sources[0], p.Input.Dest
	size := p.From.Size
	x := protocol.Copy{ID: j.ID, Size: size, Name: s.Ref.Parts[len(s.Ref.Parts)-1], Parent: p.To.Parent, Digest: j.Digest, MTime: p.From.MTime}
	quiet := func() bool {
		if ctx.Err() != nil {
			return true
		}
		if !h.allowed(ctx, j.Owner, p) {
			if j.Commit {
				h.end(j, "needs_review", "GRANT_REVOKED")
			} else {
				h.end(j, "failed", "GRANT_REVOKED")
			}
			return true
		}
		if h.stopped(ctx, j) && !j.Commit {
			h.end(j, "cancelled", "")
			return true
		}
		return false
	}
	if quiet() {
		return
	}
	if !j.Commit && p.To.State == "exists" {
		h.end(j, "skipped", "NAME_EXISTS")
		return
	}
	if j.Commit {
		a, f := h.rpc(ctx, d, "dst_reconcile", x)
		if f != nil {
			h.pause(ctx, j, f)
			return
		}
		switch a.State {
		case "committed":
			h.end(j, "succeeded", "")
			return
		case "needs_review":
			h.end(j, "needs_review", "COMMIT_UNKNOWN")
			return
		case "retry_commit":
			if h.stopped(ctx, j) {
				h.end(j, "cancelled", "")
				return
			}
		default:
			h.end(j, "needs_review", "COMMIT_UNKNOWN")
			return
		}
	} else {
		if !h.save(j, "running", 0) {
			return
		}
		a, f := h.rpc(ctx, d, "dst_begin", x)
		if f != nil {
			h.pause(ctx, j, f)
			return
		}
		if a.Offset < 0 || a.Offset > size {
			h.end(j, "failed", "INVALID_WATERLINE")
			return
		}
		j.Bytes = a.Offset
		for j.Bytes < size {
			if quiet() {
				return
			}
			if !h.save(j, "running", 0) {
				return
			}
			x.Offset = j.Bytes
			x.Length = min(size-j.Bytes, relay.MaxBlock)
			confirmed, f := h.block(ctx, j, x)
			if f != nil {
				h.pause(ctx, j, f)
				return
			}
			if confirmed != x.Offset+x.Length {
				h.end(j, "failed", "INVALID_WATERLINE")
				return
			}
			j.Bytes = confirmed
			j.Tries = 0
			j.Problem = ""
			if !h.save(j, "running", 0) {
				return
			}
		}
		if quiet() || !h.save(j, "verifying", 0) {
			return
		}
		a, f = h.rpc(ctx, s.Ref, "src_hash", protocol.Copy{Version: s.Version})
		if f != nil {
			h.pause(ctx, j, f)
			return
		}
		if len(a.Digest) != 64 || a.Size != size {
			h.end(j, "failed", "BAD_DIGEST")
			return
		}
		j.Digest = a.Digest
		x.Digest = a.Digest
		if _, f = h.rpc(ctx, d, "dst_verify", x); f != nil {
			h.pause(ctx, j, f)
			return
		}
	}
	if quiet() {
		return
	}
	if _, f := h.rpc(ctx, s.Ref, "src_info", protocol.Copy{Version: s.Version}); f != nil {
		h.pause(ctx, j, f)
		return
	}
	if quiet() {
		return
	}
	if h.stopped(ctx, j) {
		h.end(j, "cancelled", "")
		return
	}
	j.Commit = true
	if !h.save(j, "committing", 0) {
		return
	}
	// Mark the risk window durably before issuing the sole publish command.
	if !h.allowed(ctx, j.Owner, p) || h.stopped(ctx, j) {
		h.end(j, "cancelled", "COMMIT_NOT_SENT")
		return
	}
	dp := h.current(d.NodeID)
	if dp == nil || dp.minor < 1 {
		h.pause(ctx, j, copyFailure("NODE_OFFLINE", "设备离线", true))
		return
	}
	x.Token = h.makePublishTicket(j, dp)
	defer h.clearPublishTicket(x.Token)
	a, f := h.copyRPC(ctx, dp, d, "dst_publish", x)
	if f != nil {
		h.pause(ctx, j, f)
		return
	}
	switch a.State {
	case "committed":
		h.end(j, "succeeded", "")
	case "skipped":
		h.end(j, "skipped", "NAME_EXISTS")
	default:
		h.end(j, "needs_review", "COMMIT_UNKNOWN")
	}
}
func (h *Hub) block(ctx context.Context, j *job, x protocol.Copy) (int64, *protocol.Failure) {
	s, d := j.Plan.Input.Sources[0], j.Plan.Input.Dest
	sp, dp := h.current(s.Ref.NodeID), h.current(d.NodeID)
	if sp == nil || dp == nil || sp.minor < 1 || dp.minor < 1 {
		return 0, copyFailure("NODE_OFFLINE", "传输端点离线", true)
	}
	src := protocol.Copy{Version: s.Version, Offset: x.Offset, Length: x.Length}
	a, f := h.copyRPC(ctx, sp, s.Ref, "src_block", src)
	if f != nil {
		return 0, f
	}
	if len(a.Digest) != 64 {
		return 0, copyFailure("BAD_DIGEST", "分片摘要无效", false)
	}
	if !h.allowed(ctx, j.Owner, j.Plan) || h.stopped(ctx, j) {
		return 0, copyFailure("TRANSFER_STOPPED", "传输授权已撤销或收到取消请求", true)
	}
	t, e := h.relay.Reserve(relay.Permit{Block: relay.Block{Job: j.ID, Item: j.ID, File: j.ID, Epoch: j.Lease, Offset: x.Offset, Length: x.Length}, Source: relay.Identity{Node: s.Ref.NodeID, Serial: sp.serial}, Target: relay.Identity{Node: d.NodeID, Serial: dp.serial}, TTL: 35 * time.Second})
	if e != nil {
		return 0, copyFailure("RELAY_BUSY", "中继暂不可用", true)
	}
	defer t.Stop()
	x.Digest, x.Token = a.Digest, t.TargetToken
	work, stop := context.WithCancel(ctx)
	defer stop()
	type outcome struct {
		r protocol.CopyResult
		f *protocol.Failure
	}
	recv := make(chan outcome, 1)
	go func() { a, f := h.copyRPC(work, dp, d, "dst_get", x); recv <- outcome{a, f} }()
	ready := make(chan error, 1)
	go func() { ready <- t.WaitReady(work) }()
	check := time.NewTicker(150 * time.Millisecond)
	defer check.Stop()
	select {
	case r := <-recv:
		if r.f == nil {
			return 0, copyFailure("BAD_AGENT_RESPONSE", "目标没有完成中继握手", false)
		}
		return 0, r.f
	case e := <-ready:
		if e != nil {
			return 0, copyFailure("RELAY_NOT_READY", "目标中继未就绪", true)
		}
	case <-ctx.Done():
		return 0, copyFailure("NODE_OFFLINE", "调度中断", true)
	}
	src.Token = t.SourceToken
	send := make(chan *protocol.Failure, 1)
	go func() { _, f := h.copyRPC(work, sp, s.Ref, "src_put", src); send <- f }()
	var r outcome
	for send != nil || recv != nil {
		select {
		case f := <-send:
			send = nil
			if f != nil {
				return 0, f
			}
		case r = <-recv:
			recv = nil
			if r.f != nil {
				return 0, r.f
			}
		case <-check.C:
			if !h.allowed(ctx, j.Owner, j.Plan) || h.stopped(ctx, j) {
				return 0, copyFailure("TRANSFER_STOPPED", "传输授权已撤销或收到取消请求", true)
			}
		case <-ctx.Done():
			return 0, copyFailure("NODE_OFFLINE", "调度中断", true)
		}
	}
	return r.r.Offset, nil
}
