package service

import (
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"dockroom/internal/agent/fs"
	"dockroom/internal/protocol"
)

type Advertisement = protocol.Root

type Root struct {
	Advertisement
	fs.Root
	PrivateID string `json:"privateId,omitempty"`
}
type Config struct {
	Node            string `json:"nodeId"`
	Hub             string `json:"hub"`
	Control         string `json:"control"`
	ReusablePairing bool   `json:"reusablePairing,omitempty"`
	Roots           []Root `json:"roots"`
}

type page struct {
	user, filter       string
	ref                protocol.Ref
	entries            []protocol.Entry
	native, id, cursor string
	off                int
	until              time.Time
}
type Browser struct {
	Roots map[string]Root
	mu    sync.Mutex
	pages map[string]*page
}

func NewBrowser(config Config) (*Browser, error) {
	b := &Browser{Roots: map[string]Root{}, pages: map[string]*page{}}
	if !protocol.ID(config.Node) || len(config.Roots) > 32 {
		return nil, fs.ErrUnsafe
	}
	var roots []fs.Root
	for _, r := range config.Roots {
		if !protocol.ID(r.ID) || len(r.Label) == 0 || len(r.Label) > 100 || (r.Mode != "readOnly" && r.Mode != "readWrite") {
			return nil, fs.ErrUnsafe
		}
		if _, ok := b.Roots[r.ID]; ok {
			return nil, fs.ErrUnsafe
		}
		b.Roots[r.ID] = r
		roots = append(roots, r.Root)
	}
	if e := fs.Validate(roots); e != nil {
		return nil, e
	}
	return b, nil
}

func fail(code, message string, retry bool) *protocol.Failure {
	return &protocol.Failure{Code: code, Message: message, Retryable: retry}
}
func fsError(err error) *protocol.Failure {
	if errors.Is(err, fs.ErrRootChanged) {
		return fail("ROOT_UNAVAILABLE", "授权根当前不可用，需端点核对", true)
	}
	return fail("PATH_UNAVAILABLE", "目录不可访问或不是受支持的授权路径", false)
}

func (b *Browser) Execute(c protocol.Command) (any, *protocol.Failure) {
	p := c.Payload
	root, ok := b.Roots[p.Ref.RootID]
	if !ok {
		return nil, fail("ROOT_UNKNOWN", "授权根不存在", false)
	}
	if c.Op == "resolve" {
		parts, e := root.Root.PartsFromPath(p.Input)
		if e != nil {
			return nil, fsError(e)
		}
		f, e := root.Root.OpenDir(parts)
		if e != nil {
			return nil, fsError(e)
		}
		f.Close()
		p.Ref.Parts = parts
		return protocol.Resolved{Ref: p.Ref, Native: root.Root.Native(parts)}, nil
	}
	if c.Op != "list" {
		return nil, fail("OP_UNSUPPORTED", "仅支持只读目录操作", false)
	}
	f, e := root.Root.OpenDir(p.Ref.Parts)
	if e != nil {
		return nil, fsError(e)
	}
	defer f.Close()
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	for key, pg := range b.pages {
		if now.After(pg.until) {
			delete(b.pages, key)
		}
	}
	if p.Cursor != "" {
		pg, ok := b.pages[p.Cursor]
		if !ok || pg.user != p.User || pg.filter != p.Filter || pg.ref.RootID != p.Ref.RootID || !slices.Equal(pg.ref.Parts, p.Ref.Parts) {
			return nil, fail("LIST_EXPIRED", "列表游标已失效，请刷新目录", true)
		}
		return b.output(pg), nil
	}
	rows := make([]protocol.Entry, 0)
	filter := strings.ToLower(p.Filter)
	scanned := 0
	for {
		if time.Now().After(c.Deadline) {
			return nil, fail("DEADLINE", "目录读取超时", true)
		}
		batch, e := f.ReadDir(256)
		if e != nil && !errors.Is(e, io.EOF) {
			return nil, fsError(e)
		}
		for _, d := range batch {
			scanned++
			if scanned > 100000 {
				return nil, fail("DIR_LIMIT", "目录超过 10 万项，请拆分后再浏览", false)
			}
			name := d.Name()
			if strings.EqualFold(name, fs.Private) || !strings.Contains(strings.ToLower(name), filter) {
				continue
			}
			row := protocol.Entry{Name: strings.ToValidUTF8(name, "�"), Kind: "unsupported", Actions: []string{}}
			if d.Type()&os.ModeType == 0 || d.IsDir() {
				info, v, e := fs.Metadata(f, name)
				if e == nil {
					if info.IsDir() {
						row.Kind = "directory"
						row.Actions = []string{"open"}
					} else if info.Mode().IsRegular() {
						row.Kind = "file"
						row.Size = info.Size()
					}
					row.MTime = info.ModTime().UTC()
					row.Version = v
				}
			}
			rows = append(rows, row)
		}
		if errors.Is(e, io.EOF) {
			break
		}
	}
	slices.SortFunc(rows, func(a, b protocol.Entry) int {
		rank := func(s string) int {
			switch s {
			case "directory":
				return 0
			case "file":
				return 1
			default:
				return 2
			}
		}
		if x := rank(a.Kind) - rank(b.Kind); x != 0 {
			return x
		}
		if x := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); x != 0 {
			return x
		}
		return strings.Compare(a.Name, b.Name)
	})
	return b.output(&page{user: p.User, filter: p.Filter, ref: p.Ref, entries: rows, native: root.Root.Native(p.Ref.Parts), id: protocol.Token(), until: time.Now().Add(2 * time.Minute)}), nil
}

func (b *Browser) output(pg *page) protocol.Listing {
	end := min(pg.off+200, len(pg.entries))
	if end < len(pg.entries) && pg.cursor == "" {
		// Bound retained snapshots and cursor records: no per-user unbounded resource.
		if len(b.pages) >= 64 {
			var oldest string
			var stamp time.Time
			for k, p := range b.pages {
				if oldest == "" || p.until.Before(stamp) {
					oldest, stamp = k, p.until
				}
			}
			delete(b.pages, oldest)
		}
		next := *pg
		next.off = end
		next.cursor = ""
		pg.cursor = protocol.Token()
		b.pages[pg.cursor] = &next
	}
	if pg.cursor != "" && b.pages[pg.cursor] == nil {
		pg.cursor = ""
		return b.output(pg)
	}
	return protocol.Listing{Entries: pg.entries[pg.off:end], Native: pg.native, Cursor: pg.cursor, ListingID: pg.id}
}
