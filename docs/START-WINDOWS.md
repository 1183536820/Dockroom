# 渡柜 · Windows 单文件试运行与复验

范围：专用 Windows 本地 NTFS 合成根，仅单普通文件、同名跳过。Linux 不是本阶段的部署前置。不要使用生产根、生产凭据或测试 CA 部署正式服务。

只想试用时，直接复制同一个 Windows EXE 到两台电脑，参见 [页面接入](WINDOWS-PAIRING.md)。以下是维护人员的构建和测试步骤：从工作区根目录，用 Go 1.27.4（本机可用 `.\.tools\go\bin\go.exe`）和 Node 24.19.0：

```powershell
npm --prefix web ci
npm --prefix web run build
New-Item -ItemType Directory -Force .local/bin | Out-Null
go build -o .local/bin/hub.exe ./cmd/hub
go build -o .local/bin/agent.exe ./cmd/agent
# 仅在故障注入测试中使用，不分发：
go build -tags dockroom_fixture -o .local/bin/agent-fixture.exe ./cmd/agent
go build -tags dockroom_fixture -o .local/bin/hub-desktop-fixture.exe ./cmd/hub
$env:DOCKROOM_TEST_BASE = (Get-Location).Path
go test -count=1 ./...
go vet ./...
$env:DOCKROOM_BROWSER_CHANNEL = 'chrome'
npm --prefix web test
```

产出双击版时用 `powershell -File scripts/build-windows.ps1`（Go 不在 PATH 时加 `-Go .\.tools\go\bin\go.exe`）。脚本输出 `dist/Dockroom-Windows-x64.exe` 和 SHA-256 文件。

自动测试开独立 Hub 和两个独立 Agent，回环随机端口、随机测试凭据；ACL 改动只在本轮新建的合成目录。正常 Agent 不包含故障注入器。截图在 `.local/evidence`，测试私有文件在忽略目录 `.local/m1-*`，不提交或转发。

在同一台电脑手动启动，可用 [M1 手册](START-M1.md) 的两节点配对步骤；实际连接另一台 Windows 电脑，见 [双机局域网接入](CONNECT-WINDOWS.md)。本地写入仅在以下条件下开启：

1. 在**本轮新建的合成目标根**上，用 Agent 运行身份配置显式 NTFS ACL。根、私有区、各级目标父目录仅允许服务账号、SYSTEM 和本地管理员的允许型 ACE；目标父目录的文件继承策略必须和私有区一致。不要修改既有业务根的 ACL。
2. 除 `agent pin --path <根>` 返回的 `identity` 外，运行 `agent prepare-write --path <根>` 创建同卷隐藏 `.dockroom`，抄下返回的 `privateId`。仅初始化全新的私有区；不要手工删除或重建旧私有区来绕过身份检查。
3. 目标根配置 `mode: "readWrite"` 并填入 `privateId`；重启 Agent。设备管理为指定账号设置目标根「授予读取及复制写入」，源根只需读取。默认不会给已有读取授权加写权限。
4. 双栏浏览各自目录。选中一个文件，按 `Ctrl+C` 或点「复制文件」；激活目标栏，按 `Ctrl+V` 或点「粘贴到此处」，核对两端真实路径再开始。提交后留在工作台，匹配的目标目录在任务成功后刷新。可手动打开任务记录查看确认字节、最终路径和 SHA-256。
5. 工作台内 `Ctrl+Z` 撤销上一次剪贴板修改；在复制确认框内则关闭尚未提交的确认。它不回滚或删除已提交文件；尚在运行的任务可在任务记录请求取消。输入框保留常规文本快捷键。

待投产验证：两台 Windows 主机、低权限服务、安装包与升级恢复；目录/多选/软删除恢复不是本次交付。未实测断电和满盘，仅有空间预检和错误处理。未建立自动清理策略，取消及失败会保留私有暂存供核对，不能据此盲删。
