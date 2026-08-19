# Upgrade to v1.0.0-rc.25-pro

本文用于从 `v1.0.0-rc.24-pro.1` 升级到基于官方 `v1.0.0-rc.25` 的 Pro 版本。官方基线标签指向提交 `f116414284162ad15d8925f7bca494c109b83e93`。

## Change summary

- 渠道自动测试增加 `auto_ban_only` 模式和可配置并发度，失败渠道处理更明确。
- Gateway 渠道增加请求字段透传控制；参数覆盖规则可读取请求用户、Token 分组和实际使用分组。
- Advanced Custom 渠道可配置模型列表与余额查询管理路由，Gemini 风格的 `/v1/models` 请求返回模型列表。
- Responses 转换保留 penalty、prompt cache 和 reasoning/tool-call 信息，并修复 cached token 结算。
- 充值、退款、预扣和额度缓存同步改为带边界检查的原子更新，避免重复回调、并发超扣和缓存余额滞后。
- 保留 Pro 的 Redis Sentinel、音频转录/翻译 SSE、Artifactory 7.71.21 容器兼容和 GHCR 多架构发布门禁。

## Public contracts

- 系统设置 `monitor_setting.channel_test_concurrency` 接受 `1-32`，默认值为 `1`；渠道测试模式新增 `auto_ban_only`。
- Advanced Custom 管理路由新增 `/v1/dashboard/billing/credit_grants`，必须使用 `none` converter 且不能绑定模型占位符。
- Gateway 渠道设置支持 `pass_through_body_enabled` 和按协议划分的字段透传开关。开启完整请求体透传会绕过协议转换，应只用于已验证的兼容上游。
- 参数覆盖上下文新增 `user_id`、`user_group`、`token_group` 和 `using_group`。
- Responses Compact 不再根据模型名的 `-compact` 后缀自动选择；调用方应使用明确端点，渠道必须声明对应能力。
- 音频 multipart 请求继续接受 `stream=true`，转录和翻译端点以 SSE 返回上游流。

## Database and accounting

本次没有新增数据库表或列，不需要执行结构迁移。rc.25 调整的是已有用户、Token、渠道和充值记录的更新方式：

- 用户与 Token 额度预扣使用带余额条件的原子更新；Redis 缓存命中时同步更新缓存和数据库持久化队列。
- 充值结算在事务中锁定订单并原子增加额度，重复成功回调不会重复入账。
- 充值额度超过数据库可表示范围时明确失败，不再截断或溢出。
- 渠道健康状态只更新状态流程拥有的字段，避免旧快照覆盖密钥、计数器或配置。

升级前应停止当前实例并备份数据库。SQLite 直接备份数据库文件；MySQL/PostgreSQL 使用现有备份方式。升级不会主动重写历史记录。

## Required configuration

- 未配置新渠道测试并发度时保持串行执行。提高并发度前确认上游渠道允许相应测试流量。
- Sentinel 继续同时要求 `REDIS_CONN_STRING`、`REDIS_SENTINEL_MASTER_NAME` 和 `REDIS_SENTINEL_ADDRS`；认证参数见 `docs/redis-sentinel.md`。
- 检查 Gateway 渠道的字段透传开关；默认关闭，不自动迁移已有渠道设置。
- 检查依赖模型名 `-compact` 的调用，改为显式 Responses Compact 路由。

## Deployment and rollback

`v1.0.0-rc.25-pro.1` 的容器工作流在镜像推送后因 attestation 配置类型校验错误而停止，没有完成运行时校验、双架构 smoke 和 Cosign 签名。该标签保持不可变并由 `.2` 取代，不应作为部署目标。

1. 停止 `v1.0.0-rc.24-pro.1` 实例，备份数据库并记录当前镜像标签或 digest。
2. 部署 `ghcr.io/streamsc/new-api-pro:v1.0.0-rc.25-pro.2`，确认启动日志没有数据库、Redis 或渠道设置错误。
3. 验证 `/api/status`、登录、普通 Chat/Responses、音频 SSE、渠道测试、充值/兑换和 Sentinel 连接。
4. 抽查使用日志中的 cached token、reasoning effort、条件倍率和最终扣费。

回滚时停止 rc.25-pro 实例并重新部署 `v1.0.0-rc.24-pro.1`。由于没有数据库结构迁移，正常回滚不需要恢复备份；仅在确认数据已损坏时恢复升级前备份。

## Operational risks

- 渠道测试并发度过高会同时占用多个上游连接并产生测试用量，默认值 `1` 最稳妥。
- 完整请求体或字段透传会改变协议转换边界；错误配置可能向上游发送不支持或敏感字段。
- Redis 中的额度缓存现在参与原子预扣；Sentinel 或 Redis 异常应明确出现在日志中，不能把缓存错误当作成功结算。
- 充值上限检查可能拒绝此前会被截断或溢出的异常订单，这是预期的安全行为。
- `-compact` 后缀不再驱动路由，未迁移的调用可能落到普通 Responses 端点。

## Validation results

| Check | Result |
| --- | --- |
| Backend vet, build, and root tests | Passed |
| RelayKit independent vet, build, and tests | Passed |
| Sentinel and audio targeted tests | Passed |
| Frontend test, typecheck, lint, format, and build | Vitest 179/179, typecheck and build passed. Lint and five-file format checks are unchanged upstream rc.25 baseline failures; this branch has no `web/` diff. |
| Preflight binary version | Passed: `preflight-v1.0.0-rc.25-pro` |
| Local Docker release image | Blocked by local container-network dependency downloads after three bounded attempts; no source/build error was reported. The GHCR tag workflow remains the mandatory container gate. |
| GitHub branch preflight | Passed: [run 32255462261](https://github.com/streamsc/new-api-pro/actions/runs/32255462261), including Linux, macOS, and Windows artifacts |
| GitHub Release `.1` | Passed: [run 32256570739](https://github.com/streamsc/new-api-pro/actions/runs/32256570739) |
| GHCR `.1` | Superseded: image push completed, but an incorrect attestation config assertion skipped runtime validation, dual-architecture smoke, and Cosign signing |
| Corrected `.2` publication | GitHub Release and GHCR workflows are mandatory deployment gates |
