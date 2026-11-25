# 渡柜 M1 · 启动和复验

当前运行的是**只读开发切片**。不要照此配置生产根。Hub 持久保存配对、会话、读取授权和管理审计；浏览器不读 Agent 本地磁盘，Agent 不开放监听端口。当前所有可见根只能读取目录，未开放内容读取或任何写操作。

## 1. 构建

使用 Go `1.27.4`、Node `24.19.0`。本次 Windows 机器已在忽略目录 `.tools/go` 安装校验后的 Go；若不在 PATH，以下 `go` 换为 `.\.tools\go\bin\go.exe`。从仓库根目录执行：

```powershell
npm --prefix web ci
npm --prefix web run build
New-Item -ItemType Directory -Force .local/bin | Out-Null
go build -o .local/bin/hub.exe ./cmd/hub
go build -o .local/bin/agent.exe ./cmd/agent
```

先构建 Web 再构建 Hub，前端资源嵌入二进制。Linux 同样构建，去掉 `.exe`。不要从浏览器打开 `workbench.html` 作为运行入口。

## 2. 自动验证（推荐先跑）

```powershell
$env:DOCKROOM_TEST_BASE = (Get-Location).Path
go test -count=1 -v ./...
go vet ./...
# 使用已安装的稳定 Chrome；没有 Chrome 时，在 web 下 npx playwright install chromium，省略以下环境变量
$env:DOCKROOM_BROWSER_CHANNEL = 'chrome'
npm --prefix web test
```

Playwright 启动 1 个 Hub 和 2 个独立 Agent，各有不同根和身份目录，使用随机回环端口。夹具生成随机口令并通过匿名子进程 stdin 传递，不写出口令、会话、接入码。Node 请求验证夹具 CA；Chrome 测试进程仅为该次随机服务器的 SPKI 设置临时信任例外，不更改系统信任库，也不全局关闭校验。生成截图在 `.local/evidence`，进程结束后夹具状态留在 `.local/m1-*`（只含合成文件和夹具私钥；不要提交）。Linux 待实机；`DOCKROOM_FIXTURE_BASE` 可指向测试专用父目录，M1 必须与 `/` 同挂载。

## 3. 手动体验（两个独立 Agent，两个隔离根）

以下手动示例与自动夹具独立，使用回环 8442 / 8443。先创建本轮专用父目录，在该目录自建两个新授权根和合成样本：

```powershell
$demo = Join-Path (Resolve-Path .local) ('manual-' + (New-Guid).Guid)
New-Item -ItemType Directory $demo | Out-Null
New-Item -ItemType Directory (Join-Path $demo 'source'),(Join-Path $demo 'target'),(Join-Path $demo 'agent-a'),(Join-Path $demo 'agent-b') | Out-Null
New-Item -ItemType Directory (Join-Path $demo 'source/十一月'),(Join-Path $demo 'target/空目录') | Out-Null
New-Item -ItemType File (Join-Path $demo 'source/十一月/合成 样张.txt') | Out-Null
.local/bin/hub.exe init --data (Join-Path $demo hub) --host localhost
.local/bin/hub.exe user-add --data (Join-Path $demo hub) --user reader
.local/bin/hub.exe serve --data (Join-Path $demo hub)
```

初始化和建账号均在本机输入独立随机口令；Hub 保持运行。在专用浏览器配置中显式信任此次夹具的 `hub/ca.pem`，打开 [https://localhost:8442](https://localhost:8442)，用 `admin` 登录。关闭体验时移除自己添加的夹具信任；不能验证 CA 时改用自动验证，不要无视证书错误。

两个 Agent 分别做以下步骤（`agent-a` 对应 `studio-07`/`out`/`source`；`agent-b` 对应 `archive-01`/`archive`/`target`）：

1. 在 Agent 状态目录内放 Hub 的公开 `ca.pem`。私钥不要复制给另一节点。执行 `.local/bin/agent.exe pin --path <合成根的绝对路径>`，把输出填入 `config.json` 的 `identity`。
2. Agent 状态目录的 `config.json` 形如（用真实**夹具**绝对路径，JSON 内的反斜线写作 `\\`）：

   ```json
   {"nodeId":"studio-07","hub":"https://localhost:8442","control":"wss://localhost:8443","roots":[
     {"rootId":"out","label":"制作交付","mode":"readOnly","path":"D:\\隔离夹具\\source","identity":"填 pin 输出"}
   ]}
   ```

3. 在设备管理分别签发一次性接入码；执行 `.local/bin/agent.exe enroll --data <Agent状态目录>`，在提示符输入对应接入码。在浏览器录入并核对端点显示的指纹，然后批准。
4. 在各自的终端执行 `.local/bin/agent.exe serve --data <Agent状态目录>`。出现在线状态后，在设备管理为 `reader` 对两个根分别授予读取。切换到 `reader` 登录验证两栏。停止某个 Agent 检查另一栏仍在线；Ctrl+C 停止手动启动的进程。

限制：只测临时合成根。若物理根更换，必须换 `rootId`、重新授权；原 ID 的映射在 Hub 被锁定。在 Windows，`0600` 不是 ACL 安装器；本手册不授权生产使用或替代服务账户、专用 ACL、证书轮换与备份验收。
