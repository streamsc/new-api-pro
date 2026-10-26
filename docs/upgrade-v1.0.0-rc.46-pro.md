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

尚未验证：真实供应商音频 SSE/任务创建轮询结算/Resend 与全部新增渠道、生产规模/长时间负载、ClickHouse 独立日志库、最低支持数据库版本、原始下载源环境的 Dockerfile 构建、双架构 OCI 索引及 registry/Artifactory 导入、签名/Release 跨平台流程、未覆盖路径的浏览器端到端验收。后续已获授权并完成的隔离真实文本调用及浏览器验收见下节；其余不能报告通过。多架构流水线保留原门禁，正式发布前必须执行。


## 后续本地容器验收（2026-10-23）

对代码提交 `79d5b54e0b84e33101d8c1025e83cbc01c303f12` 完成本地验收：

- Prometheus 3.2.1 镜像经指定代理下载成功；`promtool test rules concurrency.test.yml` 通过，`promtool check config prometheus.example.yml` 通过，识别 8 条规则。
- 用 `git archive` 建立隔离构建副本，按发布流程写入 `VERSION=v1.0.0-rc.46-pro.local`。本机 Docker exporter 不支持直接加载多架构 manifest list，因此分别构建、加载两个平台的 `release` target。
- 原始网络路径下 Go 模块代理超时、Debian 下载约 32 KB/s；临时副本仅将 Go 下载命令设置为 `GOPROXY=https://goproxy.cn,direct`，并把 Debian apt 源切换到华为云镜像。依赖版本、基础镜像摘要、PGO 编译和 Debian 签名校验保持原样。仓库 Dockerfile 未改动，临时 Dockerfile 已随日志保存。最终本地镜像包含镜像源配置，因此是验收产物，不是正式发布镜像；不宣称原始网络路径构建通过。
- Bun 1.4.0 前端构建和 Go 1.26.10 PGO 编译成功；amd64、arm64 完整运行层及最终 scratch 层构建成功。
- 两个平台均实测 `-version` 输出正确、入口点为 `/new-api`、工作目录 `/data`、暴露端口 3000。启动后 `/api/status` 返回成功且版本一致，首页和引用的本地 JS/CSS 均返回成功；检查数据目录和 CA 证书非空。使用临时 SQLite，服务端口仅绑定 loopback，检查后删除验收容器。
- arm64 镜像：`new-api-pro:rc46-local-arm64`，image ID `sha256:e4f4d37856b0084ad99cecf130f37c9c6581d683e4d64612ddc50d312ce2e122`。
- amd64 镜像：`new-api-pro:rc46-local-amd64`，image ID `sha256:a52e70311d08197d5e947bb526fcd76cf43b44f1dcd04c9d9e60ab6bcadd8c32`。

完整构建日志、Prometheus 日志、镜像 inspect、状态响应和可复跑脚本在 `output/upgrade-rc46-20261023/container-validation/`。本次未改业务代码、未推送镜像或分支、未签名、未创建 Release、未部署生产。后续真实文本调用和部分浏览器验收见下节；音频/任务/图片真实调用、未覆盖 UI 路径、OCI 索引/registry/签名等门禁仍待执行。


## 后续浏览器与真实供应商隔离验收（2026-10-23）

用户授权使用现有远端渠道做隔离验收并允许少量付费调用。本轮没有修改业务代码，没有替换或重启现有远端服务。

### 本地浏览器

使用 Playwright CLI 独立浏览器连接 `127.0.0.1:23146`，运行已构建的 arm64 rc.46 镜像，使用一次性 SQLite 和管理员：

- 初始化向导、登录、渠道卡片及表格正常。UI 创建禁用渠道，使用 `https://example.invalid` 和无效测试凭据，没有调用真实上游。
- 并发上限 2 保存后显示 `0 / 2`，编辑回显正确；输入 -1 阻止提交且保留表单，改为 3 保存后显示 `0 / 3`。
- 复制渠道、优先级增减正常。将优先级 0 的渠道拖入优先级 1 的渠道组，显示保存成功；完整刷新页面后两渠道优先级仍为 1。
- 页面可见时两轮自动列表刷新相隔 60.024 秒；观察到列表 URL 尾斜杠重定向请求对，不把两条 HTTP 记录计为两轮刷新。
- 浏览器拦截列表 GET 返回 503 后保留渠道行，显示“在途请求数获取失败”及 `- / 3`；撤销拦截后刷新恢复 `0 / 3`。没有向服务或共享 Redis 注入故障。
- 390×844 移动端表格可显示名称和操作，桌面卡片及表格正常；本轮不是全部尺寸和控件的无障碍审计。
- UI 创建仅“读取渠道”的作用域令牌并完成密码二次验证。不携带浏览器 Cookie 的请求：读取渠道 200，写入 403 `ACCESS_TOKEN_SCOPE_DENIED`，旧格式令牌 401 `ACCESS_TOKEN_LEGACY_RETIRED`。撤销也要求二次验证，完成后令牌返回 401 `AUTH_UNAUTHORIZED`。
- 浏览器错误日志来自未登录刷新 401、主动注入 503 和权限拒绝测试；另有密码表单 autocomplete/username 的浏览器建议，本轮未修改表单。

### 远端真实调用

在 `ubuntu@129.146.74.208` 使用独立 rc.46 容器、loopback 端口、SQLite、测试用户和 API key。仅读取 `new-api-mysql-2` 的渠道 1/2 与定价配置，复制到隔离数据库；不复制生产用户/Token，不连接生产钱包、日志库或 Redis。测试用户和 API key 分别限制为 125000 quota，禁用重试，仅允许 `gpt-6-sol`，每次最多 32 输出 token。

| 渠道 | 请求 | HTTP / 终态 | 耗时 | 五项 quota 变化 |
|---|---|---|---|---|
| 1 | Responses 非流式 | 200 / completed | 1.659 秒 | 均 614 |
| 1 | Responses SSE | 200 / response.completed | 1.579 秒 | 均 614 |
| 2 | Responses 非流式 | 200 / completed | 3.619 秒 | 均 614 |
| 2 | Responses SSE | 200 / response.completed | 3.931 秒 | 均 614 |

五项分别为用户余额减少、用户已用增加、Token 余额减少、Token 已用增加和消费日志累计增加。四笔各产生一条消费日志，累计 2456 quota；这是隔离网关账务口径，不是供应商账单核销。每笔上游 usage 为输入 4391、缓存输入 4224、输出 5；测试文本只要求回复 OK，上游注入的输入按实际回包记录。本轮验证终态、usage 和账务一致性，不验证首 token 延迟或长流取消。

现有渠道 1/2 配置前后 SHA-256 一致；现有 `new-api` / `new-api-2` 保持 healthy，未对其执行重启或更新。临时容器、复制凭据/SQLite、上传镜像归档及远端验收镜像均已清理，远端仅保留脱敏结果和无凭据的验收脚本。最初导入 MySQL JSON 字段时遇到 SQLite 参数类型错误，修正脚本序列化后重跑；失败尝试尚未发起真实请求。

仍未覆盖：真实音频 SSE、图片生成、异步任务轮询结算、长时间负载、生产数据库上的 rc.46 切换、其余 UI 路径。现有渠道没有音频/任务模型，未擅自增加供应商配置或额外发起图片付费请求。

截图、脱敏浏览器快照、权限响应、轮询时间戳、供应商结果和可复跑脚本位于 `output/playwright/rc46-acceptance/`。本地浏览器及临时容器均已关闭；原有文件与上游工作树保持不变。

## 本地发布候选与重点复核（2026-10-23）

候选版本为 `v1.0.0-rc.46-pro.1`。只读执行 `git ls-remote origin refs/tags/v1.0.0-rc.46-pro.1` 成功且无匹配；这是检查时的状态，不保留标签名。本轮不创建标签、不推送、不发布、不部署，源文件 `VERSION` 保持原状。发布说明草稿位于 `output/upgrade-rc46-20261023/release-notes.md`；正式 Release 模板已突出旧面板令牌立即失效、使用最小权限 `nap_` 令牌替换管理脚本凭据的要求。

重点复核覆盖认证分类/旧令牌拒绝/scope 与敏感操作证明绑定、标准 Relay 取消与租约释放、结算后退款保护、Redis/Sentinel 并发失败处理、HMAC 身份来源及发布工作流。此次重点范围内未发现新的阻断性业务缺陷，不等同全代码或 OWASP 合规审计。已结算请求即使之后因租约丢失进入失败退款入口，BillingSession 的 settled/fundingSettled 状态也阻止退回已提交资金；取消不保证免计费。

发现并修正发布门禁缺口：Docker 双架构启动原先仅检查 `/api/status.success`，现同时要求 `.data.version` 等于发布标签；更新手动发布示例至 rc.46。二进制与 Docker 工作流独立，Docker 在运行检查前已推送镜像，不能将镜像标签存在或二进制 Release 成功视为整个发布成功。采用前须确认两条流程成功、OCI/provenance 校验及签名验证完成。

本轮验证：

- `go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12 -shellcheck= -pyflakes= .github/workflows/release.yml .github/workflows/docker-build.yml` 通过；未运行 ShellCheck/Pyflakes，也未触发 GitHub 工作流。
- 将新 jq 断言用于此前保存的 amd64/arm64 实际状态响应：正确版本通过；错误版本及 success=false 均拒绝。未重建镜像，业务代码未改动。
- `go test ./middleware ./service ./controller ./relay -run 'Test(UserAuthRejectsStoredLegacyToken|DashboardAccessTokenClassification|.*ChannelConcurrency.*|.*BillingSession.*|.*Cancellation.*)' -count=1` 成功；middleware/service/controller 有匹配测试，relay 无匹配测试。真实 Redis/Sentinel 专用集成测试本轮未配置环境，依据前述已完成的真实实例验证，不重复声称重跑。

正式 registry/OCI 索引/签名及跨平台 Release 等未完成门禁仍按前述记录保留，不能据本地候选宣称生产发布完成。

## 正式发布与 new-api-2 同机 OCI 隔离验收（2026-10-26）

用户授权发布后，已为提交 `55e29590a82a6004c8d3973cf18c5eafa8996dfe` 创建并推送 annotated tag `v1.0.0-rc.46-pro.1`，未移动既有标签。

- [Docker 工作流 40257961459](https://github.com/streamsc/new-api-pro/actions/runs/40257961459) 全部通过：Go 测试、原始 Dockerfile 多架构构建、OCI index/provenance 关联、运行配置、amd64/arm64 启动版本断言、两个平台及 index 签名。
- [跨平台 Release 工作流 40257961710](https://github.com/streamsc/new-api-pro/actions/runs/40257961710) 全部通过；Linux amd64/arm64、macOS、Windows 二进制和校验和已发布到 [预发布版本](https://github.com/streamsc/new-api-pro/releases/tag/v1.0.0-rc.46-pro.1)。
- 正式镜像 `ghcr.io/streamsc/new-api-pro:v1.0.0-rc.46-pro.1`，OCI index 摘要 `sha256:c936878d0489f1b5e85e2d2afd803f77e838c2e6c2020d730c2f1832fbf0aa01`；amd64 为 `sha256:a271712be2a272a162f233c3f7df12572403de0f613db717bcf4e21c00eb53d5`，arm64 为 `sha256:d33f2ed133c96020a2d98013b609f3c497c0e59f77532c2e415e4f06285394e3`。
- 本地重新读取 registry 原始 index：OCI media type、两个平台镜像、两个 unknown/unknown attestation manifest 及 subject digest 对应关系均通过。
- 在 `ubuntu@129.146.74.208`（new-api-2 所在 ARM64 主机）直接从 GHCR 按 index 摘要拉取成功；Docker 正确选择 arm64，镜像 revision/version 标签匹配正式提交和版本。
- 远端 Cosign v3.1.4 下载后核对官方 SHA-256。分别验证 index、amd64、arm64 三个摘要的签名，限定证书身份为本仓库 `docker-build.yml@refs/tags/v1.0.0-rc.46-pro.1`、OIDC issuer 为 `https://token.actions.githubusercontent.com`，证书、claims 和透明日志 inclusion 均通过。
- 同机一次性容器只绑定 loopback 随机端口，独立 SQLite，限制 512 MiB/1 CPU；`-version`、`/api/status` 版本、首页与 6 个 JS/CSS 资源通过。未连接生产数据库/Redis，未发起供应商调用。验收容器已删除，正式镜像缓存保留。
- `new-api-2` 验收前后容器 ID、启动时间 `2026-09-30T08:59:03.869227895Z`、原 rc.40 镜像及 healthy 状态完全一致，未重启或替换。此次是同机隔离验收，不是生产切换或生产库升级。

证据及脚本：`output/upgrade-rc46-20261026/oci-acceptance/`，包含工作流日志/状态、原始 index、三份签名验证结果和远端 smoke 结果；远端 `/home/ubuntu/rc46-oci-acceptance/` 保留无凭据脚本及验证结果。上述正式 OCI/签名/跨平台门禁已完成，前述真实音频/图片/异步任务、长负载、ClickHouse、最低数据库版本和未覆盖 UI 等边界仍然成立；未验证 Artifactory 导入。
