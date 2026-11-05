# Issue #5 validation record

Date: 2026-09-07. Source base: `0f11c372a` plus the local Issue #5 changes.
Sentinel follow-up: 2026-09-08, same application build, with an added integration test.
Runtime: Go 1.26.1, macOS arm64; Linux arm64 cross-build also succeeded.
No production deployment or release was performed. Acceptance delivery was prepared
on 2026-09-08; the linked pull request and Issue #5 track merge and closure status.

## Automated verification

- Root `go vet ./...`, `go build` and `make test`: passed. The latter includes the existing Relay, routing, billing and concurrency suites and independent relaykit tests.
- Independent relaykit `GOWORK=off go vet ./...` and `go build ./...`: passed.
- Targeted `go test -race` for metrics collector, service, controller and model behavior: passed.
- Configuration projection: SQLite, MySQL 8.4 and PostgreSQL 16 passed. MySQL/PostgreSQL tests held a real table lock to verify driver deadline cancellation, followed by successful reads/updates/deletes. Tests require empty disposable databases.
- Dedicated Redis 7 integration: shared observations from separate clients, CLIENT PAUSE read timeout, recovery, active lease preservation and release: passed. No existing Redis data was used.
- Real TCP HTTP tests: successful exposition, keep-alive reuse, two-request concurrency cap, cancellation propagation, unsupported write deadlines, dependency-failure HTTP 200, and a slow reader triggering a write deadline and releasing its slot: passed.
- Prometheus 3.2.1 `promtool check config` and `test rules`: passed. Fixed inputs cover shared deduplication, local aggregation, unlimited channels, damaged peer configuration, differing limits, independent domains and partial/all target failures.
- A live Redis-backed scrape of 1000 configured channels emitted 3010 exposition lines, returned HTTP 200 in 23.495 ms, had both component success values equal to 1, and passed `promtool check metrics`.

## Routing and isolation

- Actual application instances: both embedded frontend and slave-node `FRONTEND_BASE_URL` mode returned HTTP 200 exposition at /metrics. The latter still redirected its root URL to the external frontend.
- A Linux application container on a dedicated internal Docker network was reachable by a scraper on that network and had no published backend port.
- A separate Nginx business proxy returned 404 for /metrics, /metrics?x=1 and /metrics/, while /api/status returned 200.
- This validates the sample local topology. No production/public deployment was inspected or changed.

## Relay load comparison

The same instrumented application build was compared with no scrapes versus a scrape
at start and every 30 seconds, for 65 seconds per run. Each instance saw the same
1000-channel directory in a shared temporary SQLite database. These load runs used
process-local concurrency; Redis correctness and one 1000-channel scrape were tested
separately. The workload was fixed at 40 requests/second total, round-robin across
instances: 80% synchronous responses delayed 100 ms and 20% SSE responses containing
five output events 100 ms apart, with actual usage and a final DONE event. Every
request traversed the actual authenticated Relay, channel selection, reservation and
billing path to the deterministic local upstream. Startup warmup was excluded from
the measured windows. In the five-instance sequence the every-fifth streaming
request lands on the same instance; both comparison runs use that same distribution.

CPU is the sum of application-process CPU time deltas. RSS is the peak sampled
aggregate RSS (every five seconds), not total system memory or isolated allocations.

| Instances | Scrapes | Successful requests | Failed requests | RPS | P95 ms | CPU seconds | Peak RSS KiB | Successful scrapes | Max scrape ms |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | none | 2600 | 0 | 40 | 514.670 | 28.04 | 90336 | 0 | 0.000 |
| 1 | 30s | 2600 | 0 | 40 | 514.874 | 28.48 | 87952 | 3 | 5.610 |
| 5 | none | 2600 | 0 | 40 | 515.316 | 28.97 | 341664 | 0 | 0.000 |
| 5 | 30s | 2600 | 0 | 40 | 515.287 | 29.69 | 354720 | 15 | 13.457 |

One earlier five-instance baseline was discarded: the host reached 96% disk usage
and the existing 95% disk guard rejected 1079 requests. Only the disposable test
database's `performance_setting.monitor_disk_threshold` was then set to 0; both
five-instance comparison runs above used that setting. Production configuration and
the guard implementation were not changed.

These are short development-machine observations, not a production capacity bound
or a statistically isolated estimate of overhead. Other validation/container work
ran on the same host. There were no request or scrape failures in the retained runs.
The workload generator and raw experiment inputs remain in
`/tmp/issue5-validation.DJo6Ct/` for this workspace session.

## Re-running verification

```sh
go test ./pkg/concurrencymetrics ./controller ./service ./model
go test -race ./pkg/concurrencymetrics ./controller ./service ./model -run 'Test(ParseConcurrencyLimit|ConcurrencySnapshot|ChannelConcurrencyConfigProjection|CollectorExposes|MetricsHTTP|MemoryChannelConcurrency|RedisChannelConcurrency|ChannelConcurrencyRead)'
```

Opt-in integration environment variables: `CONCURRENCY_TEST_MYSQL_DSN`,
`CONCURRENCY_TEST_POSTGRES_DSN` (empty disposable databases), and
`CONCURRENCY_TEST_REDIS_ADDR` (dedicated server; the test issues CLIENT PAUSE).
Run `TestChannelConcurrencyConfigProjection` and `TestChannelConcurrencyRealRedis`.
From this directory, run `promtool check config prometheus.example.yml` and
`promtool test rules concurrency.test.yml`.

## Actual Sentinel failover

Passed on 2026-09-08 using an isolated Docker network, Redis 7.4.11 Alpine, one master,
one replica and one Sentinel (quorum 1, down-after 5000 ms, failover-timeout
15000 ms). Two actual Linux arm64 application instances used the existing Sentinel
environment configuration, a disposable SQLite database, and Redis DB 13.
The database contained the previous 1000 channels plus disabled test channel 91502
with a configured limit of 10. HTTP ports were bound only to loopback.

`TestChannelConcurrencySentinelFailover` acquired a real service lease, observed its
score on the replica, then issued `SHUTDOWN NOSAVE` to the elected master and
confirmed that it stopped responding. The production `common.InitRedisClient`
created the tested failover client; a separate Sentinel client checked shared data.

- Before failure: both clients saw count 1; the lease was confirmed replicated.
- At 699 ms after shutdown: collection reported config success 1, concurrency
  success 0, preserved the limit, and omitted the count. The complete collector
  output passed comparison with the official Prometheus test parser.
- At 6.126 s: Sentinel had changed the master from `192.168.97.2:6379` to
  `192.168.97.3:6379`, and both clients saw count 1. Scope remained `redis`.
- At 30.127 s: the lease score increased on the promoted master, confirming actual
  renewal, and the lease had not been marked lost.
- A new acquisition raised the count to 2; releasing both leases produced real 0.

Concurrently, a 45-second HTTP observation fetched each application's real
`/metrics` endpoint, waiting 250 ms between completed requests. Each response was
checked for HTTP status, component values, test-channel limit/count, and total
in-flight sample count. All 266 responses returned HTTP 200; configuration remained
successful and the test-channel limit remained 10 throughout.

| Instance | Scrapes | Concurrency-failure scrapes | Maximum response ms | First observed recovered count 1, ms from observer start |
| --- | --- | --- | --- | --- |
| 19401 | 133 | 3 | 3023 | 8939 |
| 19402 | 133 | 3 | 3025 | 8942 |

Every concurrency-failure response omitted **all** in-flight samples. Successful
responses contained all 1001 count samples. Both applications observed the surviving
lease as 1 after recovery, and both ended at 0 after release. The HTTP observation
clock started before the integration test, so its timings are not failover duration.

The new test, `go vet ./service`, the service suite, and the existing targeted
metrics/concurrency race suite passed. The real Sentinel test ran as a Linux
cross-compiled test binary without race instrumentation; the ordinary native race
run skips it unless its integration environment is supplied.

For a repeat run, provision a **disposable** master/replica/Sentinel cluster named
`issue5`, using DB 13, and configure down-after at least 5000 ms. Set
`CONCURRENCY_TEST_SENTINEL_ADDR` and `CONCURRENCY_TEST_SENTINEL_REPLICA_ADDR`, then run
`go test ./service -run '^TestChannelConcurrencySentinelFailover$' -v -count=1` from
a host/container that can reach Sentinel's advertised Redis addresses. The test
shuts down the elected master; recreate the cluster before repeating. Temporary
configuration, test binary/log, Sentinel log, HTTP observer and all 266 observations
are retained in `/tmp/issue5-sentinel-validation/`. Test containers and network were
removed after verification.

This verifies an actual single-master outage and automatic promotion, not Sentinel
quorum resilience, network partitions or preservation of unreplicated writes.
No Prometheus server was attached during this follow-up; deployment/domain labels
remain static target configuration, and their query behavior was tested separately
with promtool. Multi-host Redis-backed load testing was not performed.

## Issue status

The previously missing Sentinel functional verification is now covered within the
topology and limits described above.
The Issue #5 acceptance scope is satisfied by the evidence below. Multi-host load,
Sentinel quorum/partition testing and production deployment are not added as
acceptance requirements. Closing this phase does not close parent #4 or implement
dependent phase #6.

## Acceptance matrix

| Issue #5 requirement | Evidence | Result |
| --- | --- | --- |
| Format and fixed metric contract | `TestCollectorExposesOnlyCurrentKnownValues`, Sentinel complete output comparison, live `promtool check metrics` | Passed |
| All channels, empty directory, creation/update/deletion | `TestChannelConcurrencyConfigProjection`, `TestMetricsHTTPCurrentSnapshotAndKeepAlive`, disabled Sentinel test channel | Passed |
| Limit parsing and no repairs | `TestParseConcurrencyLimit`, persisted damaged-setting HTTP assertion, finite-to-unlimited HTTP scrape | Passed |
| Existing reservation semantics and lower limits | Memory/Redis concurrency suites, real Relay/SSE load above, HTTP limit lowered to 1 while two live reservations remain visible and uncancelled; acquisition/release policy unchanged | Passed |
| Failure and recovery | Snapshot partial-failure cases, Redis uninitialized/failure tests, real paused Redis and Sentinel outage/recovery | Passed |
| Snapshot lifecycle and cancellation | Fresh-registry comparison, deletion HTTP assertion, collector cancellation, real DB lock deadlines, Redis timeout and live lease renewal | Passed |
| Two-request entry limit and slot release | Real TCP concurrency/cancellation and slow-reader tests, successful and dependency-error response tests | Passed |
| Local sum and Redis deduplication | Fixed promtool inputs: two local counts of 2 give 4; shared observations of 2 remain 2 with limit 10 and ratio 0.2 | Passed |
| Deployment/domain isolation and Sentinel identity | Queries retain deployment/domain grouping, fixed independent-domain inputs; actual Sentinel promotion with unchanged application scope and static target-label contract | Passed |
| Partial failures and configuration disagreement | Complete snapshot output tests and promtool cases: valid peer remains usable, conflicting limits suppress ratio, all failed targets produce no count | Passed |
| Routes and deployment isolation | Actual embedded/external frontend modes and isolated Docker/Nginx checks described above | Passed |
| Data minimization and sensitive output | Projection fixture has no credential columns; only ID/setting selected; no repairing getter; fixed-label collector and fixed HTTP errors | Passed |
| Regression and performance | Root/relaykit vet/build/tests, targeted race; SQLite/MySQL/PostgreSQL and Redis integration; recorded 1000-channel, 1/5-instance Relay comparisons | Passed |

Performance evidence is for the explicitly recorded process-local mode, with Redis
correctness and live collection verified separately. The existing Relay acquisition,
deferred release, retry, billing and streaming code is outside this change.

Delivery recheck on 2026-09-08: root and independent relaykit `go vet ./...` and
`go build ./...`, `make test`, and `go test -race ./controller -run '^TestMetricsHTTP'
-count=1` passed after adding the lower-limit HTTP assertion. Remote CI results are
recorded on the delivery pull request.

The first PR CI run passed backend checks but found two pre-existing
`Promise.withResolvers` calls in channel-list tests that are unavailable under the
project's ES2022 TypeScript library. Delivery includes a test-only correction using
the repository's existing deferred-Promise pattern; no frontend application logic,
compiler target or dependency was changed. Forced typecheck and affected-file lint
passed. All 38 frontend test files (196 tests) passed locally with
`NODE_OPTIONS=--no-experimental-webstorage bun run test`: the unmodified Node 26
experimental storage globals had caused eight jsdom storage failures. This local
environment override is not a production or CI configuration change.

## Issue #8 implementation validation — 2026-11-04

Workspace: `release/v1.0.0-rc.46-pro`. Existing `docs/diff-trace/` and prior
`output/` artifacts were retained. This patch was not deployed or published.

Observed passing checks:

- `go test ./controller ./service ./middleware ./model ./relay ./pkg/concurrencymetrics -count=1`.
  Covers ordinary relay cancellation, retries/final accounting, Responses WS,
  request-policy/billing regressions and existing legacy limiter behavior.
- `go test -race ./service -run 'UserResource|ResourceStore|ResourceRedisDatabases|MemoryChannelConcurrency' -count=1`.
- `CONCURRENCY_TEST_REDIS_ADDR=127.0.0.1:16389 go test ./service -run 'TestUserResourcesRealRedisProcesses|TestChannelConcurrencyRealRedis|TestUserResource|TestResource' -count=1 -v`.
  Dedicated Redis 7 Alpine container: independent OS processes share one user cap;
  exit leaves a slot, a new process cannot acquire it, accelerated server-side
  expiry frees concurrency without refunding window usage. Real CLIENT PAUSE
  causes failed reads, not cancellation. Unit cases additionally exercise all
  three-resource atomicity, rolling boundaries, zero allowance, unlimited-to-limit
  transition, idempotent release, domains, unavailable statistics and data loss.
- `CONCURRENCY_TEST_SENTINEL_ADDR=127.0.0.1:26390 CONCURRENCY_TEST_SENTINEL_REPLICA_ADDR=127.0.0.1:16391 go test ./service -run '^TestChannelConcurrencySentinelFailover$' -count=1 -v`.
  Dedicated master/replica/Sentinel, actual `SHUTDOWN NOSAVE`, promotion after about
  6.8 seconds. User window and slot were replicated before failure; new master
  retained them, channel expiry timestamp did not renew, and releases succeeded.
  This does **not** prove unreplicated writes survive failover.
- `CONCURRENCY_TEST_MYSQL_DSN=... CONCURRENCY_TEST_POSTGRES_DSN=... go test ./model -run TestUserResourceOptionsDatabaseMatrix -count=1 -v`.
  SQLite **3.50.4**, MySQL **8.0.46**, PostgreSQL **16.15**. Existing options table,
  repeated migration, prior unrelated row preservation, defaults/override update,
  stale-cache audit preimage and invalid limit rejection passed. No schema or
  driver change is introduced; this is not a whole released-database upgrade test.
- Additional focused controller checks cover RootAuth rejection, authenticated self
  identity despite another ID in query parameters, anonymous rejection, audit
  before/after/reason, current username mapping, absent unavailable metric samples,
  free/billing-disabled estimates, cross-key user RPM, and retry usage unchanged by
  upstream actual input counts. Legacy disabled limiter is tested with nil Redis.
- Frontend `bun run test src/features/dashboard/components/overview/__tests__ src/features/system-settings/request-limits/__tests__`: **17 tests passed**.
  Includes 39/40/79/80%, unlimited, failure/stale data suppression, hidden/unmounted
  polling, required reason, zero/unlimited overrides, invalid limits and failed
  save retention. `bun run typecheck`, changed-file `oxlint`, and `bun run build`
  passed. Production build reported approximately 67.4 MB raw / 20.8 MB gzip for
  the entire existing application, not the marginal size of this feature.
- `go build ./...`, `go test ./relay/... -count=1` and `git diff --check` passed.
- Playwright visually inspected actual feature components with isolated API
  fixtures: desktop, 390px dark mobile, and settings JSON editor. Screenshots are
  under `output/playwright/issue8-resources-*.png`. Reused PanelWrapper, ErrorState,
  Progress, JsonCodeEditor, settings form/actions, RHF/Zod and TanStack Query;
  no replacement general-purpose component or production dependency was added.
- Live acceptance used two compiled gateway processes, shared SQLite/Redis 7,
  a round-robin HTTP proxy and a controllable local OpenAI-compatible upstream.
  A real browser logged in with encrypted password login, saved limits with a
  reason, and displayed live usage. The persisted operation audit contains the
  root identity, reason and correct before/after values. Two API keys belonging
  to the same user were used against different replicas.
- `output/issue8-validation/live-dual-gateway.log`: cross-key concurrent rejection
  leaves all counters unchanged; both peers expose equal usage gauges; upstream
  actual input 1234 does not replace admission estimate 9; two upstream retry
  attempts consume one RPM; completed streaming releases concurrency; RPM/TPM
  and enabled-zero rejection occur before upstream; Retry-After is present for
  the exhausted rolling window; an actual 61-second wait expires RPM/TPM.
- Redis pause/recovery verified restricted 503, omitted unavailable usage/metrics,
  no cancellation of admitted work, and successful all-unlimited requests. A
  timed-out release was observed to execute after unpause, so timeout is not
  treated as proof that the write failed. Initial default-timeout trials also
  encountered existing authentication-cache delays and a global API limiter
  failure before the resource endpoint. The isolated resource-failure run used
  Redis URL read/write/dial timeouts of 200ms, no client retries, and
  `GLOBAL_API_RATE_LIMIT_ENABLE=false`; these were local fixture settings only.
- `output/issue8-validation/live-lifecycle.log`: an actual peer restart retains
  shared concurrency/window state; a streaming client's socket disconnect
  releases concurrency without refunding RPM; an unlimited user is still rejected
  with channel-store 503 when the selected channels are constrained during Redis
  failure. Config synchronization was set to two seconds for local acceptance.
- Live evidence: `output/playwright/issue8-live-{settings,overview}.png`,
  `output/issue8-validation/live-browser-audit.json`, and peer resource/metrics
  snapshots. The overview screenshot shows 1/1 concurrent, 9/10000 estimated
  input TPM, and 1/10 RPM. Only the upstream and fixture data were synthetic;
  authentication, config persistence, relay, Redis admission and the UI were real.
- Final review removed duplicate unlimited-state text from the overview.
  All 17 frontend tests, typecheck, affected-file oxlint, production frontend build,
  final embedded gateway build and `git diff --check` passed after this change.

Verification boundaries:

- No production traffic, sustained capacity benchmark, exhaustive network-partition
  matrix or half-hour wall-clock endurance test was run. The 30-minute expiry
  was accelerated with controlled clocks/server timestamps; the 60-second window
  was additionally verified with real elapsed time. Live browser acceptance used
  a local upstream and shared SQLite; MySQL/PostgreSQL and Sentinel were exercised
  separately by the integration checks above.
- Input is an estimate of client-visible DTO input; remote opaque references and
  provider-specific fields can differ from actual full model context. Redis loss,
  asynchronous replication and exceptional >30-minute requests can undercount;
  none of these trigger recovery coordination or additional request cancellation.
- Configuration propagation uses existing polling (default 60 seconds) and can lag
  longer under database failure. Scrapes are bounded, may omit samples on timeout,
  and have user-count-dependent cardinality/cost; no production-size scrape load
  benchmark was performed.
- Existing channel WebSocket coverage is unchanged; channel slots cover ordinary
  HTTP attempts. New user slots additionally cover Responses WebSocket turns.
- New user resource UI strings have English and Simplified Chinese text; the other
  locale catalogs include English fallback entries for the newly added strings.

Upstream merge considerations: the main HTTP relay adds one admission call outside
its retry loop, Responses WS adds one per-turn call, and option/metrics/routes gain
small registration points. Legacy model limiting is not rewritten. The channel
renewal/cancellation behavior is intentionally removed; do not restore it when
merging upstream. Redis deployments use existing database/instance isolation; no
`GATEWAY_DEPLOYMENT_ID` is required. Channel keys retain their existing names. Use
a drained upgrade when changing slot lifetime/renewal semantics. Review these
integration points and rerun the focused regression tests after upstream merges.

Follow-up correction: removed the deployment-ID option, resource key namespace,
self API domain field and gateway-generated domain metric labels. Independent
installations use separate Redis databases/instances. Prometheus examples now
retain an operator-provided `deployment` target label. Earlier live artifacts
above record the pre-correction implementation and are historical evidence.
Correction checks passed: `go test ./service ./controller ./relay
./pkg/concurrencymetrics -count=1`, focused service race tests, `go build ./...`,
and real Redis tests with independent OS processes sharing database 13 and an
independent admission in database 14. Missing deployment ID is accepted in default,
single and cluster modes with Redis configured. Logs are
`output/issue8-validation/remove-deployment-id-*.log`.

PR CI follow-up: the complete suite found missing declarations for four backend
translation keys and three personal-access-token routes. Added the key constants
and existing `profile:read` / `option:read` / `option:write` route grants without
changing role checks or the deny-by-default policy. Regression tests first
reproduced valid scoped tokens receiving `ACCESS_TOKEN_ROUTE_UNDECLARED`, then
verified allowed reads/writes, missing scopes, read-only write denial, non-root
configuration denial, and authenticated self identity despite a supplied user ID.

Authorization review references: [OWASP ASVS 5.0.0 V8](https://github.com/OWASP/ASVS/blob/v5.0.0/5.0/en/0x17-V8-Authorization.md)
8.2.1 (function permissions), 8.2.2 (object access), 8.3.1 (server enforcement),
and the OWASP [Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html),
[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
and [Authorization](https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html)
cheat sheets. This is a focused route-authorization check, not a full ASVS certification;
credential issuance, session rotation and authentication lifecycles are unchanged.

Follow-up verification passed: focused i18n/router/controller regressions;
`make test` across the root, relaykit and tokenkit modules; `GOWORK=off go vet ./...`
and `GOWORK=off go build ./...` independently in all three modules; and
`git diff --check`. Local logs: `output/issue8-validation/ci-fix-*.log`.

## Issue #6 收敛范围验收（2026-11-05 至 2026-11-06）

### 实现基线与改动范围

基线为 `5c8c23c36901b1d2f9ec6a0171e76370242b8ecb`。实施前已完整读取
2026-11-05 更新的 #6 和总需求 #4（更新时间分别为 `09:57:05Z`、`09:57:03Z`）。
本次交付渠道名称映射、渠道并发拒绝计数、逻辑请求结果和总耗时四项指标。
原有未跟踪文件均已保留。

渠道名称复用现有采集快照；逻辑请求计数、耗时和结果分类集中在 `pkg/relaymetrics`。
主程序仅在选中渠道时识别模型、Relay 最外层结束处理、渠道槽位拒绝分支和指标注册处接入。
未改动适配器或独立模块，也未引入上游合并、实际 usage 或首内容识别工作。

### 自动化验证

以下检查已通过：

- `make test`：根模块、relaykit、tokenkit 测试套件。
- 针对逻辑结果、Relay 边界、取消及指标 HTTP 接口的
  `go test -race ./pkg/relaymetrics ./controller`；此前并发服务相关竞态回归也已通过。
- `go vet ./controller ./middleware ./model ./service ./pkg/relaymetrics ./pkg/concurrencymetrics`、
  `go build ./...`、`git diff --check`。
- 真实 SQLite **3.50.4**、MySQL **8.0.46**、PostgreSQL **16.15** 上的
  `TestChannelConcurrencyConfigProjection` 和 `TestUserResourceOptionsDatabaseMatrix`。
  覆盖同名渠道、改名、删除、无效配置、读取取消和数据库阻塞超时。
  查询仅增加读取已有名称列，没有新增表或迁移。
- 专用 Redis **7** 上的 `TestChannelConcurrencyRealRedis` 和
  `TestUserResourcesRealRedisProcesses`，覆盖共享状态、读取失败、多进程和数据库隔离。
- Prometheus **3.2.1** 的 `promtool test rules docs/prometheus/concurrency.test.yml`：
  覆盖有限/无限制上限、共享/进程作用域、同名不同 ID、跨部署隔离、改名期间冲突抑制、
  关联右侧唯一性和各进程独立重启。官方解析器及严格注册表检查验证了名称、标签、类型、桶边界和样本。

真实 Relay 集成测试覆盖同步/流式成功、同步失败、HTTP 200 后流式失败、缺少必需终止事件、
Ollama 旧流式路径的 `unknown`、同步/流式取消、重试后成功（两次上游调用只记一个逻辑请求）、
上游 429、渠道槽位拒绝、Redis 申请失败、用户准入拒绝及提前校验返回。
分类测试另覆盖 panic、内部取消、响应完成后客户端关闭、业务拒绝、安全策略拒绝、未知不完整原因、
缺少流式状态，以及所有结果的耗时样本。现有性能页面的健康采样口径保持不变。

### 本地 Prometheus + Grafana 现场验收

按用户要求，搭建独立 Docker Compose 环境：两个包含最终补丁的 **Linux ARM64 网关**、
共享测试 SQLite 和 Redis、Python 模拟上游、Prometheus **3.2.1**、Grafana **12.3.0**。
所有对外端口均绑定 `127.0.0.1`。已比对容器内与最终本地构建二进制的校验和。
现有预生产环境未被复用或修改。

本地访问地址（保留运行，供用户确认）：

- Grafana：[验收看板](http://127.0.0.1:33630/d/issue6-local)
- Prometheus：[采集目标](http://127.0.0.1:33690/targets)
- 网关指标：[副本 A](http://127.0.0.1:33681/metrics)、[副本 B](http://127.0.0.1:33682/metrics)

运行目录为 `/tmp/issue6-monitoring`。性能对比使用的基线网关已停止，临时数据库验证容器已移除。
测试流量已结束。Grafana 提供匿名只读访问；测试令牌和数据库仅存放在运行目录，不复制到验收资料中。
确认后可执行 `docker compose -f /tmp/issue6-monitoring/compose.yml down` 停止验收环境。

通过真实 HTTP、原始指标和 Prometheus API 核验的结果如下：

| 验收项 | 实测结果 |
| --- | --- |
| 采集及看板 | 两个目标均为 UP；看板查询无错误或警告。展示结果分布、包含 unknown 的比例、P50/P95/P99、拒绝率、名称、容量、采集状态及原有用户额度指标。 |
| 最终批次 | **18 个逻辑请求：成功 9、失败 5、取消 2、unknown 2；渠道拒绝恰好 1 次。** |
| 按模型核对 | gpt-4o-mini：成功 7、失败 5、取消 2；legacy-demo：unknown 2；secondary-demo：成功 2。 |
| 重试和拒绝边界 | 两个重试场景各调用上游两次，逻辑请求各记一次。槽位拒绝未调用上游。 |
| 共享并发去重 | 保持一个请求在途时，两副本均输出 1；Prometheus 去重后为 **1**。 |
| 无限上限及同名渠道 | 渠道 2 的启用标记为 0，不输出数值上限；同名渠道 1、3 通过 ID 区分。 |
| 改名 | 渠道 1 输出新名称并停止输出旧映射，拒绝 Counter 不重置；两副本及唯一名称规则收敛。改名冲突和跨部署隔离另有明确规则测试。 |
| 用户 RPM 拒绝 | 临时设为 0 后，两节点均拒绝并增加用户拒绝计数，渠道拒绝计数不变；随后恢复配置。 |
| Redis 故障 | 暂停 Redis 后，在途 Gauge 缺失，采集成功标记为 0；独立可读的名称和本地请求 Counter 仍存在。恢复 Redis 后采集正常，不以假零代替缺失。 |
| 实际进程重启 | Counter/Histogram 重置，Prometheus `resets(...)` 正确识别；所有模型/结果的 Histogram `_count` 与请求 Counter 一致。 |

现场测试发现并修正了一个生命周期边界：客户端可能已收到完整同步响应并关闭连接，而 Relay 仍在清理。
同步处理器正常返回被视为完成事实，因此稍后的 context 取消不会把成功误记为取消。
同步读取响应体时发生的真实取消会返回 Relay 错误，仍归为取消；已知业务拒绝也优先于稍后的取消。
适配器若吞掉写出错误，现有状态无法提供同等区分精度；该局限已记录，未为监控增加协议解析或写出跟踪。

### 截图与原始证据

以下截图由真实浏览器采集并完成目视检查；前五张保留原始英文界面作为测试过程证据：

- `output/playwright/issue6-monitoring/01-overview.png`：指标总览。
- `output/playwright/issue6-monitoring/02-capacity-and-users.png`：容量和用户额度。
- `output/playwright/issue6-monitoring/03-prometheus-targets.png`：两个采集目标均正常。
- `output/playwright/issue6-monitoring/04-rename-and-recovery.png`：改名及恢复过程。
- `output/playwright/issue6-monitoring/05-final-batch.png`：最终二进制的精确 18 请求批次。
- `output/playwright/issue6-monitoring/06-acceptance-zh.png`：中文验收看板，同一固定时间范围。

`output/issue6-validation/review-url.txt` 保存了固定时间范围的看板链接，便于测试结束后回看。

原始证据和可复现配置位于 `output/issue6-validation/`：
`live-acceptance.json`、`live-lifecycle.json`、在途/改名/故障/重启的 `.prom` 快照、
`dashboard-query-checks.json`、`database-matrix.log`、`redis-regressions.log`、
`promql-rules.log`、最终测试/构建日志，以及 `monitoring/` 中的配置和脚本。
这些资料中的身份均为测试数据。

### 同负载性能对比

从上述未修改的 HEAD 单独构建基线二进制。三种模式交替执行，**每种 3 轮，每轮 300 请求**，
固定 **50 请求/秒、4 个客户端工作线程**，使用同一模拟上游、共享测试 SQLite/Redis 和免费按次定价。
每轮等待逻辑请求清理完成后再开始下一轮。三种模式均保留原有用户额度指标。

- 基线：未加入本次指标记录。
- 启用记录：包含本次补丁，不执行周期抓取。
- 记录并抓取：每 **100ms** 完整 HTTP 抓取一次 `/metrics`。

对比期间暂停 Prometheus 周期抓取，结束后恢复。渠道和用户并发上限在所有模式下统一临时提高，
测量后恢复原配置。

三轮各项统计值的中位数如下，单位为毫秒；环境为 Apple M4 / Docker Desktop ARM64：

| 模式 | 平均耗时 | P50 | P95 | P99 |
| --- | ---: | ---: | ---: | ---: |
| 基线 | 9.121 | 6.575 | 14.390 | 101.396 |
| 启用记录 | 7.255 | 7.016 | 10.621 | 16.639 |
| 记录并抓取 | 8.017 | 6.124 | 14.115 | 60.794 |

**2,700 个测量请求全部成功，抓取也全部成功**，三种模式均维持约 50 请求/秒。
表中测量的是客户端观察到的 HTTP 耗时；指标 Histogram 的边界还包含后续清理，两者不能直接等同。
`BenchmarkLogicalMetrics` 另提供分类、记录和 HTTP 指标输出的独立微基准。

尾延迟波动较大，不能据此宣称性能提升或保证生产环境开销。
前期不限速探索曾使 SQLite 单写入端在响应已送达后的清理阶段积压，并触发准入拒绝；
该结果未混入受控性能对比。最终固定到达速率并逐轮等待清理，排除了这一干扰。
各轮请求、抓取和清理等待数据见 `live-load-comparison.json`、`live-load.log`。
未执行生产规模标签基数、持续容量、真实远端供应商矩阵或完整 30 分钟墙钟耐久测试。

### 支持范围与后续上游同步风险

本次同时回核了 #4 第一阶段的有限/无限制上限、零在途、读取失败和共享计数域行为。
指标名称、标签归并、桶边界、入口、分类精度和查询分母已在 README 固定。
实际 Token 用量、渠道尝试、首次上游事件和首内容耗时均不属于本期验收。
Realtime、Responses WebSocket、异步/被插件接管的请求，以及 Relay 前的拒绝不纳入逻辑请求指标。

后续同步上游时，应重点检查 Relay 最外层结束处理顺序、原始请求 context 的捕获、唯一槽位拒绝分支、
选中渠道的模型归并及 StreamStatus 语义，并重跑集成、竞态、规则和真实客户端关闭连接测试。
不要恢复租约丢失取消行为，也不要让 relaykit 依赖宿主 Prometheus 注册表。
