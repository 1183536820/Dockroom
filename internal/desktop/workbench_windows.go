package desktop

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dockroom/internal/agent/fs"
	"dockroom/internal/agent/service"
	"dockroom/internal/api"
	"dockroom/internal/host"
	"dockroom/internal/pki"
	"dockroom/internal/protocol"
)

func (a *app) prepareHostEndpoint(settings hostState, run *host.Running) (agentState, error) {
	const stateFile = "host-agent-settings.json"
	var s agentState
	e := load(a.dir, stateFile, &s)
	if e == nil {
		if s.Dir != "host-endpoint" || s.Config.Node != api.HostEndpoint || len(s.Config.Roots) == 0 || len(s.Config.Roots) > 32 {
			return agentState{}, errors.New("本机文件端设置无效")
		}
		if e = securedDir(filepath.Join(a.dir, s.Dir), true); e != nil {
			return s, e
		}
		return s, nil
	}
	if !errors.Is(e, os.ErrNotExist) {
		return s, e
	}
	s.Dir = "host-endpoint"
	dir := filepath.Join(a.dir, s.Dir)
	if e = securedDir(dir, true); e != nil {
		return s, e
	}
	rootPath := filepath.Join(a.dir, "本机收件夹")
	if e = securedDir(rootPath, true); e != nil {
		return s, e
	}
	id, e := fs.Pin(rootPath)
	if e != nil {
		return s, e
	}
	root := service.Root{Advertisement: protocol.Root{ID: "files", Mode: "readWrite", Label: "本机收件夹"}, Root: fs.Root{Path: rootPath, Identity: id}}
	root.PrivateID, e = ensurePrivate(root.Root)
	if e != nil {
		return s, e
	}
	s.Config = service.Config{Node: api.HostEndpoint, Hub: "https://" + net.JoinHostPort(settings.Address, strconv.Itoa(settings.BrowserPort)), Control: "wss://" + net.JoinHostPort(settings.Address, strconv.Itoa(settings.DevicePort)), ReusablePairing: true, Roots: []service.Root{root}}
	ca, e := os.ReadFile(filepath.Join(a.dir, "Hub", "ca.pem"))
	if e != nil {
		return s, e
	}
	if e = writeInitially(dir, "ca.pem", ca); e != nil {
		return s, e
	}
	var csr, key []byte
	key, e = os.ReadFile(filepath.Join(dir, "agent-key.pem"))
	if errors.Is(e, os.ErrNotExist) {
		csr, key, e = pki.CSR(api.HostEndpoint)
		if e == nil {
			e = pki.WriteNew(dir, "agent-key.pem", key)
		}
	} else if e == nil {
		csr, e = pki.RenewalCSR(api.HostEndpoint, key)
	}
	if e != nil {
		return s, e
	}
	cert, e := run.RegisterLocalEndpoint(csr)
	if e != nil {
		return s, e
	}
	if e = writeInitially(dir, "agent.pem", cert); e != nil {
		return s, e
	}
	if e = save(a.dir, stateFile, s); e != nil {
		return s, e
	}
	return s, nil
}

func writeInitially(dir, name string, data []byte) error {
	e := pki.WriteNew(dir, name, data)
	if !errors.Is(e, os.ErrExist) {
		return e
	}
	old, e := os.ReadFile(filepath.Join(dir, name))
	if e != nil {
		return e
	}
	if string(old) != string(data) {
		return errors.New("本机身份文件已变更")
	}
	return nil
}

func ensurePrivate(root fs.Root) (string, error) {
	// Existing private state is accepted only when its persisted identity is known.
	return fs.PrepareWrite(root)
}

func (a *app) activateHost(settings hostState) error {
	if !slices.Contains(addresses(), settings.Address) {
		return errors.New("原主机 IP 不再可用。请先恢复该局域网 IP，现有证书与数据不会被覆盖")
	}
	x := net.JoinHostPort(settings.Address, strconv.Itoa(settings.BrowserPort))
	y := net.JoinHostPort(settings.Address, strconv.Itoa(settings.DevicePort))
	run, e := host.Start(filepath.Join(a.dir, "Hub"), "https://"+x, x, y)
	if e != nil {
		return e
	}
	local, e := a.prepareHostEndpoint(settings, run)
	if e != nil {
		run.Close()
		return e
	}
	a.mu.Lock()
	a.stopHost = run
	a.host = run.Browser(a.origin)
	a.mu.Unlock()
	a.startHostEndpoint(local)
	return nil
}

type localFolder struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Mode string `json:"mode"`
	root fs.Root
}

func folderList(sets ...agentState) []localFolder {
	out := []localFolder{}
	for _, s := range sets {
		for _, r := range s.Config.Roots {
			out = append(out, localFolder{s.Config.Node + "/" + r.ID, r.Path, r.Mode, r.Root})
		}
	}
	return out
}

func (a *app) addFolder(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Folder string `json:"folder"`
		Target string `json:"target"`
	}
	if !body(w, r, &b) {
		return
	}
	a.mu.Lock()
	primary, other := a.hostAgent, a.agent
	cancel, done := a.hostAgentCancel, a.hostAgentDone
	a.mu.Unlock()
	asHost := primary.Dir != ""
	switch b.Target {
	case "host":
		asHost = true
	case "connected":
		asHost = false
	case "":
		if primary.Dir != "" && other.Dir != "" {
			failure(w, 422, "请选择要开放到哪个工作台")
			return
		}
	default:
		failure(w, 422, "工作台无效")
		return
	}
	name := "host-agent-settings.json"
	if !asHost {
		primary, other = other, primary
		name = "agent-settings.json"
		a.mu.Lock()
		cancel, done = a.agentCancel, a.agentDone
		a.mu.Unlock()
	}
	if !primary.Config.ReusablePairing || cancel == nil {
		failure(w, 409, "先启动主机或使用新版连接码连接")
		return
	}
	if len(primary.Config.Roots) >= 32 {
		failure(w, 422, "本机最多开放 32 个文件夹")
		return
	}
	path := filepath.Clean(b.Folder)
	id, e := fs.Pin(path)
	if e != nil {
		failure(w, 422, "请选择本地 NTFS 上的普通文件夹")
		return
	}
	root := service.Root{Advertisement: protocol.Root{ID: "folder-" + hex.EncodeToString([]byte(protocol.Token()[:8])), Mode: "readOnly", Label: filepath.Base(path)}, Root: fs.Root{Path: path, Identity: id}}
	base, e := fs.Pin(a.dir)
	if e != nil {
		failure(w, 422, "无法核对本机目录")
		return
	}
	if fs.Validate([]fs.Root{{Path: a.dir, Identity: base}, root.Root}) != nil {
		failure(w, 422, "不能开放渡柜程序数据目录或其上级")
		return
	}
	for _, f := range folderList(primary, other) {
		if fs.Validate([]fs.Root{f.root, root.Root}) != nil {
			failure(w, 422, "此文件夹已开放、与其他开放目录重叠，或原目录不可访问")
			return
		}
	}
	if pin, e := fs.PrepareWrite(root.Root); e == nil {
		root.Mode = "readWrite"
		root.PrivateID = pin
	}
	primary.Config.Roots = append(slices.Clone(primary.Config.Roots), root)
	if _, e = service.NewBrowser(primary.Config); e != nil {
		failure(w, 422, "此文件夹无法开放")
		return
	}
	if e = replaceSetting(a.dir, name, primary); e != nil {
		failure(w, 500, "无法保存目录设置")
		return
	}
	cancel()
	<-done
	if asHost {
		a.startHostEndpoint(primary)
	} else {
		a.startAgent(primary)
	}
	reply(w, 200, map[string]string{"mode": root.Mode})
}

func replaceSetting(dir, name string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	tmp := "settings-" + protocol.Token() + ".tmp"
	if e = pki.WriteNew(dir, tmp, b); e != nil {
		return e
	}
	defer os.Remove(filepath.Join(dir, tmp))
	return os.Rename(filepath.Join(dir, tmp), filepath.Join(dir, name))
}

func (a *app) startHostEndpoint(s agentState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx, cancel := context.WithCancel(a.ctx)
	a.hostAgentCancel = cancel
	a.hostAgent = s
	a.hostAgentDone = make(chan struct{})
	done := a.hostAgentDone
	a.workers.Go(func() {
		defer close(done)
		_ = service.RunStatus(ctx, filepath.Join(a.dir, s.Dir), s.Config, func(string) {})
	})
}

type remoteWorkbench struct {
	origin, endpoint, dir string
	tls                   *tls.Config
	transport             *http.Transport
	mu                    sync.Mutex
	session               string
	expires               time.Time
}

func newRemoteWorkbench(s agentState, dir string) (*remoteWorkbench, error) {
	if !s.Config.ReusablePairing {
		return nil, errors.New("旧版连接请在主机上打开工作台")
	}
	roots := x509.NewCertPool()
	ca, e := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if e != nil || !roots.AppendCertsFromPEM(ca) {
		return nil, errors.New("无法读取主机证书")
	}
	control, e := url.Parse(s.Config.Control)
	if e != nil || control.Scheme != "wss" || control.Hostname() == "" || control.User != nil {
		return nil, errors.New("连接地址无效")
	}
	tlsConf := &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS13, GetClientCertificate: func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
		cert, e := tls.LoadX509KeyPair(filepath.Join(dir, "agent.pem"), filepath.Join(dir, "agent-key.pem"))
		return &cert, e
	}}
	return &remoteWorkbench{origin: s.Config.Hub, endpoint: "https://" + control.Host, dir: dir, tls: tlsConf, transport: &http.Transport{TLSClientConfig: tlsConf, ResponseHeaderTimeout: 20 * time.Second, IdleConnTimeout: 30 * time.Second}}, nil
}

func (b *remoteWorkbench) open(ctx context.Context) (string, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.session != "" && time.Now().Before(b.expires) {
		return b.session, nil
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodPost, b.endpoint+"/device/workbench", nil)
	if e != nil {
		return "", e
	}
	c := &http.Client{Transport: b.transport, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, e := c.Do(req)
	if e != nil {
		return "", e
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", errors.New("主机尚未开启新版工作台或已撤销此设备")
	}
	var result struct {
		Session string `json:"session"`
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 2048)).Decode(&result); e != nil || !protocol.ValidPairKey(result.Session) {
		return "", errors.New("主机会话无效")
	}
	b.session = result.Session
	b.expires = time.Now().Add(7 * time.Hour)
	return b.session, nil
}

func (b *remoteWorkbench) readiness(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	session, e := b.open(ctx)
	if e != nil {
		return "unavailable"
	}
	public, _ := url.Parse(b.origin)
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, b.endpoint+"/workspace/api/v1/nodes", nil)
	if e != nil {
		return "unavailable"
	}
	req.Host = public.Host
	req.AddCookie(&http.Cookie{Name: "__Host-dockroom", Value: session})
	res, e := b.transport.RoundTrip(req)
	if e != nil {
		return "unavailable"
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b.invalidate(session, res.StatusCode)
		return "unavailable"
	}
	var nodes []struct {
		Online bool `json:"online"`
	}
	if e = json.NewDecoder(io.LimitReader(res.Body, 256<<10)).Decode(&nodes); e != nil {
		return "unavailable"
	}
	count := 0
	for _, n := range nodes {
		if n.Online {
			count++
		}
	}
	if count == 0 {
		return "no-folders"
	}
	if count == 1 {
		return "one-device"
	}
	return "ready"
}

func (b *remoteWorkbench) invalidate(session string, status int) {
	if status != 401 && status != 403 {
		return
	}
	b.mu.Lock()
	if b.session == session {
		b.session = ""
	}
	b.mu.Unlock()
	b.transport.CloseIdleConnections()
}

func (b *remoteWorkbench) serve(w http.ResponseWriter, r *http.Request, localOrigin string) {
	if r.Method != "GET" && r.Header.Get("Origin") != localOrigin {
		failure(w, 403, "请求来源无效")
		return
	}
	if r.URL.RawPath != "" || strings.Contains(r.URL.Path, "\\") {
		http.NotFound(w, r)
		return
	}
	path := r.URL.Path
	allowed := path == "/api/v1/catalog" || path == "/api/v1/catalog/login" || path == "/api/v1/me" || path == "/api/v1/nodes" || strings.HasPrefix(path, "/api/v1/nodes/") && strings.HasSuffix(path, "/roots") || path == "/api/v1/fs/list" || path == "/api/v1/fs/resolve" || path == "/api/v1/plans" || strings.HasPrefix(path, "/api/v1/plans/") || path == "/api/v1/jobs" || strings.HasPrefix(path, "/api/v1/jobs/")
	if !allowed {
		http.NotFound(w, r)
		return
	}
	sess, e := b.open(r.Context())
	if e != nil {
		failure(w, 502, "暂时打不开已连接主机的工作台，请返回连接设置后重试")
		return
	}
	remote, _ := url.Parse(b.endpoint)
	public, _ := url.Parse(b.origin)
	proxy := &httputil.ReverseProxy{
		Transport: b.transport,
		Rewrite: func(p *httputil.ProxyRequest) {
			p.SetURL(remote)
			p.Out.URL.Path = "/workspace" + p.In.URL.Path
			p.Out.URL.RawPath = ""
			p.Out.Host = public.Host
			p.Out.Header.Del("Cookie")
			p.Out.AddCookie(&http.Cookie{Name: "__Host-dockroom", Value: sess})
			if p.In.Header.Get("Origin") == localOrigin {
				p.Out.Header.Set("Origin", b.origin)
			} else {
				p.Out.Header.Del("Origin")
			}
		},
		ModifyResponse: func(res *http.Response) error {
			if !strings.HasPrefix(path, "/api/v1/catalog") || res.StatusCode == 401 {
				b.invalidate(sess, res.StatusCode)
			}
			kind, _, _ := strings.Cut(res.Header.Get("Content-Type"), ";")
			if (res.StatusCode >= 300 && res.StatusCode < 400) || (res.StatusCode != 204 && kind != "application/json") {
				return errors.New("invalid upstream workbench response")
			}
			// Keep upstream response headers from affecting the desktop origin.
			res.Header = make(http.Header)
			res.Header.Set("Content-Type", "application/json; charset=utf-8")
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			failure(w, 502, "主机工作台暂时不可用")
		},
	}
	proxy.ServeHTTP(w, r)
}
