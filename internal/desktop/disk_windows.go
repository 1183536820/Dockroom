package desktop

import (
	"dockroom/internal/agent/fs"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
)

func (a *app) diskTransfer(w http.ResponseWriter, r *http.Request) {
	var b struct{ Direction, Source, Destination, Name, Device, Hub string }
	if !body(w, r, &b) {
		return
	}
	a.mu.Lock()
	remote := a.remote
	valid := remote != nil && a.agentStatus == "online" && a.agent.Config.Node == b.Device && a.agent.Config.Hub == b.Hub
	a.mu.Unlock()
	if !valid {
		failure(w, 409, "连接已改变，请重新选择文件")
		return
	}
	method, dir := http.MethodGet, b.Source
	var reader io.Reader
	var size int64
	if !fs.Part(b.Name) {
		failure(w, 422, "文件名无效")
		return
	}
	if b.Direction == "send" {
		method, dir = http.MethodPut, b.Destination
		f, e := fs.DiskSource(b.Source, b.Name)
		if e != nil {
			failure(w, 422, "源文件无法读取")
			return
		}
		defer f.Close()
		st, e := f.Stat()
		if e != nil {
			failure(w, 422, "文件已变化")
			return
		}
		size = st.Size()
		if size > 0 {
			reader = f
		}
	} else if b.Direction != "get" {
		failure(w, 422, "传输方向无效")
		return
	}
	req, e := http.NewRequestWithContext(r.Context(), method, remote.endpoint+"/device/disk-file?"+url.Values{"dir": {dir}, "name": {b.Name}}.Encode(), reader)
	if e != nil {
		failure(w, 422, "连接地址无效")
		return
	}
	req.ContentLength = size
	res, e := remote.transport.RoundTrip(req)
	if e != nil {
		failure(w, 502, "设备连接中断")
		return
	}
	defer res.Body.Close()
	if method == "GET" && res.StatusCode == 200 && res.Header.Get("Content-Type") == "application/octet-stream" {
		if e = fs.DiskReceive(b.Destination, b.Name, res.Body, res.ContentLength); e != nil {
			if errors.Is(e, os.ErrExist) {
				failure(w, 409, "目标已有同名文件，未覆盖")
			} else {
				failure(w, 422, "未能保存文件，请检查目标目录和磁盘空间")
			}
			return
		}
	} else if method != "PUT" || res.StatusCode != 204 {
		switch res.StatusCode {
		case 403:
			failure(w, 403, "源文件不可读，或此设备仍在使用旧版连接密钥")
		case 409:
			failure(w, 409, "目标已有同名文件，未覆盖")
		default:
			failure(w, 502, "对方无法完成传输，请检查版本、目录和空间")
		}
		return
	}
	w.WriteHeader(204)
}
