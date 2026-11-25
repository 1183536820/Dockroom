package service

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"dockroom/internal/agent/fs"
	"dockroom/internal/agent/journal"
	"dockroom/internal/protocol"
	"dockroom/internal/relay"
)

type transfer struct {
	journal *journal.Journal
	client  *http.Client
	url     string
	mu      sync.Mutex
}

func hash(ctx context.Context, f *os.File, off, n int64) (string, error) {
	r := io.NewSectionReader(f, off, n)
	h := sha256.New()
	b := make([]byte, 32<<10)
	for n > 0 {
		if e := ctx.Err(); e != nil {
			return "", e
		}
		c, e := io.ReadFull(r, b[:min(int64(len(b)), n)])
		if e != nil {
			return "", e
		}
		h.Write(b[:c])
		n -= int64(c)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func (t *transfer) req(ctx context.Context, method, token string, r io.Reader, n int64) (*http.Response, error) {
	if len(token) != 43 {
		return nil, fs.ErrUnsafe
	}
	q, e := http.NewRequestWithContext(ctx, method, t.url, r)
	if e != nil {
		return nil, e
	}
	q.ContentLength = n
	q.Header.Set("Authorization", "Bearer "+token)
	return t.client.Do(q)
}
func digest(s string) bool { b, e := hex.DecodeString(s); return e == nil && len(b) == 32 }
func changed() *protocol.Failure {
	return fail("SOURCE_CHANGED", "源文件版本已变更，请刷新后重新选择", false)
}
func transferError(e error) *protocol.Failure {
	if fs.DiskFull(e) {
		return fail("DISK_FULL", "目标空间不足，文件未发布", false)
	}
	if errors.Is(e, journal.ErrMismatch) || errors.Is(e, journal.ErrState) {
		return fail("STAGING_MISMATCH", "暂存的身份、状态或摘要不匹配，未发布", false)
	}
	if errors.Is(e, fs.ErrUnsafe) || errors.Is(e, fs.ErrRootChanged) {
		return fail("UNSAFE_PATH", "卷、权限或目录身份未通过检查", false)
	}
	return fail("TRANSFER_INTERRUPTED", "读写或连接中断，尚未确认完成", true)
}
func (t *transfer) run(ctx context.Context, b *Browser, c protocol.Command) (any, *protocol.Failure) {
	p := c.Payload
	x := p.Copy
	r, ok := b.Roots[p.Ref.RootID]
	if !ok || x == nil || !fs.CanWrite {
		return nil, fail("OP_UNSUPPORTED", "该设备不支持复制", false)
	}
	if strings.HasPrefix(c.Op, "src_") {
		f, e := fs.OpenSource(r.Root, p.Ref.Parts)
		if e != nil {
			return nil, transferError(e)
		}
		defer f.Close()
		i, v, e := fs.FileVersion(f)
		if e != nil {
			return nil, transferError(e)
		}
		if !digest(x.Version) || v != x.Version {
			return nil, changed()
		}
		res := protocol.CopyResult{Native: r.Root.Native(p.Ref.Parts), Size: i.Size(), MTime: i.ModTime().UTC()}
		switch c.Op {
		case "src_info":
			parent, e := r.Root.OpenDir(p.Ref.Parts[:len(p.Ref.Parts)-1])
			if e != nil {
				return nil, transferError(e)
			}
			defer parent.Close()
			res.Parent, e = fs.ObjectIdentity(parent)
			if e != nil {
				return nil, transferError(e)
			}
		case "src_hash":
			res.Digest, e = hash(ctx, f, 0, i.Size())
			if e != nil {
				return nil, transferError(e)
			}
		case "src_block", "src_put":
			if x.Offset < 0 || x.Length < 1 || x.Length > relay.MaxBlock || x.Offset > i.Size() || x.Length > i.Size()-x.Offset {
				return nil, transferError(fs.ErrUnsafe)
			}
			if c.Op == "src_block" {
				res.Digest, e = hash(ctx, f, x.Offset, x.Length)
			} else {
				var a *http.Response
				a, e = t.req(ctx, "PUT", x.Token, io.NewSectionReader(f, x.Offset, x.Length), x.Length)
				if e == nil {
					a.Body.Close()
					if a.StatusCode != 204 {
						e = io.ErrUnexpectedEOF
					}
				}
			}
			if e != nil {
				return nil, transferError(e)
			}
		default:
			return nil, transferError(fs.ErrUnsafe)
		}
		_, now, e := fs.FileVersion(f)
		if e != nil || now != v {
			return nil, changed()
		}
		return res, nil
	}
	if r.Mode != "readWrite" || r.PrivateID == "" || t.journal == nil {
		return nil, fail("READ_ONLY", "目标根未启用写入", false)
	}
	if !fs.Part(x.Name) || x.Size < 0 {
		return nil, transferError(fs.ErrUnsafe)
	}
	// Serialize local journal operations, while WSS heartbeat/source reads stay independent.
	t.mu.Lock()
	defer t.mu.Unlock()
	if e := ctx.Err(); e != nil {
		return nil, transferError(e)
	}
	area, e := r.Root.WriteArea(p.Ref.Parts, r.PrivateID)
	if e != nil {
		return nil, transferError(e)
	}
	defer area.Close()
	parent, e := fs.ObjectIdentity(area.Parent)
	if e != nil || c.Op != "dst_info" && parent != x.Parent {
		return nil, transferError(fs.ErrUnsafe)
	}
	res := protocol.CopyResult{Parent: parent, Native: r.Root.Native(append(append([]string{}, p.Ref.Parts...), x.Name))}
	if c.Op == "dst_info" {
		f, e := fs.OpenFinal(area, x.Name)
		if e == nil {
			f.Close()
			res.State = "exists"
		} else if !fs.Absent(e) {
			return nil, fail("DEST_UNAVAILABLE", "目标名称存在但不可安全访问，未写入", false)
		}
		if res.State != "exists" {
			if e = fs.CheckSpace(area, x.Size); e != nil {
				return nil, transferError(e)
			}
		}
		return res, nil
	}
	if len(x.ID) != 43 {
		return nil, transferError(fs.ErrUnsafe)
	}
	item, state, e := t.journal.Find(x.ID)
	if c.Op == "dst_begin" && errors.Is(e, sql.ErrNoRows) {
		if e = fs.CheckSpace(area, x.Size); e != nil {
			return nil, transferError(e)
		}
		f, e := area.Stage(x.ID, true)
		if e != nil {
			return nil, transferError(e)
		}
		defer f.Close()
		id, e := fs.ObjectIdentity(f)
		if e != nil {
			return nil, transferError(e)
		}
		if e = t.journal.Begin(journal.Item{ID: x.ID, Identity: id, Size: x.Size}); e != nil {
			return nil, transferError(e)
		}
		return res, nil
	}
	if e != nil || item.Size != x.Size {
		return nil, transferError(journal.ErrMismatch)
	}
	if c.Op == "dst_reconcile" || c.Op == "dst_publish" {
		if state == "verified" && c.Op == "dst_publish" {
			v := journal.Intent{ItemID: x.ID, ParentIdentity: x.Parent, Leaf: x.Name, Identity: item.Identity, Digest: x.Digest}
			if e = t.journal.RecordIntent(v); e != nil {
				return nil, transferError(e)
			}
		}
		if c.Op == "dst_reconcile" && state == "verified" {
			res.State = "retry_commit"
			return res, nil
		}
		v, e := t.journal.Intent(x.ID)
		if e != nil || v.ParentIdentity != parent || v.Digest != x.Digest || v.Leaf != x.Name || v.Identity != item.Identity {
			return nil, transferError(journal.ErrMismatch)
		}
		if c.Op == "dst_reconcile" || state == "committed" || state == "needs_review" {
			s := observe(ctx, func() (*os.File, error) { return area.Stage(x.ID, false) }, item.Size)
			d := observe(ctx, func() (*os.File, error) { return fs.OpenFinal(area, x.Name) }, item.Size)
			answer, e := t.journal.Reconcile(v, s, d)
			if e != nil {
				return nil, transferError(e)
			}
			res.State = string(answer)
			return res, nil
		}
		f, e := area.Stage(x.ID, false)
		if e != nil {
			return nil, transferError(e)
		}
		defer f.Close()
		id, e := fs.ObjectIdentity(f)
		if e != nil || id != item.Identity {
			return nil, transferError(journal.ErrMismatch)
		}
		if i, e := f.Stat(); e != nil || i.Size() != item.Size {
			return nil, transferError(journal.ErrMismatch)
		}
		actual, e := hash(ctx, f, 0, item.Size)
		if e != nil || actual != x.Digest {
			return nil, transferError(journal.ErrMismatch)
		}
		if e = ctx.Err(); e != nil {
			return nil, transferError(e)
		}
		permit, e := http.NewRequestWithContext(ctx, "POST", strings.TrimSuffix(t.url, relay.Route)+"/device/data/publish-permit", nil)
		if e != nil || len(x.Token) != 43 {
			return nil, transferError(fs.ErrUnsafe)
		}
		permit.Header.Set("Authorization", "Bearer "+x.Token)
		response, e := t.client.Do(permit)
		if e != nil {
			return nil, transferError(e)
		}
		response.Body.Close()
		if response.StatusCode != 204 {
			return nil, fail("PUBLISH_DENIED", "发布未获实时授权，正在核对任务状态", true)
		}
		if e = ctx.Err(); e != nil {
			return nil, transferError(e)
		}
		if e = fs.Publish(f, area.Parent, x.Name); e != nil {
			if fs.Collision(e) {
				res.State = "skipped"
				return res, nil
			}
			return nil, transferError(e)
		}
		if e = f.Sync(); e != nil {
			return nil, transferError(e)
		}
		fixtureCheckpoint(ctx, "after-publish")
		// The same exclusive handle still denotes the freshly published object.
		decision, e := t.journal.Reconcile(v, journal.Evidence{Status: journal.Absent}, journal.Evidence{Status: journal.Present, Identity: id, Digest: x.Digest})
		if e != nil {
			return nil, transferError(e)
		}
		res.State = string(decision)
		return res, nil
	}
	f, e := area.Stage(x.ID, false)
	if e != nil {
		return nil, transferError(e)
	}
	defer f.Close()
	switch c.Op {
	case "dst_begin":
		res.Offset, e = t.journal.Recover(item, f)
	case "dst_get":
		var a *http.Response
		a, e = t.req(ctx, "GET", x.Token, nil, 0)
		if e != nil {
			break
		}
		defer a.Body.Close()
		if a.StatusCode != 200 || a.ContentLength != x.Length {
			e = io.ErrUnexpectedEOF
			break
		}
		res.Offset, e = t.journal.Append(item, f, x.Offset, x.Length, x.Digest, a.Body)
		if e == nil {
			fixtureCheckpoint(ctx, "after-block")
		}
	case "dst_verify":
		if x.MTime.Before(time.Unix(0, 0)) || x.MTime.After(time.Now().AddDate(20, 0, 0)) {
			return nil, transferError(fs.ErrUnsafe)
		}
		if state == "verified" {
			id, e := fs.ObjectIdentity(f)
			if e != nil || id != item.Identity {
				return nil, transferError(journal.ErrMismatch)
			}
			if i, e := f.Stat(); e != nil || i.Size() != x.Size {
				return nil, transferError(journal.ErrMismatch)
			}
			want, e := hash(ctx, f, 0, x.Size)
			if e != nil || want != x.Digest {
				return nil, transferError(journal.ErrMismatch)
			}
			return res, nil
		}
		if e = fs.SetMTime(f, x.MTime); e == nil {
			e = t.journal.Verify(item, f, x.Digest)
		}
	default:
		e = fs.ErrUnsafe
	}
	if e != nil {
		return nil, transferError(e)
	}
	return res, nil
}

func observe(ctx context.Context, open func() (*os.File, error), size int64) journal.Evidence {
	f, e := open()
	if fs.Absent(e) {
		return journal.Evidence{Status: journal.Absent}
	}
	if e != nil {
		return journal.Evidence{}
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() || i.Size() != size {
		return journal.Evidence{Status: journal.Present}
	}
	id, e := fs.ObjectIdentity(f)
	if e != nil {
		return journal.Evidence{}
	}
	d, e := hash(ctx, f, 0, size)
	if e != nil {
		return journal.Evidence{}
	}
	return journal.Evidence{Status: journal.Present, Identity: id, Digest: d}
}
