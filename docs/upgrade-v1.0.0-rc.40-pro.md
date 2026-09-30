# rc.25 Pro → rc.40 Pro 升级与审阅说明

日期：2026-09-30（Asia/Shanghai）。依据：[2026-09-22 差异追踪](diff-trace/2026-09-22-new-api-v1.0.0-rc.40-new-api-pro-v1.0.0-rc.25.md)。

## 范围与审阅入口

- 独立分支：`upgrade/v1.0.0-rc.40`，从官方 rc.40 `0aec08fee811ec6136828fda790551b49e410301` 线性重放下游补丁；不改写旧发布分支或标签。
- 原工作分支：`release/v1.0.0-rc.25-pro`，原 HEAD `eaeb4406f`。除 pro.5 外，保留其后的 Prometheus 并发指标补丁。
- `git diff v1.0.0-rc.40..upgrade/v1.0.0-rc.40` 审阅下游改动；`git diff eaeb4406f..upgrade/v1.0.0-rc.40` 审阅完整升级；`git log --reverse --oneline v1.0.0-rc.40..upgrade/v1.0.0-rc.40` 查看分层补丁。
- 本分支为本地待审阅升级，不代表已发布或已完成生产迁移。现有差异追踪目录未纳入自动提交。

## 分阶段处理结果

### 1. 数据库与账务基线

保留 rc.40 的迁移实现、数据库方言适配、唯一性修复、审计表和 64 位钱包规则。rc.25 的原子充值/预扣已经属于上游，不重复覆盖新计费实现。

报告之外需要特别注意：启动前检查 `users.quota`、`used_quota`、`aff_quota`、`aff_history`。MySQL/PostgreSQL 的这些列必须为 BIGINT，32 位列会在 AutoMigrate 前被拒绝。64 位 rc.25 安装可能已经使用 BIGINT，先检查实际 schema，不必盲目 ALTER。不要通过 `SKIP_64BIT_QUOTA_SCHEMA_CHECK` 绕过检查。

### 2. 请求策略与渠道选择

保留 rc.40 的渠道约束、请求策略、模型映射、分组重试、计费快照及请求策略事件。Pro 租约在每次标准 Relay 选中渠道后、真正请求上游前获取，在该次请求结束后释放；跨渠道重试重新获取租约。饱和直接 429，Redis 不可用直接 503，均停止重试，不自动禁用渠道，最终失败走现有退款链。

- 保留 `setting.max_concurrency`、可空 `in_flight`、Redis/process 作用域、一分钟采样、手动刷新和 Prometheus。
- 渠道表单直接适配 rc.40 编辑器，保留插件多绑定及 Responses WebSocket 设置；复用现有 Form/Input、DataTablePage、ChannelCard 和查询基础设施，不引入旧版编辑器。
- 后台采样错误沿用 rc.40 的共享 QueryClient，在已有配置时保留页面、清空有效计数并显示一处提示。首屏错误仍走共享错误处理。
- HMAC 使用鉴权后的 `RelayInfo.UserId/TokenId`，保留来源隔离、缺失身份省略、渠道测试不发送以及非法占位符拒绝行为；多实例必须共享稳定 `CRYPTO_SECRET`。
- 修复普通 HTTP/multipart 转发等待响应头时的取消传播，入站取消/租约取消能够中断上游等待并释放占位；本地取消标记为不可重试，避免误禁用健康渠道。审阅后补齐响应头之后读取响应体的取消处理：控制器在重试决策及渠道故障处理前终止，保留既有失败退款流程。真实 HTTP 回归覆盖响应头前取消及响应体读取时取消。
- 音频转录/翻译继续接受 multipart `stream=true`，由 SSE 处理器转发。

**计数边界不扩大：** `/v1/realtime`、新 Responses WebSocket 入口、任务插件（包括接管 Images/Responses 的协议路径）、Midjourney/Suno、渠道测试、模型列表及余额查询不计入标准 Relay 并发。插件 Images 请求可能从普通 Relay 改走任务路径，启用前需要重新评估容量；本次不把任务 ID 生命周期混入 HTTP 租约。

### 3. 计费与任务插件

保留 rc.40 的 `trust_quota_usd`、预扣倍率、任务 usage expression、插件专属价格、冻结快照、幂等结算/退款及溢出保护。预扣倍率仅影响保留额度，不改变最终实际计费。`trust_quota_usd` 上游默认 10 USD，升级灰度应显式配置 `quota_setting.trust_quota_usd=0`，再单独评估信任跳过预扣。

任务提交保留所有 2xx 成功响应规则，随后仍需解析有效任务 ID/结果，不能将 202 直接视为结算完成。插件异步任务和后台结算保留上游独立生命周期；客户端断开等待不等于撤销已持久化任务。保留 8 MiB 上传限制、按版本/hash 同步、MySQL LONGTEXT、媒体转换及 enum 收窄后的定价编辑。

七种 locale 逐 key 合并：保留全部 rc.40 翻译与 Pro 的 13 条文案，并修正旧 Pro 八条采样文案位于 `translation` 命名空间之外而无法加载的问题。

### 4. 发布基础设施

保留独立 RelayKit 门禁、Artifactory 7.71.21 的最终 scratch 重打包、GHCR 多架构 OCI/provenance 与签名门禁。使用 rc.40 的 Bun 镜像版本/digest 和当前上游 action 引用；Release 正文更新至新迁移要求。没有触发发布、上传镜像或创建 tag。

## 生产迁移步骤与风险

1. 停止写入并排空已接受的异步任务；保存应用镜像 digest、配置、options、插件源码/版本、渠道 setting/header_override、用户/Token 余额与对账快照。同步备份主数据库和独立日志数据库，并验证恢复。采样计数为零不是排空证明。
2. 检查上述四个钱包列。确需扩容时，在停写副本上执行并测量 DDL，再安排生产窗口。PostgreSQL 可使用 `ALTER TABLE users ALTER COLUMN quota TYPE BIGINT`，对其余三个列分别执行；MySQL 使用 `ALTER TABLE users MODIFY COLUMN quota BIGINT ...`，必须按 `SHOW CREATE TABLE users` 保留原有 NULL/default 等属性，不直接复制省略属性的示例。
3. 检查 options 重复/空 key、Token key 和 prefill group 唯一性。rc.40 会修复唯一性；options 冲突值采用读取结果的最后一行，不能当作业务上的“最新值”，应提前人工消歧。保存 `options_legacy_*` 备份表；检查迁移日志，options 迁移错误可能仅记录日志而继续启动。
4. 由单个迁移节点先启动，暂不开新 WebSocket/实验插件流量。检查用户钱包、options、Token/prefill 唯一性及审计表。重启一次，确认 schema 稳定，再接入其他节点。
5. 若从中间版本已有 `task_plugins`，MySQL 的 `source/icon TEXT → LONGTEXT` 可持锁/重建表；rc.25 直接升级则新建插件表。单独测量表大小、锁等待、DDL 时间、磁盘及 `max_allowed_packet`。反向代理请求限制也必须容纳源码及 JSON 包装，不能仅设置为恰好 8 MiB。
6. 在 `trust_quota_usd=0` 下逐笔重放有限/无限 Token、分组重试、失败退款、重复充值回调、音频 SSE、并发超限、客户端取消和 Redis/Sentinel 故障。账务核对通过后再单独启用任务插件和 Responses WebSocket，验证 201/202、无效任务 ID、后台失败退款、上传/激活、Images metadata 和严格客户端。
7. 主题偏好从 cookie 改为 localStorage，旧偏好可能重置一次；按预期向用户说明。渠道客户端需处理 `in_flight=null`，禁止把未知显示为零。

**回滚：** 停止新写入，使用已验证的旧镜像与同一时间点的数据库/配置快照整体恢复。不要让 rc.25 与 rc.40 长时间混写，也不要将已存大源码列缩回 TEXT。若新版本已有真实计费，先对账并保存增量，禁止直接恢复旧快照造成已接受任务/余额丢失。插件协议及审计变化使本次不能沿用 pro.5“无需恢复数据即可回滚”的说明。

## 验证记录

测试日期为 2026-09-30，Go 1.26.1，Bun 1.3.14，Node 26.5.0。CI 仍使用上游 Bun 1.4.0。测试均针对隔离测试服务，不访问生产数据。

- 根模块：`GOWORK=off go test -p 1 -ldflags='-s -w' ./...`；`GOWORK=off go vet -p 1 ./...`；实际构建并运行了升级二进制。
- 独立 RelayKit：在 `relaykit/` 执行 `GOWORK=off go test -p 1 ./...`、`go build -p 1 ./...`、`go vet -p 1 ./...`，通过。
- race：`go test -p 1 -race ./common ./service ./controller ./relay/channel -run 'Sentinel|ChannelConcurrency|ContextHMAC|RelayCancellationBeforeHeaders' -count=1`（首次使用 `-ldflags='-s -w'`）；最终取消修复另重跑 controller/channel。
- 数据库矩阵：`TEST_MYSQL_DSN=... TEST_POSTGRES_DSN=... go test -p 1 ./model -run 'Migration|Migrate|DatabaseMatrix' -count=1 -v`；另执行新增 `TestTaskPluginPayloadMigrationDatabaseMatrix`，覆盖旧 TEXT 数据保留、完整 8 MiB 往返和再次迁移不产生 DDL。
- 实际启动：从 `abfde8c0b` 构建 pro.5，分别创建三种旧库，写入用户余额/Token/渠道并发/HMAC/options 样本，再启动 rc.40 两次。另建三种全新库启动两次；MySQL/PostgreSQL 配置独立日志库。全部成功，列 schema 快照稳定，业务样本不变。显式构造 32 位钱包列时启动按预期拒绝，迁为 BIGINT 后成功。
- 数据库版本：MySQL 8.0.46、PostgreSQL 16.15；SQLite 3.50.4（项目 `glebarez/sqlite` 驱动）。未演练最低支持版本 MySQL 5.7.8/PostgreSQL 9.6、生产规模 DDL 或 ClickHouse 日志库。
- 实际 Redis/Sentinel：隔离 Redis 7 集群中运行 `TestChannelConcurrencyRealRedis` 与 `TestChannelConcurrencySentinelFailover`。主库关闭后约 6.6 秒恢复，双客户端看到原租约，30 秒续租成功，新增/释放后计数归零；故障期间指标未知而非伪零。
- 前端：`NODE_OPTIONS=--no-experimental-webstorage bun run test --maxWorkers=2`、`bun run typecheck`、`bun run build`；受影响文件 oxlint 与保留版权头的 oxfmt 检查。具体最终计数见下方最终门禁。

首轮并行 Go 链接因本机磁盘不足失败，后改串行及移除可再下载的 Go 模块压缩缓存；这不是源代码通过的证据，以上记录以修复后的复跑为准。初轮前端旧错误配置断言已替换为真实用户行为回归，未保留失效兼容路径。

本地完整日志在 `/tmp/rc40-*.log`，实际启动日志在 `/tmp/rc40-startup/`。这些是本次临时证据，不作为版本库必需文件。

### 最终门禁

- 根模块全量 Go 测试通过；最终取消修复后，`GOWORK=off go test -p 1 ./controller ./relay/channel ./model` 复跑通过，全库 `go vet -p 1 ./...`、`go build -p 1 ./...` 通过。
- 响应体取消修复后，`GOWORK=off go test -p 1 ./controller ./relay/channel ./relay/channel/openai ./service`、controller vet，以及取消/并发错误契约定向 race 通过。新增真实 Relay 回归验证单次尝试、不记录渠道故障、500 自动禁用规则下渠道仍启用、并发租约归零。
- 独立 RelayKit test/build/vet、相关 race、三数据库迁移及实际启动矩阵、Redis/Sentinel 故障切换均通过。
- 前端 **168 个测试文件、2113 项测试全部通过**；typecheck、生产 build、受影响文件 lint/format 通过。测试环境的 `scrollTo` 未实现提示未影响测试结果。
- 工作流 YAML 解析、`git diff --check` 通过。以上为本地门禁，不代表下列生产及镜像发布门禁已经执行。

### 尚未覆盖的生产风险

已在用户授权的 `new-api-2` 测试环境完成 rc.25-pro.3 数据副本升级/恢复演练、真实文本请求计费核对、下游补丁专项验证及登录后的管理 UI 验收，详见 [测试环境验收记录](validation-rc40-new-api-2-2026-09-30.md)。当前运行的是基于旧运行时镜像替换二进制的测试镜像，不能视为正式 Dockerfile 发布验证。

尚未进行目标生产数据副本对账、生产并发压测、真实供应商音频及任务插件执行/结算、最低数据库版本、ClickHouse、Windows/正式多架构镜像构建与签名/Artifactory 导入。UI 已覆盖渠道创建/修改/删除、并发显示与采样故障恢复、插件列表和详情读取；未覆盖插件安装、配置写入与完整任务生命周期。正式生产迁移仍需完成适用的生产副本演练和镜像门禁。

## 2026-09-30 发布前差异复核

审阅基线为上游 `0aec08fee811ec6136828fda790551b49e410301`，升级分支受审 HEAD 为 `888bac74b`，共 78 个文件。重点复核 Sentinel 配置传递、标准 Relay 租约获取/释放、响应体取消后的重试与退款边界、HMAC 覆盖顺序、音频 SSE usage、前端采样错误恢复，以及 Dockerfile/发布工作流。

本轮未发现新的确定性发布阻断代码缺陷；修正了总说明落后于测试环境验收记录的问题。此结论不替代尚未执行的发布门禁，也不是对上游全部新增代码的完整审计。

- 通配透传保留客户端值属于已确认的预期行为；鉴权身份存在时，显式 HMAC 覆盖仍优先于透传值，不将缺失身份时的透传行为列为缺陷。
- 响应体取消在渠道故障处理和重试决策前终止，失败退款保留 rc.40 的统一入口；既有回归验证单次尝试、渠道不自动禁用和租约归零。
- 数据库迁移和任务插件核心账务实现沿用 rc.40；下游没有重放旧计费实现覆盖这些路径。
- 发布工作流包含独立 RelayKit 测试和镜像架构/OCI/签名步骤，但本轮没有运行远端 Actions、推送镜像或创建标签。前端全量验证沿用先前记录，不把工作流中的前端构建等同于测试。
- 下一发布步骤为确定候选版本，按正式 Dockerfile 构建并验证平台镜像，再在 `new-api-2` 验证正式构建产物；当前验收不能直接放行其他生产环境。
