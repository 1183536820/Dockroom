# 03 · 架构、数据与接口合同

## 部署与代码边界

```text
浏览器 ── HTTPS REST + SSE ── Hub（页面、身份、策略、任务调度、审计）── SQLite/WAL
                                ↑ 控制 WSS / mTLS ↑
                            Agent A             Agent B
                            授权根 A            授权根 B
数据：源读取 → 源出站 HTTPS PUT → Hub 有界流 → 目标出站 HTTPS GET → 同卷暂存 → 校验 → 发布
```

一个 Go 模块：`cmd/hub`、`cmd/agent`；`internal/{api,auth,scheduler,relay,store,protocol}` 中枢模块，`internal/agent/{fs,staging,journal,service}` 端点模块，OS 专属文件操作用 Go build tags 隔离。`web/` 用 React + TypeScript + Vite、TanStack Query/Table、原生 CSS 变量；路由不携带原生绝对路径。前端编译后嵌入 Hub。`spec/` 放 OpenAPI 3.1、协议 JSON Schema 与夹具；`deploy/` 放服务模板；`tests/e2e/` 放双 Agent 场景。前后端只共用契约生成的类型。Go、Node、依赖在 M0 选当时受支持版并锁定，不在方案里猜未来版本。

单实例 Hub + SQLite（本地盘，不放网络共享），不引入 Redis、对象存储、消息总线或 K8s。小团队负担低，代价是单点与单写者。控制链只发指令、心跳、ACK，不塞文件字节，避免慢盘阻塞目录请求。浏览器只读进度，不持有任务执行租约。Hub 持有流经的明文，不声称端到端加密。

## 路径和列表契约

机器定位符为 `{nodeId,rootId,parts:["十一月","封面.tif"]}`；`parts:[]` 表示根。端点回传 `nativePath` 仅展示，`version` 为不透明并发令牌（卷/文件标识、大小、修改/变化时间等可用信息），不是内容哈希。键入绝对路径由本端根匹配服务转回定位符，不能当命令或任意文件读入口。根换物理位置必须分配新 rootId；卷和根目录身份变化需管理员本地重新确认。

`list` 返回 `entries[{name,kind,sizeBytes,mtimeUtc,version,actions}], nativePath, nextCursor, listingId`。目录与普通文件分组，同组按确定性名称键及原名打破平局；Agent 构造短期排序快照，游标绑定用户、目录、筛选和快照，默认 200 条/页。到期返回 `LIST_EXPIRED` 并刷新；筛选作用于整个快照而非已加载项目。限单目录 10 万项，超限显示原因；多选跨页按真实引用，不提供不明确的“全选全部结果”。

## 浏览器 API 草案（统一 `/api/v1`）

| 动词与资源 | 要点 |
|---|---|
| `POST /session`、`DELETE /session`、`GET /me` | 登录、退出、当前身份与能力；会话 cookie |
| `GET /nodes`、`GET /nodes/{id}/roots` | 权限过滤后的节点、根、只读属性、在线状态 |
| `POST /fs/list`、`POST /fs/resolve` | 传结构化定位符或本栏输入，返回带展示路径的条目或授权定位符 |
| `POST /plans` | `action, sources[{ref,version}], destination?, conflictPolicy`；生成归属当前用户的清单，返回 202 与 planId；`GET /plans/{id}` 轮询扫描，ready 后返回数目、字节、冲突、风险及到期时间（10 分钟） |
| `POST /jobs` | `{planId}` + `Idempotency-Key`，成功返回 202 `{jobId,state}`；相同用户+键+正文返回同一结果，键重复但正文不同返回 409。计划单次消费、可重取既有任务；过期重建 |
| `GET /jobs`、`GET /jobs/{id}`、`GET /jobs/{id}/items` | 游标分页；逐项返回两端路径、状态、已确认字节、错误码与恢复建议 |
| `POST /jobs/{id}/cancel`、`POST /jobs/{id}/retry` | 取消是请求不是回滚；重试先产出新 plan，确认后为新任务，保留父任务关联 |
| `POST /fs/mkdir`、`POST /fs/rename` | 只允许单级名称；同样带幂等键，生成可追踪的短任务，检查父目录和旧项身份 |
| `GET /trash`、`GET /events` | 回收清单；SSE 按用户过滤，带单调事件 ID，断流用 Last-Event-ID 补齐；超保留窗口则重新拉任务快照 |
| 管理端 `POST /enrollments`、`POST /nodes/{id}/revoke`、`PUT /users/{id}/grants` | 仅管理员；颁发一次性接入码、撤销节点、更新授权并记录审计 |

`action` 首版为 copy、trash、restore；删除用来源引用，恢复用 trashId，目标固定原父目录。例：`POST /plans` 的复制正文：

```json
{"action":"copy","sources":[{"ref":{"nodeId":"win-01","rootId":"out","parts":["十一月","封面.tif"]},"version":"v17"}],"destination":{"nodeId":"lin-01","rootId":"archive","parts":["2026"]},"conflictPolicy":"skip"}
```

错误体统一 `{code,message,requestId,retryable,itemId?}`；未登录 401、授权不足 403/隐藏对象 404、版本/重名 409、非法名称 422、离线 503、目标空间不足 507。异步任务的走盘错误体现在逐项结果，不能以请求 202 当作操作成功。

## Agent 协议与控制负载

Agent 主动登录设备专用监听器；HELLO 宣告协议 major/minor、OS、卷能力、可用根；不兼容 major 必须拒绝任务并提示升级。心跳 15 秒，45 秒无响应判离线，指数退避加抖动上限 60 秒。控制信封 `protocol,requestId,nodeId,jobId?,itemId?,attempt,leaseEpoch,deadline,op,payload`；响应携 requestId、结果或结构化错误。操作白名单为列目录/统计/清单/读取分片/写暂存/核验/发布/回收/恢复/查询凭据，不提供 shell、插件执行或任意 URL 转发。Agent 校验节点归属和本地根权限；拒过期租约和旧 epoch。

## 传输引擎：以目标落盘为进度依据

1. Hub 校验用户权限并扫描树，记录清单、版本、两端根、目标冲突策略；给每个顶层项和其叶文件分配稳定 ID。预检不预留空间，执行期重新检查。默认每节点 1 个大流、Hub 共 4 个；目录元数据与心跳独立。持久化租约、占用顺序按节点 ID 排序，避免相反方向任务互锁。
2. 目标为项创建同卷私有 staging。按文件顺序传，分片默认 8 MiB。先让目标 Agent 以 mTLS GET 等待指定块；目标就绪后源 Agent PUT 同一块；双端均访问 Hub 固定域名。短时授权绑定节点身份、角色、job/item/file、epoch、offset、length 和过期时间，不写入 URL 查询串。Hub 逐段转发，有背压、最大长度、连接等待/空闲超时；不把整文件或整片载入内存，不做离线中转仓。
3. 源端计算分片 SHA-256，经控制链报给 Hub；目标独立算并核对，写入 staging、同步数据、事务持久化块进度后才 ACK。失败块从约定偏移重写；恢复后以目标端已落盘连续前缀为准，重新核对身份、清单、租约、源版本；超出持久化水位的尾部不信任。源变更则停止，不能把新旧内容拼起来。
4. 源端对同一版本完整内容计算 SHA-256（恢复时必要则重读）；目标从暂存重新完整读取计算并比对。再核对树名、层级、空目录和文件计数，应用支持的 mtime。仅当整项通过才发布。无覆盖同卷原子重命名必须用平台能保证的原语；不支持则阻止写操作，不能用“先检查存在再普通 rename”。并发撞名时返回跳过或重新独占取名。
5. 提交前持久化 intent（目标名、预发布文件身份与摘要）；提交后同步可用元数据、写本地 receipt，再回报 Hub。ACK 丢失时按 intent、receipt 和文件身份核对，不能仅凭同名文件判成功；结果不确定进入 `needs_review`，禁止盲目重放。目录批次不承诺整体事务；成功过的顶层项不回滚。同节点复制首版复用相同语义与通道，单独计一次节点租约，后续才做本地快路径。

## 状态、存储与保留

计划 `scanning → ready | blocked | expired`；任务 `queued → running ↔ waiting_node → succeeded | partial | skipped | failed | cancelled | needs_review`。取消中另标 cancel_requested。项内 `pending → staging → verifying → committing → committed`，逐项亦可 skipped/failed/cancelled/needs_review。全跳过为 skipped；成功夹杂跳过/失败/取消为 partial；任何未知提交优先 needs_review。等待离线超 24h 转 failed，可手动重试。重试暂时性网络错误限 3 次，权限、磁盘满、版本变化不自动重试。重启先核对，再发租约，不以超时推断文件未提交。

| 持久化位置 | 表或对象（核心键） |
|---|---|
| Hub / 身份 | users、sessions(tokenHash)、nodes(certSerial,status)、roots(nodeId,rootId)、grants(userId,nodeId,rootId,rights)、enrollments(tokenHash,expires,used) |
| Hub / 工作 | plans(owner,expires,manifestVersion)、plan_entries、jobs(owner,planId UNIQUE,parentId,state,epoch)、items(jobId,sourceRef,destRef,state,bytes,receipt)、files(itemId,relpath,size,version,digest)、leases、idempotency(owner,key UNIQUE per owner,bodyHash,result) |
| Hub / 记录 | events(id,jobId,owner)、audit(actor,action,ref,result,requestId,utc)、trash_index(nodeId,trashId,owner,expires)。队列按 state/created 索引，事件按 jobId/id，浏览和记录均分页 |
| Agent / 本地 | 受 ACL 保护的身份和根配置；journal 记录 intent、分片水位、校验、最终 receipt、回收清单。暂存/回收区位于每个根的同卷私有目录，排除在浏览/选择/复制之外；禁在区内登记另一个根 |

清单有上限（默认每任务 10 万项、深度 128），超限明确拒绝并建议拆批。完成后清单仅按历史保留期清理；中断暂存仅在租约终结且已核对后按策略清理。遇到历史库、Agent 凭据或本地台账不一致，优先保留文件，降级只读待核查。
