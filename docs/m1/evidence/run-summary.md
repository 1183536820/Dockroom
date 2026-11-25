# Windows/NTFS 隔离联调 · 2026-11-25（Asia/Shanghai）

- 宿主：Windows NT 10.0.26100，本机 D: NTFS；Go 1.27.4、Node 24.19.0、Chrome 157.0.8103.79。无 Linux 实机。
- Playwright `1 passed`（用时 13.0 秒）：回环 Hub PID 20628、Agent PID 32368/23808，各自文件根和独立证书。操作者/权限：合成 `admin`、`reader`、`other`；没有任务号（本阶段不提供复制）。
- 夹具：中文、空格、0B/1B、空目录、多层目录、207 项分页目录和指向夹具外侧哨兵的 junction。断线时返回 `NODE_OFFLINE`，并且另一栏仍可读；重开 Hub 后认证、配对、读取授权可用。把同一已有 rootId 指向另一物理合成根并重新钉住后，旧授权不能读取替换根。
- 记录结果：HTTP 读取 200、空目录 entries `[]`；未凭证 401、坏 Origin/CSRF 403、无根授权 404、非法或不受支持路径 422、跨用户/筛选/目录的游标 409。拒绝无证书设备连接。撤销后不再联机。
- `go test -count=1 ./...` 与 `go vet ./...` 通过；Linux/amd64 `go build ./...`、fs/journal 测试二进制编译通过（未执行）。`go test -race` 未跑（本环境 `CGO_ENABLED=0`，未发现对应编译器）。前端类型生成、类型检查及打包通过；Vite 输出第三方 `use client` 指示器告警，未阻塞 SPA 构建。

截图：`m1-1440.png`、`m1-1024.png`、`m1-390.png`、`m1-offline.png`。截图路径名称全部为合成样本；无口令、接入码、私钥或生产目录。测试未安装系统信任、未写入生产根。
