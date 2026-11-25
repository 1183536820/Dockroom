package desktop

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"dockroom/internal/agent/fs"
	"dockroom/internal/agent/service"
	"dockroom/internal/api"
	"dockroom/internal/host"
	"dockroom/internal/pki"
	"dockroom/internal/protocol"
	"embed"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed page/*
var page embed.FS
var connectedAssets = api.WorkbenchAssets()

const localCookie = "__Host-dockroom-setup"

type hostState struct {
	Address     string `json:"address"`
	BrowserPort int    `json:"browserPort"`
	DevicePort  int    `json:"devicePort"`
}
type agentState struct {
	Dir    string         `json:"dir"`
	Config service.Config `json:"config"`
	Tag    string         `json:"tag"`
}
type app struct {
	ctx                 context.Context
	exit                context.CancelFunc
	dir, origin, secret string
	mu                  sync.Mutex
	op                  sync.Mutex
	workers             sync.WaitGroup
	host                http.Handler
	stopHost            *host.Running
	hostSettings        hostState
	agent               agentState
	agentStatus         string
	agentCancel         context.CancelFunc
	agentDone           chan struct{}
	hostAgent           agentState
	hostAgentCancel     context.CancelFunc
	hostAgentDone       chan struct{}
	remote              *remoteWorkbench
	initErr             string
}

func Run() error {
	parent, launch, e := desktopEnvironment()
	if e != nil {
		return e
	}
	base := filepath.Join(parent, "Dockroom")
	if e = securedDir(base, true); e != nil {
		return e
	}
	l, e := lock(filepath.Join(base, "launcher.lock"))
	if e != nil {
		return e
	}
	defer l.Close()
	ctx, exit := signal.NotifyContext(context.Background(), os.Interrupt)
	defer exit()
	return runDesktop(ctx, exit, base, launch)
}

func runDesktop(ctx context.Context, exit context.CancelFunc, dir string, launch func(string) error) error {
	l, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		return e
	}
	defer l.Close()
	namePath := filepath.Join(dir, "web-name")
	name, e := os.ReadFile(namePath)
	if errors.Is(e, os.ErrNotExist) {
		var bytes [8]byte
		_, _ = rand.Read(bytes[:])
		name = []byte(hex.EncodeToString(bytes[:]))
		e = pki.WriteNew(dir, "web-name", name)
	}
	if _, err := hex.DecodeString(string(name)); e != nil || err != nil || len(name) != 16 {
		return errors.New("无法读取本机页面标识")
	}
	origin := "http://dockroom-" + string(name) + ".localhost:" + strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
	a := &app{ctx: ctx, exit: exit, dir: dir, origin: origin, secret: protocol.Token()}
	defer a.close()
	if e = load(dir, "host-settings.json", &a.hostSettings); e != nil && !errors.Is(e, os.ErrNotExist) {
		a.initErr = "无法读取之前的主机设置"
	}
	if e = load(dir, "agent-settings.json", &a.agent); e == nil {
		if filepath.Base(a.agent.Dir) != a.agent.Dir || !strings.HasPrefix(a.agent.Dir, "device-") || len(a.agent.Config.Roots) == 0 || len(a.agent.Config.Roots) > 32 {
			return errors.New("本机设备状态目录格式无效")
		}
		if e = securedDir(filepath.Join(dir, a.agent.Dir), true); e != nil {
			return e
		}
		a.startAgent(a.agent)
	} else if !errors.Is(e, os.ErrNotExist) {
		a.initErr = "无法读取之前的设备设置"
	}
	if a.hostSettings.Address != "" {
		if e = a.activateHost(a.hostSettings); e != nil {
			a.initErr = "主机未能自动启动：" + e.Error()
		}
	}
	srv := &http.Server{Handler: a, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 45 * time.Second, MaxHeaderBytes: 16 << 10}
	defer srv.Close()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(l) }()
	if e = launch(a.origin + "/setup#" + a.secret); e != nil {
		return e
	}
	select {
	case <-ctx.Done():
		return nil
	case e = <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}

func load(base, name string, v any) error {
	f, e := os.Open(filepath.Join(base, name))
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 256<<10))
	if e = d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("设置文件格式无效")
	}
	return nil
}
func save(base, name string, v any) error {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		return e
	}
	return pki.WriteNew(base, name, b)
}

func (a *app) close() {
	a.op.Lock()
	defer a.op.Unlock()
	a.mu.Lock()
	h := a.stopHost
	c := a.agentCancel
	hostCancel := a.hostAgentCancel
	remote := a.remote
	a.mu.Unlock()
	if c != nil {
		c()
	}
	if hostCancel != nil {
		hostCancel()
	}
	if remote != nil {
		remote.transport.CloseIdleConnections()
	}
	if h != nil {
		h.Close()
	}
	a.workers.Wait()
}
func (a *app) startAgent(s agentState) {
	a.mu.Lock()
	defer a.mu.Unlock()
	ctx, cancel := context.WithCancel(a.ctx)
	a.agentCancel = cancel
	a.agent = s
	a.agentStatus = "connecting"
	a.agentDone = make(chan struct{})
	done := a.agentDone
	if a.remote != nil {
		a.remote.transport.CloseIdleConnections()
	}
	a.remote, _ = newRemoteWorkbench(s, filepath.Join(a.dir, s.Dir))
	a.workers.Go(func() {
		defer close(done)
		if e := service.RunStatus(ctx, filepath.Join(a.dir, s.Dir), s.Config, func(status string) { a.mu.Lock(); defer a.mu.Unlock(); a.agentStatus = status }); e != nil {
			a.mu.Lock()
			a.agentStatus = "error"
			a.mu.Unlock()
		}
	})
}
func addresses() []string {
	list := []string{}
	ifs, _ := net.Interfaces()
	for _, i := range ifs {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, addr := range addrs {
			ip, _, e := net.ParseCIDR(addr.String())
			if e == nil && ip.To4() != nil && ip.IsGlobalUnicast() {
				list = append(list, ip.String())
			}
		}
	}
	slices.Sort(list)
	return append(slices.Compact(list), "127.0.0.1")
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func failure(w http.ResponseWriter, status int, msg string) {
	reply(w, status, map[string]string{"message": msg})
}
func body(w http.ResponseWriter, r *http.Request, v any) bool {
	if r.Header.Get("Content-Type") != "application/json" {
		failure(w, 415, "请求格式无效")
		return false
	}
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	d.DisallowUnknownFields()
	if d.Decode(v) != nil || d.Decode(new(any)) != io.EOF {
		failure(w, 400, "请求格式无效")
		return false
	}
	return true
}
func same(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }
func (a *app) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Host != strings.TrimPrefix(a.origin, "http://") {
		http.NotFound(w, r)
		return
	}
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		http.Error(w, "Forbidden", 403)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; frame-ancestors 'none'; object-src 'none'; base-uri 'none'; form-action 'self'")
	if r.Method == "GET" {
		name, typ := "", ""
		switch r.URL.Path {
		case "/setup":
			name, typ = "index.html", "text/html; charset=utf-8"
		case "/setup.js":
			name, typ = "app.js", "text/javascript; charset=utf-8"
		case "/setup.css":
			name, typ = "style.css", "text/css; charset=utf-8"
		}
		if name != "" {
			b, e := page.ReadFile("page/" + name)
			if e != nil {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", typ)
			w.Write(b)
			return
		}
	}
	if r.Method == "POST" && r.URL.Path == "/local/v1/open" {
		if r.Header.Get("Origin") != a.origin || !same(r.Header.Get("X-Local-Key"), a.secret) {
			failure(w, 403, "请从渡柜 EXE 打开本机设置页")
			return
		}
		http.SetCookie(w, &http.Cookie{Name: localCookie, Value: a.secret, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
		w.WriteHeader(204)
		return
	}
	c, e := r.Cookie(localCookie)
	if e != nil || !same(c.Value, a.secret) {
		if strings.HasPrefix(r.URL.Path, "/local/") {
			failure(w, 401, "请从渡柜 EXE 打开本机设置页")
		} else {
			http.Redirect(w, r, "/setup", http.StatusSeeOther)
		}
		return
	}
	if !strings.HasPrefix(r.URL.Path, "/local/") {
		if r.URL.Path == "/computer/" || strings.HasPrefix(r.URL.Path, "/computer/assets/") {
			r = r.Clone(r.Context())
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/computer")
			connectedAssets.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/device/") {
			http.NotFound(w, r)
			return
		}
		a.mu.Lock()
		h := a.host
		remote := a.remote
		a.mu.Unlock()
		if r.URL.Path == "/connected" {
			http.Redirect(w, r, "/connected/", 303)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/connected/") {
			if remote == nil {
				if r.URL.Path == "/connected/" {
					http.Redirect(w, r, "/setup#workbench-unavailable", 303)
				} else {
					failure(w, 503, "没有可用的已连接主机")
				}
				return
			}
			if r.URL.RawPath != "" || strings.Contains(r.URL.Path, "\\") {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = strings.TrimPrefix(r.URL.Path, "/connected")
			if strings.HasPrefix(r.URL.Path, "/api/v1/") {
				remote.serve(w, r, a.origin)
				return
			}
			connectedAssets.ServeHTTP(w, r)
			return
		}
		if h == nil {
			// Only the entry document may redirect. Never dispatch an API to a
			// different workspace based on which role happens to be online.
			if r.URL.Path == "/" && remote != nil {
				http.Redirect(w, r, "/connected/", 303)
			} else if r.URL.Path == "/" {
				http.Redirect(w, r, "/setup", 303)
			} else {
				failure(w, 503, "本机主机未开启")
			}
			return
		}
		h.ServeHTTP(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/local/v1/catalog" {
		api.DirectoryCatalog(w, r)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/local/v1/status" {
		a.mu.Lock()
		defer a.mu.Unlock()
		s := map[string]any{"addresses": addresses(), "hostRunning": a.host != nil, "host": a.hostSettings, "agentStatus": a.agentStatus, "error": a.initErr}
		s["localFolders"] = folderList(a.hostAgent, a.agent)
		s["remoteAvailable"] = a.remote != nil
		if a.agent.Dir != "" {
			s["device"] = a.agent.Config.Node
			s["hub"] = a.agent.Config.Hub
			s["control"] = a.agent.Config.Control
			s["tag"] = a.agent.Tag
			s["reusablePairing"] = a.agent.Config.ReusablePairing
			s["folder"] = a.agent.Config.Roots[0].Path
			s["mode"] = a.agent.Config.Roots[0].Mode
		}
		reply(w, 200, s)
		return
	}
	if r.Method == "GET" && r.URL.Path == "/local/v1/connected-status" {
		a.mu.Lock()
		remote, status := a.remote, a.agentStatus
		a.mu.Unlock()
		if status != "online" || remote == nil {
			reply(w, 200, map[string]string{"state": "offline"})
			return
		}
		state := remote.readiness(r.Context())
		reply(w, 200, map[string]string{"state": state})
		return
	}
	if r.Method != "POST" || r.Header.Get("Origin") != a.origin {
		failure(w, 403, "请求来源无效")
		return
	}
	if r.URL.Path == "/local/v1/exit" {
		w.WriteHeader(204)
		time.AfterFunc(300*time.Millisecond, a.exit)
		return
	}
	if !a.op.TryLock() {
		failure(w, 409, "请等当前操作完成")
		return
	}
	defer a.op.Unlock()
	switch r.URL.Path {
	case "/local/v1/disk-transfer":
		a.diskTransfer(w,r)
	case "/local/v1/host":
		a.createHost(w, r)
	case "/local/v1/pick":
		p, e := folderPicker()
		if e != nil {
			failure(w, 422, e.Error())
			return
		}
		reply(w, 200, map[string]string{"folder": p})
	case "/local/v1/add-folder":
		a.addFolder(w, r)
	case "/local/v1/open-folder":
		var req struct {
			ID string `json:"id"`
		}
		if !body(w, r, &req) {
			return
		}
		a.mu.Lock()
		folders := folderList(a.hostAgent, a.agent)
		a.mu.Unlock()
		var root fs.Root
		for _, f := range folders {
			if f.ID == req.ID {
				root = f.root
				break
			}
		}
		if root.Identity == "" {
			failure(w, 422, "尚未配置共享文件夹")
			return
		}
		f, e := root.OpenDir(nil)
		if e != nil {
			failure(w, 422, "共享文件夹不再可用")
			return
		}
		f.Close()
		if e = openBrowser(root.Path); e != nil {
			failure(w, 500, "无法打开文件夹")
			return
		}
		w.WriteHeader(204)
	case "/local/v1/join":
		a.join(w, r)
	case "/local/v1/reconnect":
		a.mu.Lock()
		state, cancel, done := a.agent, a.agentCancel, a.agentDone
		a.mu.Unlock()
		if state.Dir == "" || cancel == nil {
			failure(w, 409, "还未连接其他主机")
			return
		}
		cancel()
		<-done
		a.startAgent(state)
		w.WriteHeader(204)
	default:
		http.NotFound(w, r)
	}
}

func (a *app) createHost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		hostState
		Password string `json:"password"`
	}
	if !body(w, r, &in) {
		return
	}
	a.mu.Lock()
	settings, existing := a.hostSettings, a.host != nil
	a.mu.Unlock()
	if existing {
		failure(w, 409, "主机已启动")
		return
	}
	var e error
	if settings.Address == "" {
		settings = in.hostState
		if !slices.Contains(addresses(), settings.Address) || settings.BrowserPort < 1024 || settings.BrowserPort > 65535 || settings.DevicePort < 1024 || settings.DevicePort > 65535 || settings.DevicePort == settings.BrowserPort || len(in.Password) < 12 || len(in.Password) > 1024 {
			failure(w, 422, "请选择本机地址和两个不同端口，设置至少 12 字节的管理员口令")
			return
		}
		if _, e = os.Stat(filepath.Join(a.dir, "Hub")); !errors.Is(e, os.ErrNotExist) {
			failure(w, 409, "已存在之前的主机状态，未覆盖，请联系维护人员")
			return
		}
		for _, port := range []int{settings.BrowserPort, settings.DevicePort} {
			listener, e := net.Listen("tcp", net.JoinHostPort(settings.Address, strconv.Itoa(port)))
			if e != nil {
				failure(w, 409, "主机端口不可用，请在高级设置更换端口")
				return
			}
			listener.Close()
		}
		if e = host.AddUser(filepath.Join(a.dir, "Hub"), "admin", in.Password, true, settings.Address); e != nil {
			failure(w, 500, "无法初始化主机，状态已保留")
			return
		}
		if e = save(a.dir, "host-settings.json", settings); e != nil {
			failure(w, 500, "无法保存主机设置")
			return
		}
		a.mu.Lock()
		a.hostSettings = settings
		a.mu.Unlock()
	}
	e = a.activateHost(settings)
	if e != nil {
		var bind *net.OpError
		if errors.As(e, &bind) {
			failure(w, 409, "主机端口或地址不可用，请检查端口占用，再重试")
		} else {
			failure(w, 500, "主机无法启动："+e.Error())
		}
		return
	}
	a.mu.Lock()
	a.initErr = ""
	a.mu.Unlock()
	reply(w, 200, map[string]string{"workbench": "/"})
}

func (a *app) join(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Code      string `json:"code"`
		Key       string `json:"key"`
		Folder    string `json:"folder"`
		NewFolder bool   `json:"newFolder"`
		Replace   bool   `json:"replace"`
	}
	if !body(w, r, &in) {
		return
	}
	a.mu.Lock()
	previous, oldCancel, oldDone := a.agent, a.agentCancel, a.agentDone
	a.mu.Unlock()
	if (previous.Dir != "") != in.Replace {
		failure(w, 409, "请从「更换主机」重新输入连接码和密钥")
		return
	}
	id := strings.ToLower(protocol.Token()[:16])
	reusable := !strings.HasPrefix(strings.TrimSpace(in.Code), "DG1.")
	var i protocol.JoinCode
	var node, secret string
	if reusable {
		parsed, e := protocol.ParseJoinCode(in.Code)
		if e != nil {
			failure(w, 422, e.Error())
			return
		}
		i = parsed
		secret = strings.TrimSpace(in.Key)
		if !protocol.ValidPairKey(secret) {
			failure(w, 422, "请完整粘贴主机生成的连接密钥")
			return
		}
		node = "pc-" + strings.ReplaceAll(id, "_", "-")
	} else {
		legacy, e := protocol.ParseInvite(in.Code, time.Now())
		if e != nil {
			failure(w, 422, e.Error())
			return
		}
		i = protocol.JoinCode{Hub: legacy.Hub, Port: legacy.Port, CA: legacy.CA}
		node, secret = legacy.Node, legacy.Code
	}
	var e error
	u, _ := url.Parse(i.Hub)
	root := service.Root{Advertisement: protocol.Root{ID: "files", Mode: "readOnly"}}
	state := agentState{Dir: "device-" + id}
	var roots []service.Root
	if in.Replace {
		roots = previous.Config.Roots
	} else if in.NewFolder {
		root.Path = filepath.Join(a.dir, "Exchange-"+id)
		root.Label = "渡柜收件夹"
		if e = securedDir(root.Path, false); e != nil {
			failure(w, 422, "无法创建专用收件夹")
			return
		}
	} else {
		root.Path = filepath.Clean(in.Folder)
		root.Label = filepath.Base(root.Path)
		// Do not export launcher, Hub, or Agent state even if chosen locally.
		baseID, e := fs.Pin(a.dir)
		if e != nil {
			failure(w, 422, "本机状态目录无法验证")
			return
		}
		root.Identity, e = fs.Pin(root.Path)
		if e != nil || fs.Validate([]fs.Root{{Path: a.dir, Identity: baseID}, root.Root}) != nil {
			failure(w, 422, "请选择安全的独立 NTFS 文件夹，不能共享本机程序数据目录或其上级")
			return
		}
	}
	if !in.Replace && root.Identity == "" {
		root.Identity, e = fs.Pin(root.Path)
		if e != nil {
			failure(w, 422, "专用收件夹不在可用的本地 NTFS 上")
			return
		}
	}
	if !in.Replace && in.NewFolder {
		root.PrivateID, e = fs.PrepareWrite(root.Root)
		if e != nil {
			failure(w, 422, "收件夹未通过写入权限检查，请改选现有文件夹进行只读接入")
			return
		}
		root.Mode = "readWrite"
	}
	if !in.Replace {
		roots = []service.Root{root}
	}
	state.Config = service.Config{Node: node, Hub: i.Hub, Control: "wss://" + net.JoinHostPort(u.Hostname(), strconv.Itoa(i.Port)), Roots: roots, ReusablePairing: reusable}
	if _, e = service.NewBrowser(state.Config); e != nil {
		failure(w, 422, "目录不满足接入条件")
		return
	}
	dir := filepath.Join(a.dir, state.Dir)
	if e = securedDir(dir, false); e != nil {
		failure(w, 500, "无法建立设备状态目录")
		return
	}
	if e = pki.WriteNew(dir, "ca.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: i.CA})); e != nil {
		failure(w, 500, "无法保存主机公开证书")
		return
	}
	fp, e := service.EnrollContext(r.Context(), dir, state.Config, secret)
	if e != nil {
		var deny *service.IdentityError
		if errors.As(e, &deny) && deny.Status == 429 {
			failure(w, 429, "连接请求太频繁，请稍后再试")
		} else if errors.As(e, &deny) && (deny.Status == 403 || deny.Status == 409) && reusable {
			failure(w, 422, "连接密钥错误，或主机已更换、停用密钥")
		} else if errors.As(e, &deny) && (deny.Status == 403 || deny.Status == 409) {
			failure(w, 422, "接入码已使用或不再有效，请让主机重发")
		} else {
			failure(w, 502, "未能连接或验证主机 "+u.Host+"。请核对主机地址、两台电脑的时间和局域网/VPN 连接；若连接码使用 127.0.0.1，它只能在主机本机使用。")
		}
		return
	}
	if !reusable {
		state.Tag = protocol.PairTag(fp)
	}
	if in.Replace {
		e = replaceSetting(a.dir, "agent-settings.json", state)
	} else {
		e = save(a.dir, "agent-settings.json", state)
	}
	if e != nil {
		failure(w, 500, "设备证书已保存在本机，但无法保存配置；请联系维护人员")
		return
	}
	if oldCancel != nil {
		oldCancel()
		<-oldDone
	}
	a.startAgent(state)
	reply(w, 200, map[string]string{"device": node, "tag": state.Tag})
}
