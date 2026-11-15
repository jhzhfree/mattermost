# AI-TAgent / Mattermost 集成设计

状态：设计基线，已增加可离线构建/测试的本地请求受理插件；不代表已经接通、部署或通过端到端验收。

本设计基于 fork `jhzhfree/mattermost` 的 `feature/ai-tagent-integration`，
代码基线 `a69dda3fe70c50a68591539d86095b18031abc7f`。保留上游品牌、许可和核心行为。

## 产品边界与首个场景

AI-TAgent（Octop / 轻智能体系统）负责数字员工、目标规划、执行、审批、证据和审计；
Mattermost 负责真人与数字员工协作入口；AKB 负责授权知识归档；模型网关负责模型调用与费用。
先采用外部适配器和 Bot REST API，再考虑独立插件，不先修改 Mattermost 核心。

首个验收场景为“运维数字员工晨检”：真人提出目标 → Octop 规划允许列表内的只读检查 →
审批执行 → 保存结果及证据 → 主管验收 → 审批指定频道与摘要 → Bot 发布报告链接 →
独立授权后归档知识。聊天文本、外部素材和模型生成的指令都不能自行授予执行或外发权限。

## 身份与权限

- 真人使用自己的 Mattermost 用户；数字员工使用专属 Bot，不借用真人 token 或展示名称冒充真人。
- 建立受控绑定：Octop 组织、用户、数字员工、Mattermost 实例、team/channel、Bot user ID。
- 区分 `human_requested` 与 `scheduled`。记录实际发起人、数字员工、Bot、run、approval、目的地和报告摘要哈希。
  定时任务记录其授权来源，不捏造真人操作。
- Bot 默认最小权限，不授予系统管理员。每次提交、执行、审批、发送均重新校验相关权限；
  频道成员身份不自动等于 Octop Agent 访问权。
- 初期审批跳转 Octop，复用登录和权限链；后续交互按钮必须验证当前用户、审批版本和防重放条件。

## 接口和事件契约

建议独立连接记录保存固定服务地址、凭据引用与状态；绑定记录限定允许的频道。
凭据保存在服务端秘密存储，不进入通用可回显配置、日志、错误消息或浏览器响应。

外发采用持久化 outbox：以实例、run、报告版本、目的地和审批绑定幂等键，记录发送状态及远端 post ID。
网络超时且远端结果不明确时标记 `delivery_unknown`，先对账，不盲目重发。
报告、频道或绑定变化使原审批失效；撤权、Bot 禁用和凭据失效均应阻断。

后续插件通过显式 `/aitagent` 命令接收目标，并展示任务卡片/详情栏。
事件包含来源实例、事件 ID、可信用户与频道 ID、关联 run 和类型；入口认证和用户映射完成后才能进入任务系统。
过滤自身 Bot 消息，防止消息回环；重复、乱序和重放事件不得重复执行任务。
仅在 Mattermost 管理的插件 HTTP 入口信任其认证头，不信任公网请求伪造的同名头。

## 代码依据

以下路径和行为来自上述固定基线：

| 路径 | 行为及集成影响 |
| --- | --- |
| `server/channels/api4/bot.go` | Bot CRUD、创建权限和 `EnableBotAccountCreation` 检查；Bot 不能创建 Bot |
| `server/channels/app/authentication.go` | Bot 交互登录被拒绝 |
| `server/channels/api4/user.go` | `createUserAccessToken` 校验 token 创建权限和目标用户/Bot权限；OAuth session 不能创建 token |
| `server/channels/api4/post.go` | REST 发帖将 `UserId` 设置为会话用户，不能由客户端冒充真人 |
| `server/channels/app/webhook.go` | outgoing webhook 限公开频道；incoming webhook 展示名可覆盖，不应作为身份凭据 |
| `server/public/plugin/hooks.go` | `ServeHTTP`、`ExecuteCommand`、消息提交前/后 hooks；插件自己的帖子也触发 hooks |
| `server/public/plugin/api.go` | `RegisterCommand`、`GetChannelMember`、`CreatePost`、`EnsureBotUser` 可用于独立插件 |
| `webapp/channels/src/plugins/registry.ts` | 频道按钮、自定义消息组件、右侧详情栏和 AppBar 扩展点 |

插件 API 能力不替代业务授权。私有频道与 DM 不采用 outgoing webhook 作为统一接入方式。

## 隐私与审计

只发布已经批准的最小摘要及受权限保护的链接，默认不复制原始内部响应、凭据或聊天历史。
频道权限与报告权限分别校验；知识归档是单独授权步骤，不因发布到频道而自动归档。
审计保留身份映射、操作模式、授权来源、内容哈希、目标、时间、run、审批和发送回执；
审计中避免存储秘密和不必要的正文。留存、撤销和删除策略在上线前明确。

## 分期及验收边界

1. **准备与契约**：配置脱敏、绑定校验、只读 readiness；不得声称已经连接或发消息。
2. **受控发布**：人工批准后的单频道报告外发、outbox、回执、失败恢复；使用隔离测试实例和测试频道。
3. **协作入口**：显式命令、可信身份映射、任务链接/卡片、防回环和重放。
4. **持续工作**：授权定时任务、领导查询、独立知识归档和多角色协作。

发布门槛：真实测试实例跑通完整场景，验证跨组织拒绝、撤权、Bot 禁用、token 失效、内容变更后审批失效、
超时不重复发布、凭据不泄漏和审计可追溯。模拟 HTTP 测试不等于真实 Mattermost 端到端验收。
生产外发、部署、许可接受和共享变更均需符合相应授权范围。

## 构建和许可边界

- `server/go.mod` 和 `server/public/go.mod` 指定 Go 1.26.10；`webapp/package.json`
  指定 Node 24.11.1、npm 11.6.2；开发 Compose 使用 Postgres 15。
- `server/Makefile` 从独立 `../../enterprise` 目录发现企业模块；仅 fork 本仓库不获得该模块或企业功能许可。
- 遵守 `server/AGENTS.md`，依赖整理使用 `make modules-tidy`，不直接执行 `go mod tidy`。
- `LICENSE.txt` 区分官方编译包与自编译源代码：后者为 AGPL v3 或商业许可，所列
  `server/templates/`、`server/i18n/`、`server/public/`、`webapp/` 目录有 Apache 2.0 例外。
  商标规则独立适用；不能将官方编译包的许可说明直接用于自编译 fork。
- 优先外部 API 集成、最小 fork 差异；商业分发和源码改造上线前完成许可评审，不移除或绕过企业许可检查。

以上为集成设计基线；当前局部实现及其边界见下节，不声明许可证已采购或生产环境已验收。

## 0.1 本地协作入口

`server/cmd/aitagent-plugin/` 实现独立的 `/aitagent request <goal>` 和
`/aitagent status <receipt-id>` 插件入口，详见该目录 README。它默认关闭，限定
管理员配置的组织、数字员工、专属 Bot、团队、频道及真人受理白名单。每次请求和查询
重新检查真人/目标 Bot 状态、频道归属、成员关系及真人发帖权限。

插件使用 Mattermost 服务端命令 hook 的已认证账户身份，并将目标正文视为不可信数据。
受理记录分开保存真人、数字员工、Bot、来源、目标哈希和绑定，状态固定为
`awaiting_octop_authorization`。`user_requested` 仅表示非 Bot 账户提出目标，不证明真人正在键盘前操作
（用户会话/token 也可能被自动化使用），不声称由人实际执行。后续人工代操作与自动执行
必须另记每步实际操作者及授权来源。

本阶段只有本地 KV 受理和仅本人可见的回执，七天过期；组织/数字员工映射尚未向
Octop 验证，不授予其业务权限。相同命令 dispatch 原子去重，Bot 操作者拒绝，
不监听普通聊天或 Bot 消息。没有外发客户端、执行器、审批、调度或真实 Octop 队列。
新的命令提交会有新的服务端 trigger，不能据此声称已实现跨请求或永久幂等。

离线单元测试和插件包构建不替代真实实例安装、RPC/数据库及端到端验收；
本地受理不是 1.0 完成，也不代表 Octop 发布预览已经接通真实发送。

**命令输入与审计保留提示（0.1.1）**：不得在斜杠命令中输入密钥或敏感信息。
`api4/command.go` 在插件处理前将完整 `CommandArgs` 加入服务器审计记录；
`CommandArgs.Auditable()` 包含完整 command/goal，插件拒绝也不能撤回已捕获的内容。
七天仅是插件 KV 回执的过期规则，不约束服务器审计、收集器、导出或备份的保留。
内置审计文件目标默认 `max_age=0`、`max_backups=0`，不自动按年龄或份数清理旧文件。
插件 README 提供源码核实的配置边界、离线校验的独立示例及真实部署前的验证步骤；
自动补全、管理设置和所有命令响应均明确提示两套保留策略不同。
