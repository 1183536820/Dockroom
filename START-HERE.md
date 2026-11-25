# 给 GPT-6.2 Sol 的开工交接

以下文字可以直接作为下一次开发任务的首条消息：

> 在 `D:\IProject\跨服务器文件交互` 实现“渡柜”。先读 README.md、01-product.md 到 05-delivery.md 和 workbench.html。它们是已定的产品合同；样稿仅为视觉参照，不接它的内存数据做生产后端。先盘点当前代码与工具，保留现有工作，执行 M0，然后做 M1 的双节点只读纵向切片。每阶段留下可复现的启动步骤、测试、截图或日志摘要和 CHECKPOINT.md；再按 M2–M4 推进。不要把静态页面、以浏览器做中转或单机 mock 当作核心交付。生产目录、真实凭据与生产删除均不在测试授权内。没有第二种 OS 的实机时用夹具推进并标待验，不虚报完成。架构变更先写 ADR，说明动机、影响和替代验收；不要为追求功能数量删掉授权根、无覆盖提交、校验或恢复。

## 首个 checkpoint 应回答

1. 哪些系统和卷已实际验证，哪些只通过构建？每个端点支持哪些原子/隔离原语？
2. 一个新的开发者可否只看手册就启动一台 Hub、两个隔离 Agent，登录并看到两边的真实路径？
3. 当前可以通过哪些 A 编号？剩余阻碍是代码、实机条件还是产品决策？

技术参考入口（供实现时核对能力，不是已集成依赖）：[Go os](https://pkg.go.dev/os)、[Go net/http](https://pkg.go.dev/net/http)、[SQLite 一致性备份](https://sqlite.org/backup.html)、[Windows 文件命名规则](https://learn.microsoft.com/en-us/windows/win32/fileio/naming-a-file)、[Linux 路径受限解析](https://man7.org/linux/man-pages/man2/openat2.2.html)、[WAI-ARIA 文件表格键盘模式参考](https://www.w3.org/WAI/ARIA/apg/patterns/grid/)。
