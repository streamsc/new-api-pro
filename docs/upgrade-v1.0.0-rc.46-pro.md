# rc.46 Pro 本地升级候选（2026-10-23）

本次将上游基线从 `v1.0.0-rc.40`（`0aec08fee811ec6136828fda790551b49e410301`）升级为
`v1.0.0-rc.46`（`7d8b91268dab69d7150f345c448b5e250163146a`）。目标标签通过远端 ls-remote 核对，增量为 150 个提交、805 个文件。
集成起点为本地 Pro `ec1ee134e`，包含 `v1.0.0-rc.40-pro.1` 之后的验收文档。

分支 `upgrade/v1.0.0-rc.46` 是本地可审查候选，不是已发布版本。采用共同祖先三方集成，逐处处理 17 个冲突路径；最终以 rc.46 为父提交保存线性下游补丁，不改写 rc.40 分支/标签。没有推送、Release、远端合并或生产部署。
原有 `docs/diff-trace/`、`output/` 共 23 个文件逐一 SHA-256 核验未变；差异报告未修改。上游业务工作树未修改。

## 实际集成

- Go 最低版本和 Docker builder 更新为 1.26.10，保留原多架构 BUILDPLATFORM/TARGETARCH 构建方式、Pro 版本注入、scratch 最终层与发布门禁。接入 moejs v0.1.0-alpha.7 PGO、tokenkit 本地模块和独立 CI 检查；发布文案不再沿用 rc.40 的部署结论。
- 纳入任务轮询的信任域/端口重定向控制、HTTPS 降级和不可信 body 重放拒绝；不可信跳转清除任意命名凭据且不恢复。回归额外覆盖 HMAC 命名头；普通 Relay 保留上游 3xx、不跟随跳转。
- 纳入任务每次重试重新定价、清理旧表达式快照、提交前补预扣和失败全额退款，以及协议转换、显式零用量、缓存/搜索/图片/视频结算修复。
- 纳入作用域访问令牌、摘要存储、权限/失效控制及管理员敏感操作一次性验证。**Pro 不采用上游旧面板令牌 30 天兼容期**：删除旧凭据查询认证、无 scope 放行、退役计时配置、旧令牌撤销路由/UI；非 `nap_` 旧凭据立即拒绝。历史数据库列和历史审计内容保留，不进行破坏性清库。
- 保留 Redis Sentinel、音频转录/翻译 SSE、渠道并发上限/分钟采样、Prometheus、认证上下文 HMAC、客户端取消传播及 lease 释放。渠道优先级拖拽与原有采样/错误展示、展开状态共同保留；未整文件覆盖定制。
- 修正合并时翻译命名空间冲突、认证测试夹具和拖拽状态断言；受影响文件的 lint error 已修复。复用既有 DataTablePage、渠道 query hook、优先级 hooks、ConfirmDialog 和令牌组件，没有新增 UI 基础抽象。

## 本地验证

工具：Go 1.26.10（darwin/arm64）、Bun 1.3.14、Node 26.5.0。CI 固定 Bun 1.4.0；后续容器验收已用该版本完成前端生产构建，前端测试仍为本机 Bun 1.3.14。
日志和启动脚本在 `output/upgrade-rc46-20261023/`，仅含隔离测试凭据，不含生产配置。

| 验证 | 命令/范围 | 结果 |
|---|---|---|
| 根模块 | `GOWORK=off go test ./...`、`go build ./...`、`go vet ./...` | 通过 |
| 独立模块 | `cd relaykit` / `cd tokenkit` 后分别 `GOWORK=off go build ./...`、`go test ./...`、`go vet ./...` | 通过，不依赖 workspace |
| 计费、任务、安全 | 根模块测试包含 retry reserve/refund、插件加载/运行时/fetch、敏感操作 proof 的失败/过期/重放/越权、scoped token、scope 路由完整性 | 通过；不是第三方审计认证 |
| 取消、lease、HMAC | `go test -race ./controller ./service ./relay/channel -run 'ChannelConcurrency\|RelayCancellation\|ContextHMAC' -count=1` | 通过 |
| 重定向/HMAC | `go test ./relay/channel ./relay/channel/task/jsplugin -count=1` | 通过，包括真实 net/http 重定向链的可控 transport 和本地 HTTP server |
| Redis | `CONCURRENCY_TEST_REDIS_ADDR=127.0.0.1:26379 go test ./service -run TestChannelConcurrency -count=1 -v` | Redis 7.4.11 真实实例通过 |
| Sentinel | `CONCURRENCY_TEST_SENTINEL_ADDR=127.0.0.1:26383 CONCURRENCY_TEST_SENTINEL_REPLICA_ADDR=127.0.0.1:26382 go test ./service -run TestChannelConcurrencySentinelFailover -count=1 -v` | 主节点停机、故障指标、切换后保留租约、释放归零通过 |
| 数据库模型矩阵 | 设置 `TEST_MYSQL_DSN`、`TEST_POSTGRES_DSN`、`CONCURRENCY_TEST_MYSQL_DSN`、`CONCURRENCY_TEST_POSTGRES_DSN` 后 `go test ./model -count=1 -v` | SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 16.15 通过 |
| 主库迁移矩阵 | 设置 `AUDIT_MYSQL_DSN`、`AUDIT_POSTGRES_DSN` 后 `go test ./controller -run TestAuditDatabaseMatrix -count=1 -v` | 三引擎空库/代表性旧表，两次 InitDB/InitLogDB，数据、唯一性、scope token/审计读写通过 |
| 独立日志库 | 同上 DSN，`go test ./controller -run TestIndependentAuditLogStores -count=1 -v` | MySQL/PostgreSQL 空库及旧表升级通过；ClickHouse 跳过 |
| 真实 Release 升级 | `python3 /tmp/rc46-startup-matrix.py`，脚本副本随日志保存 | 从真正 rc.40-pro.1 tag 编译二进制建库、setup、写入钱包/旧凭据，再运行 rc.46 两次；SQLite/MySQL/PostgreSQL 均通过，MySQL/PostgreSQL 使用独立日志库；余额及旧列原值保留，旧令牌 HTTP 401 |
| 前端 | `bun run typecheck`、`bun run build`、受影响 254 个源码文件 oxlint | 通过；lint 仅 warning，仓库未修改文件仍有既有 lint error，未宣称全仓 lint 通过 |
| 前端测试 | `NODE_OPTIONS=--no-experimental-webstorage bun run test --maxWorkers=4` | 203 个文件、2402 项全部通过；默认并行度运行有 1 项 20 秒超时，降低并行度完整重跑通过，未修改超时阈值 |
| PGO | `go build -pgo="$(go list -m -f '{{.Dir}}' github.com/Calcium-Ion/moejs)/default.pgo" -ldflags '-X github.com/QuantumNous/new-api/common.Version=v1.0.0-rc.46-pro.local' -o /tmp/new-api-pro-rc46-pgo .` | 通过 |
| 多架构二进制 | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64/arm64 go build`；各架构 Alpine 容器运行 `-version` | 通过；此 smoke 未注入版本，输出 v0.0.0；不等同完整 Dockerfile/签名/registry 门禁 |
| 完整容器构建与启动 | `docker buildx build --platform linux/amd64` / `linux/arm64 --target release --load`，各架构执行容器验收脚本 | 通过；临时副本调整 Go/Debian 下载源，详细边界见下节 |
| Prometheus 规则 | Prometheus 3.2.1 `promtool test rules concurrency.test.yml` | 通过（后续本地验收）：指定镜像代理下载成功，规则测试及示例配置检查通过，识别 8 条规则 |

数据库 DSN 仅指向本次创建的 loopback 容器 `rc46-mysql` / `rc46-postgres`，测试创建隔离数据库；未连接线上数据库。真实 Release 启动脚本记录在上述 output 目录，保留建库、端口、命令和断言便于复跑。GORM core/mysql/postgres 驱动版本相对 rc.46 未单独调整。最低支持版本 MySQL 5.7.8/PostgreSQL 9.6 未另跑，不能据此声明所有旧版本验证通过。

安全参考：已读取 [OWASP Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)、[Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)，官网确认稳定 ASVS 为 5.0.0。适用控制为服务端 scope/权限校验、敏感操作重新验证、证明绑定操作及身份且一次性消费、有效期/撤销、审计不记录可用凭据。ASVS 章节原始文件网络读取超时，未逐条声称满足 ASVS requirement ID；本地回归不构成完整 OWASP 合规认证。

插件元数据人工核对了新增/改动的图像、视频、输入媒体及积分数量字段：数字描述为“计费主体 + 单价”，单位在 unit，枚举/布尔字段区分输入输出含义。API 文档、schema、TypeScript 类型的 fetch/allowedHosts/json-order 契约随上游同步。

## 迁移和发布前事项

1. 停止写入，备份主库和独立日志库。先在测试实例升级并通过浏览器登录创建 `nap_` 作用域令牌，按所需最小权限替换管理脚本；生产切换时安排浏览器管理员会话用于创建生产令牌。旧令牌升级即失效，无宽限窗口，也不把旧 token 自动转换为新 token。模型调用 API key（sk-）是另一种凭据，不能与面板令牌混淆。
2. 新建 `user_access_tokens` 表、审计 `(created_at,event_id)` 索引以及 API key 额度查询字段。新建 key 默认允许查询所属用户额度，既有 key 默认关闭。上线前评估大审计表索引耗时；已有 rc.40 BIGINT 钱包列和任务插件 LONGTEXT 要求继续成立。
3. 检查 DEFAULT_LANGUAGE（未设置为英文）、普通用户历史查询默认 365 天、敏感词过滤默认关闭、通知分类开关和低余额通知行为。Playground 按用户隔离会清理旧聊天缓存。
4. 插件动态 eval/Function 不再可用；核对自定义插件和允许的凭据接收 host/port。不要用任意 allowedHosts 或全局关闭 TLS 校验绕过失败；TLS 开关是逐渠道配置。
5. 不将取消等同免费：已完成流/图片按上游实际完成和用量结算；失败任务退还累计预扣。以实际供应商回包验证搜索、视频/图片实际数量和成本变化。
6. Realtime、Responses WebSocket 和异步任务仍不进入标准 Relay 并发计数。分钟采样为观察值，零值不保证排空；停流/排空应结合实际连接和任务状态判断。

尚未验证：真实供应商音频 SSE/任务创建轮询结算/Resend 与全部新增渠道、生产规模/长时间负载、ClickHouse 独立日志库、最低支持数据库版本、原始下载源环境的 Dockerfile 构建、双架构 OCI 索引及 registry/Artifactory 导入、签名/Release 跨平台流程、浏览器人工端到端验收。没有生产凭据或部署授权，因此这些不能报告通过。多架构流水线保留原门禁，正式发布前必须执行。


## 后续本地容器验收（2026-10-23）

对代码提交 `79d5b54e0b84e33101d8c1025e83cbc01c303f12` 完成本地验收：

- Prometheus 3.2.1 镜像经指定代理下载成功；`promtool test rules concurrency.test.yml` 通过，`promtool check config prometheus.example.yml` 通过，识别 8 条规则。
- 用 `git archive` 建立隔离构建副本，按发布流程写入 `VERSION=v1.0.0-rc.46-pro.local`。本机 Docker exporter 不支持直接加载多架构 manifest list，因此分别构建、加载两个平台的 `release` target。
- 原始网络路径下 Go 模块代理超时、Debian 下载约 32 KB/s；临时副本仅将 Go 下载命令设置为 `GOPROXY=https://goproxy.cn,direct`，并把 Debian apt 源切换到华为云镜像。依赖版本、基础镜像摘要、PGO 编译和 Debian 签名校验保持原样。仓库 Dockerfile 未改动，临时 Dockerfile 已随日志保存。最终本地镜像包含镜像源配置，因此是验收产物，不是正式发布镜像；不宣称原始网络路径构建通过。
- Bun 1.4.0 前端构建和 Go 1.26.10 PGO 编译成功；amd64、arm64 完整运行层及最终 scratch 层构建成功。
- 两个平台均实测 `-version` 输出正确、入口点为 `/new-api`、工作目录 `/data`、暴露端口 3000。启动后 `/api/status` 返回成功且版本一致，首页和引用的本地 JS/CSS 均返回成功；检查数据目录和 CA 证书非空。使用临时 SQLite，服务端口仅绑定 loopback，检查后删除验收容器。
- arm64 镜像：`new-api-pro:rc46-local-arm64`，image ID `sha256:e4f4d37856b0084ad99cecf130f37c9c6581d683e4d64612ddc50d312ce2e122`。
- amd64 镜像：`new-api-pro:rc46-local-amd64`，image ID `sha256:a52e70311d08197d5e947bb526fcd76cf43b44f1dcd04c9d9e60ab6bcadd8c32`。

完整构建日志、Prometheus 日志、镜像 inspect、状态响应和可复跑脚本在 `output/upgrade-rc46-20261023/container-validation/`。本次未改业务代码、未推送镜像或分支、未签名、未创建 Release、未部署生产。真实供应商、浏览器人工验收、OCI 索引/registry/签名等此前列出的门禁仍待执行。
