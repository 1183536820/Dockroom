# M0 · 降险检查点

盘点：只有 README、01–05、START-HERE、离线 workbench.html；原样保留。没有 Git 元数据/旧代码。Windows NT 10.0.26100、D: NTFS；Go 原先不在 PATH，工作区本地下载并按官方 SHA-256 验证 Go 1.27.4；Node 24.19.0；WSL 无发行版。

复现：仓库根目录 PowerShell 设置 `$env:DOCKROOM_TEST_BASE=(Get-Location).Path`，执行 `go test -count=1 -v ./internal/agent/fs ./internal/relay`（本机可用 `.\.tools\go\bin\go.exe`）。测试只用自动创建的专属合成目录，测试结束清理夹具。Linux 需在 `/` 同一 ext4 挂载设置一个专用测试父目录，再运行同一命令；该行待实机。

| 能力 | NTFS / 本机 | ext4 |
|---|---|---|
| 父句柄、非跟随、身份钉住、保留路径、物理重叠 | 实跑 PASS，junction 指向授权根外的哨兵；不接受替换后的同名根 | `openat2` 候选，待实机 |
| 句柄相对独占改名/发布；8 路同名争用 | 实跑 PASS，仅一胜；旧目标字节不变 | `renameat2(RENAME_NOREPLACE)` 仅试验候选 |
| 同卷目录回收/恢复冲突 | 实跑 PASS，冲突后原件仍在，随后恢复身份不变 | 待实机 |
| 双向背压 | 两条并发合成 8 MiB 流，阻塞时预读不超过 32 KiB、SHA-256 一致 | 与平台无关的进程内探针，**不是联机复制验收** |

未跑：断电/重启后的台账，对抗性本地进程并发改目录、低权限服务账号、独立 Linux 数据卷。所有写原语仅存在于测试文件。出口：依据 ADR 0001 进入 M1 只读开发，不解锁写入。
