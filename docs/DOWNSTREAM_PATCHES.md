# Downstream Patch Queue

本仓库以 `QuantumNous/new-api` 的正式 Release tag 为生产基线，并以线性补丁队列维护暂未进入上游的修复、发布基础设施和增强。

## Branch model

- `main`：仅快进跟踪 `upstream/main`，用于观察上游变化和创建上游 PR，不承载下游定制。
- `release/<upstream-version>-pro`：从官方 Release tag 创建，按本文件顺序应用 active 补丁。
- `pr/<patch-id>`：需要贡献上游时，从最新 `upstream/main` 创建并重新应用对应补丁。

发布过的 tag 不移动，旧 release 分支不改写。上游发布新版本时创建新的 release 分支，不把旧 release 分支 merge 到新分支。

## Active patches for `v1.0.0-rc.24-pro`

| Order | Patch ID | Commits | Original sources | Purpose | Validation | Status |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `redis-sentinel` | `e5b790ee`, `d95ca74d`, `5597524e` | `5f89fd94`, `24a72b92`, `4a7b74fb` | 通过 go-redis failover client 支持 Sentinel 主节点发现与自动故障转移；同时服务 JWT Session 缓存、撤销 tombstone、鉴权 fence 和限流 Lua | `go test ./common ./model ./middleware ./service` | active |
| 2 | `artifactory-release-infrastructure` | `04fcff85`, `15497889`, `c7aa0403` | `80ef4a6e`, `dcc944fd`, `cab4dd81`, `c5d7f0f7`, `14809b42`, `fd9a41ce`, `9ebf0de7`, `f0d5ed21`, `0e0e3c88` | 保留 `web/dist`、RelayKit 双模块门禁、版本校验、最终 `scratch` release 层、Artifactory 7.71.21 兼容、多架构 OCI Index、minimal provenance、无 SBOM、双架构冒烟和 Cosign 签名 | Workflow YAML、`make test`、前端 build、Docker `builder`/`release`、版本和 `/api/status` | permanent |
| 3 | `audio-stt-streaming` | `4bc2bd80`, `eaae8337`, `841466a9` | `d46fcab6`, `fc63b941` | 在 `relaykit/dto` 接口上支持 `/v1/audio/transcriptions` 和 `/v1/audio/translations` SSE 响应，并覆盖真实 multipart `stream=true` 请求 | `go test ./relay/helper ./relay/channel/openai` 和 `cd relaykit && go test ./dto` | active |

## Retired patches

| Patch | Source | Reason |
| --- | --- | --- |
| Classic frontend dependency isolation | `9cf6562f` | 上游已删除 Classic 并将默认前端扁平化到 `web/`；重放会重新引入无效构建路径。 |

## Creating the next downstream release

```bash
git fetch upstream --prune --tags
git switch -c release/<new-upstream-version>-pro <new-upstream-tag>
git cherry-pick -x e5b790ee d95ca74d 5597524e
git cherry-pick -x 04fcff85 15497889 c7aa0403
git cherry-pick -x 4bc2bd80 eaae8337 841466a9
```

若上游移动 DTO 或构建模块边界，应按新边界迁移补丁和测试，不保留旧目录兼容副本。若上游已包含等价行为，验证后将补丁标记为 `upstreamed`。

## Release validation

1. 执行根模块和 `relaykit` 的 Go 测试，以及前端单元测试、类型检查、lint、格式检查和生产构建。
2. 构建 Docker `release` target，验证 `web/dist` embed、`/data`、入口点、版本和 `/api/status`。
3. tag workflow 验证 amd64/arm64 OCI manifest、attestation、运行配置、双架构 smoke 和 Cosign 签名。
4. 需要导入 Artifactory 7.71.21 时执行 `skopeo copy --all` 并确认 index、platform manifests 和 attestations 均可读取。

## Release history

- `v1.0.0-rc.21-pro.1` 至 `.4`：建立 GHCR、多平台二进制、版本门禁和 Artifactory 兼容发布流程。
- `v1.0.0-rc.21-pro.5`：加入 Redis Sentinel 支持。
- `v1.0.0-rc.22-pro.1`：基于官方 rc.22 重建补丁队列，删除 Classic 路径并完成 JWT Session 迁移。
- `release/v1.0.0-rc.24-pro`：基于官方 rc.24 重建补丁队列并适配独立 RelayKit 模块。
