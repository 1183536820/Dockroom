# Windows M2 · 2026-11-25（Asia/Shanghai）

- Windows NT 10.0.26100 / D: NTFS，Go 1.27.4，Node 24.19.0，Chrome。合成操作者 reader、admin、other；未使用生产目录或凭据。
- Playwright：3 passed，31.9 秒（M1 回归 13.0 秒，M2 进程故障链 11.9 秒，M2 浏览器全链 5.7 秒）。M2 故障链结束时 Hub/Agent PID 29488/28864/11016。双向任务 `-vJLPd-mA7xkbgi-qGDeqepat5GF5RVrSlvUiLvcPpk`、`Pc76X5THyHz5jgXrmaAQZTGADgdqlafh38UoV2cXWGg`。
- `跨分片 校验.bin` 8,388,765 B，源与两边最终副本 SHA-256 `8396fe995ca92f7091e1903c841b9deb03a00e6cda6a249a6846865322d8fd2e`。源未变；目标确实从已落盘的 8,388,608 B 水位完成；反向运行在发布后、回执前中断，重启后目标只有一份。0B 摘要另验。
- 已验：撤销写权限后无目标文件（`GRANT_REVOKED`）；取消后无目标；已有同名及预检后碰撞均 `NAME_EXISTS` 跳过；源变更 `SOURCE_CHANGED`；读权限不足 403、任务隔离 404、同目录 409；重复确认复用原任务。
- Go 单测、vet、前端生成类型与构建通过；Linux/amd64 `go build ./...` 通过，不代表 Linux 运行与写入通过。竞态工具链仍未配置。无生产性能、双主机或掉电数据。
- 浏览器截图：`m2-confirm.png`、`m2-verified.png`，包含合成文件路径、任务号和摘要，不含秘钥与接入码。
