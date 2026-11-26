package api

import (
	"dockroom/internal/agent/fs"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"
	"time"
)

func (h *Hub) diskDevice(r *http.Request, node string) bool {
	var n int
	return h.db.QueryRowContext(r.Context(), `SELECT 1 FROM disk_devices WHERE node=?`, node).Scan(&n) == nil
}
func (h *Hub) diskFile(w http.ResponseWriter, r *http.Request) {
	node, e := h.workspaceNode(r)
	if e != nil || !h.diskDevice(r, node) {
		reject(w, 403, "DISK_PAIR_REQUIRED", "请使用主机新版磁盘连接密钥重新连接一次")
		return
	}
	dir, name := r.URL.Query().Get("dir"), r.URL.Query().Get("name")
	if r.URL.Path == "/device/disk-directory" {
		if e := fs.DiskNewDirectory(dir, name); e != nil {
			if errors.Is(e, os.ErrExist) {
				reject(w, 409, "EXISTS", "目标已有同名目录或文件")
			} else {
				reject(w, 422, "CREATE_FAILED", "无法创建目标目录")
			}
			return
		}
		w.WriteHeader(204)
		return
	}
	ctl := http.NewResponseController(w)
	deadline := time.Now().Add(4 * time.Hour)
	ctl.SetReadDeadline(deadline)
	ctl.SetWriteDeadline(deadline)
	if r.Method == "GET" {
		f, e := fs.DiskSource(dir, name)
		if e != nil {
			reject(w, 403, "UNREADABLE", "源文件不可读或已移动")
			return
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil {
			reject(w, 409, "CHANGED", "源文件已变化")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
		w.WriteHeader(200)
		io.CopyN(w, f, st.Size())
		return
	}
	if e := fs.DiskReceive(dir, name, r.Body, r.ContentLength); e != nil {
		if errors.Is(e, os.ErrExist) {
			reject(w, 409, "EXISTS", "目标已有同名文件，未覆盖")
		} else {
			reject(w, 422, "WRITE_FAILED", "未能写入目标，检查 Windows 权限、磁盘空间或网络")
		}
		return
	}
	w.WriteHeader(204)
}
