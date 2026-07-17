# Downstream Patch Queue

本仓库以 `QuantumNous/new-api` 的正式 Release tag 为生产基线，并以线性补丁队列维护暂未进入上游的修复和增强。

## Branch model

- `main`：仅快进跟踪 `upstream/main`，用于观察上游变化和创建上游 PR，不承载下游定制。
- `release/<upstream-version>-pro`：从官方 Release tag 创建，按本文件顺序应用 active 补丁。
- `pr/<patch-id>`：需要贡献上游时，从最新 `upstream/main` 创建并重新应用对应补丁。

发布过的 tag 不移动，旧 release 分支不改写。相同上游版本的后续修复使用递增的 `.2`、`.3`；上游发布新版本时创建新的 release 分支，不把旧 release 分支 merge 到新分支。

## Active patches for `v1.0.0-rc.21-pro`

| Order | Patch ID | Commits | Purpose | Upstream | Validation | Status |
| --- | --- | --- | --- | --- | --- | --- |
| 1 | `audio-stt-streaming` | `d46fcab6`, `fc63b941` | 支持 `/v1/audio/transcriptions` 和 `/v1/audio/translations` 的 SSE 流式响应，并覆盖真实 multipart `stream=true` 请求 | [QuantumNous/new-api#5394](https://github.com/QuantumNous/new-api/pull/5394) | `go test ./dto ./relay/helper ./relay/channel/openai` | active |
| 2 | `artifactory-image-manifest` | `80ef4a6e`, `fd9a41ce`, `9ebf0de7` | 通过最终 `scratch` release 阶段重生成根文件系统 layer；单次构建 amd64/arm64 OCI Index，保留 minimal provenance、关闭 SBOM，并验证 attestation、运行配置和双架构 HTTP 冒烟测试 | 未提交 | GitHub Actions Docker build、raw OCI assertions、双架构 `/api/status`、Cosign；发布前在目标 Artifactory 执行 `skopeo copy --all` | active |
| 3 | `release-quality-gate` | `dcc944fd`, `9cf6562f`, `cab4dd81`, `c5d7f0f7`, `14809b42` | 发布前运行完整 Go 测试，隔离 default/classic 前端依赖，为分支预检生成无 tag 版本号，使用完整 Go module 路径注入并验证版本，三个平台构建完成后统一创建 GitHub prerelease | fork infrastructure | `go test ./...`、default/classic build、Linux/macOS/Windows build、`new-api -version` | permanent |

## Creating the next downstream release

```bash
git fetch upstream --prune --tags
git switch -c release/<new-upstream-version>-pro <new-upstream-tag>
git cherry-pick -x d46fcab6 fc63b941
git cherry-pick -x 80ef4a6e fd9a41ce 9ebf0de7
git cherry-pick -x dcc944fd 9cf6562f cab4dd81 c5d7f0f7 14809b42
```

逐项解决冲突并执行补丁对应的测试。若上游 Release 已包含某项修复，先验证等价行为，再将该项标记为 `upstreamed` 并从新分支的 cherry-pick 列表移除。

## Adding a downstream enhancement

1. 从当前 release 分支创建独立 feature 分支。
2. 保持提交聚焦、线性且可单独 cherry-pick；功能、测试和发布基础设施分别提交。
3. 在本表中登记补丁 ID、提交范围、依赖、上游链接与测试命令。
4. 如果准备贡献上游，从 `upstream/main` 创建 `pr/<patch-id>`，重新应用补丁并单独验证。
5. 上游合并后，只有当新的官方 Release tag 已包含该修复时，才从下一个下游版本移除补丁。

建议在本地启用冲突复用：

```bash
git config rerere.enabled true
```

## Release history

- `v1.0.0-rc.21-pro.1`：首个 GHCR/GitHub Release；镜像有效，但平台二进制的版本 metadata 仍显示 `v0.0.0`，已由 `.2` 替代。
- `v1.0.0-rc.21-pro.2`：修正平台二进制 linker 路径并作为当前推荐版本。
- `v1.0.0-rc.21-pro.3`：平台二进制与 OCI 镜像构建成功，但 GHCR 门禁错误地拒绝 BuildKit 标准 OCI attestation config，导致后续双架构冒烟和 Cosign 签名未执行；保留 tag 并由 `.4` 替代。
- `v1.0.0-rc.21-pro.4`：修正 attestation config 门禁；OCI Index、minimal SLSA provenance、双架构运行配置、双架构 `/api/status`、Cosign 签名及平台二进制发布全部通过，作为当前推荐版本。
