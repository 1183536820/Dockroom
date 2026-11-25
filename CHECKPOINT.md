# 渡柜 · 当前交付检查点

原产品合同和 `workbench.html` 保留，未依赖样稿数据。工作区初始没有 Git 仓库；未建 GitHub 远端、未推送。

| 阶段 | 当前状态 |
|---|---|
| [M0](docs/m0/CHECKPOINT.md) | Windows/NTFS 夹具通过安全原语探针；Linux/ext4 实机待验 |
| [M1](docs/m1/CHECKPOINT.md) | 两个独立 Windows Agent + TLS Hub 只读联调通过，Linux 仅构建和契约夹具 |
| [M2](docs/m2/CHECKPOINT.md) | Windows NTFS 双 Agent 单文件复制切片通过，含双向、断线与丢回执恢复、同名不覆盖、撤权与取消 |
| [M3](docs/m3/CHECKPOINT.md) / [M4](docs/m4/CHECKPOINT.md) | Windows 文件管理闭环及生产部署未验收；Linux 只读，实机待验 |

[同一 EXE 的 Windows 页面接入](docs/WINDOWS-PAIRING.md) · [Windows 启动与复验](docs/START-WINDOWS.md) · [Windows 优先 ADR](docs/adr/0002-windows-first.md)。禁止把本机夹具和测试证书作为生产部署。

页面接入：同一台 Windows 宿主上的独立启动器，验证了同一 `DG2` 连接码和密钥免审批接入多个独立身份、真实文件复制、错密钥拒绝、密钥轮换、重启重连、撤权不恢复授权、停用新接入。旧一次性码仍兼容原流程。两台物理电脑的局域网测试、数字签名和服务安装待验。见 [ADR 0004](docs/adr/0004-reusable-windows-join.md)。

双栏直达：主机自动 provision 本机收件夹，DG2 已连接端可用自己的设备身份打开本机与主机双栏；验证主机与已连接端各自发起的真实拷贝、新增共享目录显示、第三台设备隔离。仍为 Windows 单文件复制。见 [ADR 0005](docs/adr/0005-direct-workbench.md)。

双角色回归：同一台 Windows 内的独立进程复现「本机也开主机、本机收件夹已撤销、远端已连接」；两个入口分别读取各自工作台，不会错误回退到本机。验证原身份重连、换主机错密钥不影响原连接，以及正确换主机后原文件和路径保留。未改动用户现有配对、业务目录或已撤销的权限。见 [ADR 0006](docs/adr/0006-explicit-workbench-routing.md)。
