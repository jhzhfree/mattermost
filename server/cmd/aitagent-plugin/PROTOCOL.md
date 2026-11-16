# Mattermost ↔ Octop 协议契合审查

状态：**BLOCKED，不能安全接线**。本文件包含已核对的现状和待双方实现/评审的协议草案；
草案不是 Octop 已支持的 API，也不是双方已经达成的协议。本轮不新增运行时接收器、
模拟服务、密钥提供者或发送器，不修改 Octop 工作区。

## 核对基线与可复现证据

- Mattermost 基线：`774b1220ffdfea556bb3b2132a0afe8e65446020`。
- Octop 开发分支 `feature/akb-octop-litellm-dev`，核对提交
  [`c9fd4021c47a8a06733a6787434fc7c60cbce91e`](https://github.com/jhzhfree/Octop/tree/c9fd4021c47a8a06733a6787434fc7c60cbce91e)。
- [机器可读快照](protocol/octop-contract.json) 记录八个现有路由、模型字段/约束、
  当前用户依赖和八个关键文件的 SHA-256。
- [检查器](protocol/check_octop_contract.py) 只读取已有本地 Git 对象，解析 AST；
  不导入/执行 Octop 模块，不发起 HTTP、不读取凭据、不 fetch，不运行服务器。
  未审查提交、源码/模型/路由漂移或可能的新接收入口都要求重新审查，不能自动判为兼容。

```sh
python3 protocol/check_octop_contract.py \
  --git-dir /path/to/local/octop/.git \
  --revision c9fd4021c47a8a06733a6787434fc7c60cbce91e \
  --report /tmp/octop-contract.json
```

默认退出码 **2** 表示已复现“当前不兼容”，**1** 表示检查失败/需要重新审查。
`--verify-known-blockers` 仅把“已复现已知阻断”改为退出码 0，便于回归；报告仍为
`compatible=false`，绝不表示连接获准。检查器不负责下载缺失 Git 对象。

这是源码契约核对，不是活体 JWT、数据库授权、TLS 或 HTTP 集成测试。检索未发现
本插件 proof domain/拟议 ingress，不是对任意第三方插件或未知部署的能力断言。

## 当前 Octop 实际支持什么

以下表的完整路径、方法和约束也在 JSON 快照中：

- `W=/api/organizations/{organization_id}/digital-workers/{worker_id}`
- `P=/api/organizations/{organization_id}/scene-runs/{run_id}/mattermost-publication`

| 实际接口 | 输入及真实用途 | 为什么不能充当 Mattermost 请求接收器 |
| --- | --- | --- |
| `POST W/mattermost-readiness` | `expected_worker_version >= 1`；记录当前用户的本地检查 | 返回未绑定/未连接，不能证明 Bot 身份，更不受理 goal |
| `PUT W/mattermost-intent` | worker version、`expected_revision >= 0`、instance alias、26 字符 team/channel/bot、draft/revoked | 保存未验证的目标配置；不是请求交接、服务认证或执行授权 |
| `GET W/mattermost-intent` | query `expected_worker_version >= 1` | 只读配置，不能按 delivery key 查收件结果 |
| `GET P/candidate` | 已验收报告对应的 run；返回四个预览前置值 | 请求者身份和业务报告前提不同于插件的原始请求 |
| `POST P` | report SHA-256、worker version、intent revision、configuration revision | 从已存在的同 run 验收报告创建出站预览；不接受插件 proof |
| `GET P` | 读取原预览 | 没有对应入站 receipt/fingerprint 状态 |
| `POST P/approve` | `expected_publication_sha256`；独立审批人 | 只进入 `blocked_not_connected`，不是发送，也不是数字员工执行许可 |
| `POST P/revoke` | 同一个 publication digest | 撤销出站预览，不能取消插件请求或其接收端任务 |

四个现有请求模型声明 `extra='forbid', strict=True`。插件 `{payload,signature}`
信封的两个字段在这些模型中都是未知字段，同时缺少各自必填字段。检查器报告这一
结构性不兼容；它没有声称已实际发送请求得到 422。不得通过丢弃签名、把正文塞进
chat API 或把发布审批当执行许可来“接通”。

### 认证不是可替换的

当前路由依赖 Octop `current_user`：认证链验证 Octop JWT、会话期限/活动、会话
状态及账号；领域/仓储还重新检查组织、Agent、业务参与者及版本。发布预览依赖
同 run 成功报告和独立验收，审批不能由提议人/原请求者冒充独立审批人。

Mattermost Ed25519 proof **不是 Octop JWT**。MM user ID、Bot ID、数字员工标签或
客户端传来的 actor ID 都不能成为 Octop 登录身份。现有接口虽接受自身会话 token，
本对接不得借用真人 token 扮演数字员工，也不得把 token 放进 URL/query。发布路由
中的 JWT 用户/报告审批关系不能用于推断一个 broker 已具备服务权限。

`mattermost_verification.py:observe_bot_identity` 仍只接受精确类型
`httpx.MockTransport`；生产 transport 在创建客户端前被拒绝。独立秘密权限域/
执行器隔离没有完成。不能通过关闭 TLS 校验、固定共享密钥或改布尔值绕过该门禁。

## 待共同实现的最小入站协议草案（尚无对应路由）

目标仅为“可信受理、查询和取消”，不包含消息发布、任务启动或模型执行。以下路径
是**保留草案名**，不能调用当前 Octop 来试探，更不能伪装为已交付接口：

| 拟议操作 | 请求 | 成功结果与事务要求 |
| --- | --- | --- |
| `POST /api/integrations/mattermost/v1/intakes` | `Content-Type: application/octet-stream`，body 为现有 proof 信封原始字节，最多 32768 字节 | 201 新 pending；200 完全相同的已保留身份；同事务写身份、状态与审计。永不返回执行许可 |
| `GET /api/integrations/mattermost/v1/intakes/{delivery_key}` | 无 actor/org/body 声明；服务身份决定查阅范围 | 返回持久原 key、fingerprint、state、revision、source_version；可返回终态压缩摘要。查询不依赖过期的请求 proof |
| `POST /api/integrations/mattermost/v1/intakes/{delivery_key}/cancellations` | JSON：`operation_id` 为小写 64 位 hex，`expected_revision` 为 1–8 整数；拒绝额外字段 | 服务端独立取当前源取消证据及本地授权；事务写 cancelled 与审计；重复终态不复活；expired 保持 expired |

拟议 JSON 响应的公共字段为 `protocol_version:1`、`delivery_key`、`fingerprint`、
`state`、`revision`、`source_version`、`execution_authorized:false`；所有响应
`Cache-Control:no-store`，不得回显 goal、proof、密钥或内部 SQL/RPC 错误。
`source_version` 使用十进制字符串编码，避免 JavaScript 的 53 位整数精度问题；
接收端必须验证 1..9223372036854775807。现有 Go 内核还不是这一 HTTP 序列化实现。
压缩终态摘要必须显式区分摘要与完整事件，不能假装仍有已清除的明细。

源侧需要另行实现已认证的 active/cancellation 查询适配器；当前只有私有 Go
`handoffSourceReader` / `handoffCancellationReader`，没有 HTTP 路由。返回必须来自
当前授权及主库 CAS 确认，绑定原 receipt、目标、有效期、key/fingerprint 和版本，
而非客户端自报或缓存的旧签名。签发/保留接口尚无安全 broker 权限模型，故本轮
**不定义可公开调用的签名端点**。

### 身份、字节、幂等与版本边界

1. 传输必须验证服务身份和 TLS；broker 到 source/receiver 的允许范围由服务端
   注册表绑定。注册表必须约束 instance、audience、组织、数字员工、MM actor、
   Bot、team/channel 和 Octop actor/worker。具体服务凭据签发、轮换、吊销、秘密
   隔离及 broker 部署协议仍待双方实现；当前没有可接受的生产认证 profile。
2. `internal/handoff/proof.go` 的 v2 签名输入是
   `mattermost-octop-intent-proof/v2\0` 加原 payload 字节。信封及 payload 必须是
   该实现规定的 Go JSON 字节表示，包含字段顺序和 HTML 转义；它不是 JCS/JWT。
   重复/未知键和替代编码拒绝。proof 不超过 120 秒且不超过原回执到期，源有效期
   为毫秒、proof 时间为秒。Octop 尚未实现此格式；跨语言测试向量通过不等于接线。
3. delivery key 为现有代码对 `["mattermost-octop-delivery/v1",instance,audience,receipt]`
   Go JSON 字节的 SHA-256；fingerprint 同时绑定 intent 和接收端映射身份。具体字节
   以现有公开向量为准，不自行用“等价 JSON”重新排序。重签名/换 key 不创建新身份。
4. source organization/employee 是保留原 UTF-8 字节的 opaque ID，不按显示名或
   Unicode 归一化自动映射。当前 proof 没有 Octop worker/configuration revision，
   也没有 restore epoch；现有 sourceVersion 不能证明数据库从未回滚。实际权限
   快照、映射 epoch、恢复协议必须共同定稿，不能把这些字段偷偷塞入 v2。
5. 匹配 key 且不同 fingerprint 必须冲突，不能覆盖；旧终态不能因清理/重发复活。
   事务只对本地存储成立；源取消与接收提交不存在跨系统原子性。取消先到时可以
   尚无收件行，持久重试必须等待原身份可确认，不能凭 404 发明“未投递”。
6. `pending_authorization`、`authorization_observed`、`execution_observed` 都不是
   可执行许可。必须另做当前权限、来源和目标检查；本草案不提供执行端点。

### 错误、认证和重试约定

| 情形 | 必须采取的行为 |
| --- | --- |
| 401／无有效服务认证 | 停止业务调用，走受控凭据恢复；不得改用真人 JWT、MM proof 或自报 actor |
| 403／当前权限撤销 | 保留原身份，拒绝受理/传播；不能提权、重置队列或恢复旧审批 |
| 404／记录未找到 | 对未确认投递仍是未知；可能迟到、滞后或范围不可见；不得自动新建请求 |
| 409／不同 fingerprint | 拒绝覆盖和重放；人工核对原身份。相同身份的 revision 冲突可先重新查询，再重验当前权限 |
| 410／原身份 cancelled 或 expired | 保持终态；新 proof 和更新到期时间都不能重新受理 |
| 422／结构、字节编码或字段无效 | 拒绝，不按原样无限重试；返回脱敏错误，不回显凭据/正文 |
| 429、503、超时、断链或 commit_unknown | 不推断“没有写入”；查询原 key/fingerprint。没有独立确认前禁止自动重新投递 intake |
| 取消调用丢应答 | 独立查询 receiver；仅观察 cancelled/expired 才记录历史终态确认，响应成功或 lease 到期都不够 |
| 16 次取消 claim 耗尽或证据失效 | 保留 attention/未解决状态；无删除/换号/无限重置。需要另行授权的人工恢复流程 |

当前 Octop 发布服务实际将 forbidden/not_found/stale/conflict/storage 错误映射为
403/404/409/503，路由校验错误为脱敏 422；这与拟议 intake 状态机不是同一协议。
其 `unknown` 发布记录不能通过 approve/revoke 恢复发送。不得据相同 HTTP 状态码
就宣称两个系统语义兼容。上述拟议 401/410/429 等行为尚未在 Octop ingress 实现。

## 验收责任与明确阻断

| 验证项 | 本轮可验证的证据 | 仍需真实接收端/环境 |
| --- | --- | --- |
| 路由、模型与 proof 是否相容 | 本地 Git 校验和、AST 路由/认证依赖、严格模型与信封字段不匹配 | Octop ingress 实现和 HTTP 契约测试 |
| 签名/字段错误与重放规则 | 现有 Go/Python/OpenSSL 公共向量及 Go 内核测试 | Octop 真实解码器、事务迁移及持久幂等 |
| 当前认证与撤权 | 源码依赖/仓储规则审查；已有受限角色隔离回归 | 真实 JWT 生命周期、broker 服务身份、组织/Agent/员工版本并发验证 |
| 未知结果与取消重试 | 当前隔离 DB 调度/故障/新进程测试 | 两个真实服务、TLS、实际存储故障/恢复和源证据留存 |
| 人工与自动操作来源 | 现有源 actor/Bot 绑定和 operation_mode 约束 | Octop 服务主体授权与审计字段映射，不得冒充真人 |

**不能在仅修改本仓库的前提下交付安全的真实最小接口**：Octop 缺入站协议和服务
身份适配器，生产核验硬门禁仍在。该结论是明确阻断，而不是实现空占位端点。
下一项需要 Octop 侧授权实施：在隔离 broker/凭据边界确认后，先共同完成只落
pending、不执行、不发消息的 intake + original-identity inspection 接口及真实
HTTP/数据库契约测试；随后才验证取消和运维。现有出站发布审批保持原用途。

本轮不声称做过真实 HTTP/JWT/服务重启测试，不修改 Octop、许可证或安全门禁。
完整的环境隔离和两端验收步骤继续见 [DEMO.md](DEMO.md)。
