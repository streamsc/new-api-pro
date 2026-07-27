# Downstream Patch Queue

本仓库以 `QuantumNous/new-api` 的正式 Release tag 为生产基线，并以线性补丁队列维护暂未进入上游的修复、发布基础设施和增强。

## Branch model

- `main`：仅快进跟踪 `upstream/main`，用于观察上游变化和创建上游 PR，不承载下游定制。
- `release/<upstream-version>-pro`：从官方 Release tag 创建，按本文件顺序应用 active 补丁。
- `pr/<patch-id>`：需要贡献上游时，从最新 `upstream/main` 创建并重新应用对应补丁。

发布过的 tag 不移动，旧 release 分支不改写。上游发布新版本时创建新的 release 分支，不把旧 release 分支 merge 到新分支。

## Active patches for `v1.0.0-rc.22-pro`

| Order | Patch ID | Commits | Original sources | Purpose | Validation | Status |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `redis-sentinel` | `65a2b4bb`, `b0c70cdf`, `6e5a7436` | `5f89fd94`, `24a72b92`, `4a7b74fb` | 通过 go-redis failover client 支持 Sentinel 主节点发现与自动故障转移；同时服务 JWT Session 缓存、撤销 tombstone、鉴权 fence 和限流 Lua | `go test ./common ./model ./middleware ./service` | active |
| 2 | `artifactory-release-infrastructure` | `f0d5ed21`, `0e0e3c88` | `80ef4a6e`, `dcc944fd`, `cab4dd81`, `c5d7f0f7`, `14809b42`, `fd9a41ce`, `9ebf0de7` | 在扁平化 `web/dist` 构建上保留完整 Go 门禁、版本校验、最终 `scratch` release 层、Artifactory 7.71.21 兼容、多架构 OCI Index、minimal provenance、无 SBOM、双架构冒烟和 Cosign 签名；忽略旧 worktree 残留的退休前端目录 | Workflow YAML、`go test ./...`、前端 build、Docker `builder`/`release` target、版本和 `/api/status` 冒烟已通过；GHCR/Artifactory 远端验证发布前完成 | permanent |
| 3 | `audio-stt-streaming` | `d30aa563`, `785c12be` | `d46fcab6`, `fc63b941` | 支持 `/v1/audio/transcriptions` 和 `/v1/audio/translations` 的 SSE 流式响应，并覆盖真实 multipart `stream=true` 请求 | `go test ./dto ./relay/helper ./relay/channel/openai` | active |

## Retired patches

| Patch | Source | Reason |
| --- | --- | --- |
| Classic frontend dependency isolation | `9cf6562f` | 上游 rc.22 已删除 Classic 并将默认前端扁平化到 `web/`；重放会重新引入无效的 `web/classic` 构建路径。 |

## Creating the next downstream release

```bash
git fetch upstream --prune --tags
git switch -c release/<new-upstream-version>-pro <new-upstream-tag>
git cherry-pick -x 65a2b4bb b0c70cdf 6e5a7436
git cherry-pick -x f0d5ed21 0e0e3c88
git cherry-pick -x d30aa563 785c12be
```

逐项解决冲突并执行补丁对应测试。若新上游 Release 已包含等价行为，先验证后将补丁标记为 `upstreamed`，不要机械重放。

## Release validation

1. 执行完整 Go、前端单元测试、类型检查、格式检查和生产构建。
2. 构建 Docker `release` target，验证 `web/dist` embed、`/data`、入口点和版本信息。
3. 发布前由 workflow 验证 amd64/arm64 OCI manifest、attestation、运行配置、`/api/status` 和 Cosign 签名。
4. 在目标 Artifactory 7.71.21 上执行 `skopeo copy --all`，确认 index、platform manifests 和 attestations 均可读取。

## Release history

- `v1.0.0-rc.21-pro.1` 至 `.4`：建立 GHCR、多平台二进制、版本门禁和 Artifactory 兼容发布流程。
- `v1.0.0-rc.21-pro.5`：加入 Redis Sentinel 支持。
- `release/v1.0.0-rc.22-pro`：基于官方 rc.22 重建补丁队列，移除 Classic 构建路径；尚未创建发布 tag。
