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
| 4 | `channel-relay-concurrency` | `653265fd` | downstream | 为标准 Relay 渠道增加可配置并发上限和在途计数；Redis 模式跨实例共享租约，存储不可用时明确失败 | 根模块与 RelayKit vet/build/test、并发 race、前端测试/typecheck/build、受影响文件 lint/format | active |
| 5 | `context-hmac-header-override` | `0fc4e495` | downstream | 为 `header_override` 增加基于已认证用户或令牌 ID 的稳定 HMAC 占位符，不向上游暴露原始身份 | `go test -race ./relay/channel -run ContextHMAC`、根模块 vet/build/test、前端测试/typecheck/build、受影响文件 lint/format | active |

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
