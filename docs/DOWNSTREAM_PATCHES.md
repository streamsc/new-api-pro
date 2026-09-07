# Downstream Patch Queue

本仓库以 `QuantumNous/new-api` 的正式 Release tag 为生产基线，并以线性补丁队列维护暂未进入上游的修复、发布基础设施和增强。

## Branch model

- `main`：仅快进跟踪 `upstream/main`，用于观察上游变化和创建上游 PR，不承载下游定制。
- `release/<upstream-version>-pro`：从官方 Release tag 创建，按本文件顺序应用 active 补丁。
- `pr/<patch-id>`：需要贡献上游时，从最新 `upstream/main` 创建并重新应用对应补丁。

发布过的 tag 不移动，旧 release 分支不改写。上游发布新版本时创建新的 release 分支，不把旧 release 分支 merge 到新分支。

## Active patches for `v1.0.0-rc.25-pro`

| Order | Patch ID | Commits | Original sources | Purpose | Validation | Status |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `redis-sentinel` | `80262dd2`, `9eb80a60`, `3fe62569` | `e5b790ee`, `d95ca74d`, `5597524e` | 通过 go-redis failover client 支持 Sentinel 主节点发现与自动故障转移 | `go test ./common ./model ./middleware ./service` | active |
| 2 | `artifactory-release-infrastructure` | `2df032b3`, `22172f15`, `760abc15`, `310329bf` | `04fcff85`, `15497889`, `c7aa0403` | 保留 RelayKit 双模块门禁、版本校验、最终 `scratch` release 层、Artifactory 7.71.21 兼容、多架构 OCI Index、minimal provenance、无 SPDX SBOM、双架构冒烟和 Cosign 签名 | Workflow YAML、`make test`、前端 build、Docker `release`、版本和 `/api/status` | permanent |
| 3 | `audio-stt-streaming` | `510d170b`, `6f84ab0c`, `6dd57eeb` | `4bc2bd80`, `eaae8337`, `841466a9` | 在 `relaykit/dto` 接口上支持 `/v1/audio/transcriptions` 和 `/v1/audio/translations` SSE 响应，并覆盖真实 multipart `stream=true` 请求 | `go test ./relay/helper ./relay/channel/openai` 和 `cd relaykit && go test ./dto` | active |
| 4 | `channel-relay-concurrency` | `653265fd`, `dde5ef0f2` | downstream | 标准 Relay 渠道并发上限和在途计数；Redis 共享预留；Issue #3 补丁完善分钟采样、未知值及作用域展示，见下节 | 根模块与 RelayKit vet/build/test、并发 race、前端测试/typecheck/build、受影响文件 lint/format、浏览器及真实 Relay 验证；既有取消边界见下节 | active |
| 5 | `context-hmac-header-override` | `0fc4e495` | downstream | 为 `header_override` 增加基于已认证用户或令牌 ID 的稳定 HMAC 占位符，不向上游暴露原始身份 | `go test -race ./relay/channel -run ContextHMAC`、根模块 vet/build/test、前端测试/typecheck/build、受影响文件 lint/format | active |

## Channel concurrency display (Issue #3)

基于 `32507fc42aede780f9c22f61fab97e4897ac45fe` 完善 [Issue #3](https://github.com/streamsc/new-api-pro/issues/3)。本节记录渠道展示增量补丁；发布过的 tag 不变，后续重放需包含本次增量修改。

- 渠道列表、搜索和详情的 `in_flight` 为可空整数；计数读取失败时保留配置并返回 `null`。列表与搜索的 `data` 增加 `in_flight_scope`（`redis` / `process`）和 `in_flight_available`。此变化不修改数据库模型或转发预留策略。
- 页面复用一个列表查询，每 60 秒刷新；隐藏、离开或查询执行期间停止周期调度，恢复可见立即读取。支持手动刷新，不重试或补发错过的周期。普通后台错误保留配置、清除有效计数，并显示一处固定提示。
- 表格和卡片共用数值渲染；标签行显示 `-`，不汇总容量。查询失败或字段缺失不伪装成零值；正常采样间隔内保留最近成功值。
- 刷新不禁用整个页面，保留选择、标签展开及未提交编辑。共享行组件和渠道卡片将展开状态纳入渲染依赖，防止稳定行对象使箭头显示旧状态。
- 数值仅代表采样时刻的标准 Relay 预留数。无 Redis 时只代表处理查询的进程；零值不保证所有入口请求已排空。Realtime、异步任务、后端 Metrics 以及无限制渠道的 Redis 故障放行不在本补丁范围。

### Validation on 2026-09-05

- 后端 SQLite + miniredis 接口测试覆盖正常/失败、空列表、作用域、有限/无限制渠道和详情响应；独立注入计数读取错误验证有效预留、请求上下文和渠道状态不受影响，正常释放仍可执行。
- 前端 38 个测试文件、196 个测试通过，包含刷新生命周期、去重、搜索翻页竞态、HTTP/业务错误恢复，以及共享行展开和选择保持。Node 26 执行测试需要 `NODE_OPTIONS=--no-experimental-webstorage`，避免 Node 原生 Web Storage 与 jsdom 冲突；不为此修改应用代码。
- Playwright + Chrome 在独立本地测试实例验证桌面表格和 390px 移动端卡片：数值与作用域一致、后台刷新仍可操作、500 后页面保留配置且没有重复错误弹窗、成功后恢复；选择/标签展开和编辑草稿在刷新后保持。显示异常使用浏览器请求拦截注入，转发验证另外使用真实网关入口。
- 实际 Redis 协议测试服务和可控 HTTP 上游验证：有限/无限制渠道普通请求成功后归零，上游 500/504 后释放；持续输出的 125 秒流式请求在超过 120 秒租期后计数仍为 1，结束后归零；独立展示查询失败不取消该流；已开始输出的流式客户端断开后归零；有限并发的 Redis 预留失败仍返回 503。

执行门禁：

```bash
# web/: 先完成前端构建，再构建嵌入 web/dist 的 Go 主程序。
NODE_OPTIONS=--no-experimental-webstorage bun run test
bun run build:check
# 根模块
GOWORK=off go test ./...
GOWORK=off go build ./...
GOWORK=off go vet ./...
go test -race ./controller ./service -run 'ChannelConcurrency|MemoryChannelConcurrency|RedisChannelConcurrency' -count=1
# relaykit/: 独立模块
GOWORK=off go test ./...
GOWORK=off go build ./...
GOWORK=off go vet ./...
```

受影响前端文件另通过 oxlint 和保留版权头的 oxfmt 格式检查。此验证不包含生产部署、Docker 镜像发布或线上配置修改。

2026-09-07 发布前再次通过根模块和独立 RelayKit 的 test/build/vet、并发 race、前端 38 个文件 / 196 项测试、类型检查、生产构建、受影响文件 lint/format 和 `git diff --check`。两个新增前端测试已归入模块的 `__tests__` 目录；运行时验证沿用上述 2026-09-05 记录。

`v1.0.0-rc.25-pro.5` 已从 `abfde8c0b5afd88ea784166128f248b5d1fe30fe` 发布：[跨平台 Release](https://github.com/streamsc/new-api-pro/actions/runs/34079619869) 与 [GHCR](https://github.com/streamsc/new-api-pro/actions/runs/34079619819) 均通过。四份二进制的发布校验和与 GitHub 资产摘要一致；容器通过 OCI/provenance、运行配置和 amd64/arm64 冒烟检查，两个平台及索引的 Cosign 签名完成。远端 OCI 索引摘要为 `sha256:a22b1e4a92da2d61ec00d4256fe36d4484107a69bbbc4ddba478379508612082`。本次发布未部署到生产，也未执行 Artifactory 导入。

### Existing cancellation limitation

原始基线 `32507fc4` 与补丁版本均实测复现：普通请求在上游尚未返回响应头时，客户端主动取消或客户端超时退出后，预留仍保留，直到上游请求完成才释放。`relay/channel/api_request.go` 的上游请求使用 `http.NewRequest`，未绑定入站请求上下文；本次未修改该路径。已开始输出的流式请求断开后释放正常。

因此“所有取消/超时都及时释放”的既有机制回归项**未通过**，不能视为本补丁已经修复。该缺陷独立跟进；它不改变本期 `in_flight` 为“已预留且未释放”的展示语义，但再次说明此采样值不能用于保证请求已经排空。本次未新增失败测试锁定错误行为，也未扩大为转发取消策略改造。

## Retired patches

| Patch | Reason |
| --- | --- |
| Classic frontend dependency isolation | 上游已删除 Classic 并将默认前端扁平化到 `web/`；重放会重新引入无效构建路径。 |

## Creating the next downstream release

```bash
git fetch upstream --prune --tags
git switch -c release/<new-upstream-version>-pro <new-upstream-tag>
git cherry-pick -x 80262dd2 9eb80a60 3fe62569
git cherry-pick -x 2df032b3 22172f15 760abc15 310329bf
git cherry-pick -x 510d170b 6f84ab0c 6dd57eeb
git cherry-pick -x 653265fd
git cherry-pick -x 0fc4e495
git cherry-pick -x dde5ef0f2
```

若上游移动 DTO 或构建模块边界，应按新边界迁移补丁和测试，不保留旧目录兼容副本。若上游已包含等价行为，验证后将补丁标记为 `upstreamed`。

## Release validation

1. 执行根模块和 `relaykit` 的 Go vet、build 和 test，以及前端单元测试、类型检查、lint、格式检查和生产构建。
2. 构建 Docker `release` target，验证 `web/dist` embed、`/data`、入口点、版本和 `/api/status`。
3. tag workflow 验证 amd64/arm64 OCI manifest、attestation、运行配置、双架构 smoke 和 Cosign 签名。
4. 需要导入 Artifactory 7.71.21 时另行执行 `skopeo copy --all`；该导入不属于仓库自动发布流程。

## Release history

- `v1.0.0-rc.21-pro.1` 至 `.4`：建立 GHCR、多平台二进制、版本门禁和 Artifactory 兼容发布流程。
- `v1.0.0-rc.21-pro.5`：加入 Redis Sentinel 支持。
- `v1.0.0-rc.22-pro.1`：基于官方 rc.22 重建补丁队列，删除 Classic 路径并完成 JWT Session 迁移。
- `v1.0.0-rc.24-pro.1`：基于官方 rc.24 重建补丁队列并适配独立 RelayKit 模块。
- `release/v1.0.0-rc.25-pro`：基于官方 rc.25 重建补丁队列，纳入渠道测试、参数透传和额度结算修复。
- `v1.0.0-rc.25-pro.3`：增加标准 Relay 渠道并发控制、在途计数，以及 Redis/单实例两种运行语义。
- `v1.0.0-rc.25-pro.4`：增加基于已认证用户或令牌身份的上游请求头 HMAC 映射。
- `v1.0.0-rc.25-pro.5`：完善 Issue #3 的渠道分钟采样、未知值及作用域展示；保留并单独披露等待上游响应头期间的取消释放缺陷。
