package api

import (
	"io"
	"net/http"
	"sort"
	"strconv"

	"dockroom/internal/agent/fs"
	"dockroom/internal/auth"
)

// Directory metadata only; this handler never opens a file for reading or writing.
func DirectoryCatalog(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		d, e := fs.CatalogDrives()
		if e != nil {
			reject(w, 422, "NO_DRIVES", "无法读取 Windows 本地磁盘")
			return
		}
		out(w, 200, map[string]any{"drives": d})
		return
	}
	skip := 0
	if raw := r.URL.Query().Get("offset"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 0 || n > 10000000 {
			reject(w, 400, "OFFSET", "分页位置无效")
			return
		}
		skip = n
	}
	f, e := fs.CatalogDirectory(path)
	if e != nil {
		reject(w, 403, "DIRECTORY_UNAVAILABLE", "无法打开此目录：请检查 Windows 权限，仅支持本地 NTFS 普通目录。")
		return
	}
	defer f.Close()
	for skip > 0 {
		if r.Context().Err() != nil {
			return
		}
		batch, e := f.ReadDir(min(skip, 256))
		skip -= len(batch)
		if e == io.EOF {
			break
		}
		if e != nil {
			reject(w, 403, "LIST_FAILED", "无法读取目录")
			return
		}
	}
	rows, e := f.ReadDir(201)
	if e != nil && e != io.EOF {
		reject(w, 403, "LIST_FAILED", "无法读取目录")
		return
	}
	more := len(rows) > 200
	if more {
		rows = rows[:200]
	}
	type entry struct {
		Name string `json:"name"`
		Dir  bool   `json:"dir"`
	}
	items := []entry{}
	for _, v := range rows {
		if fs.Part(v.Name()) {
			items = append(items, entry{v.Name(), v.IsDir()})
		}
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Dir != items[j].Dir {
			return items[i].Dir
		}
		return items[i].Name < items[j].Name
	})
	out(w, 200, map[string]any{"entries": items, "more": more})
}

func (h *Hub) catalogLogin(w http.ResponseWriter, r *http.Request, u identity) {
	if u.LocalNode == "" {
		reject(w, 403, "DEVICE_REQUIRED", "请从已连接的电脑登录")
		return
	}
	var input struct {
		Password string `json:"password"`
	}
	if !decode(w, r, &input) {
		return
	}
	if !h.throttle("user:admin") {
		reject(w, 429, "RATE_LIMIT", "请稍后再试")
		return
	}
	select {
	case h.hashing <- struct{}{}:
		defer func() { <-h.hashing }()
	default:
		reject(w, 429, "RATE_LIMIT", "登录繁忙")
		return
	}
	var hash string
	if e := h.db.QueryRowContext(r.Context(), `SELECT password FROM users WHERE id='admin' AND admin=1 AND disabled=0`).Scan(&hash); e != nil || !auth.Verify(input.Password, hash) {
		reject(w, 403, "BAD_PASSWORD", "服务器管理员口令不正确")
		return
	}
	if _, e := h.db.ExecContext(r.Context(), `INSERT OR REPLACE INTO catalog_sessions(hash) VALUES(?)`, u.hash); e != nil {
		reject(w, 503, "DB_UNAVAILABLE", "登录未保存")
		return
	}
	w.WriteHeader(204)
}
func (h *Hub) remoteCatalog(w http.ResponseWriter, r *http.Request, u identity) {
	if !u.Admin {
		var ok int
		if u.LocalNode != "" && h.diskDevice(r, u.LocalNode) {
			DirectoryCatalog(w, r)
			return
		}
		if u.LocalNode == "" || h.db.QueryRowContext(r.Context(), `SELECT 1 FROM catalog_sessions,users WHERE hash=? AND id='admin' AND admin=1 AND disabled=0`, u.hash).Scan(&ok) != nil {
			reject(w, 403, "SERVER_LOGIN", "请登录服务器以查看磁盘")
			return
		}
	}
	DirectoryCatalog(w, r)
}
