# Upgrade to v1.0.0-rc.24-pro

本文用于审阅从 `v1.0.0-rc.22-pro.1` 升级到基于官方 `v1.0.0-rc.24` 的 Pro 分支。官方基线为签名 tag `v1.0.0-rc.24`，提交 `5c3abffe8572aa8a49f15c3916707d2019d66af4`。

## Change summary

- 上游把 DTO、错误类型和协议转换拆分为独立 `relaykit` Go 模块；根模块通过本地 `replace` 使用它。
- 新增 New API 渠道类型、按渠道 HTTP 协议与 HTTP/2 connection shard 控制、DeepSeek Responses、zstd 请求解压和更精确的模型分类。
- Auto Group Token 可保存有序分组快照；访问令牌与推广额度转移接口增加按用户的关键操作限流。
- 修复 HTTP/2 stream reset 重放、分层重试切组计费、兑换码精度、Suno/任务结算及多处并发更新覆盖问题。
- 保留 Pro 的 Redis Sentinel、音频转录/翻译 SSE、Artifactory 7.71.21、多架构 OCI 和发布门禁。

## Public contracts

- 新增渠道类型 `60`（New API）。该渠道必须设置 Base URL，并支持 OpenAI、Claude、Gemini 和 Alpha Search 等端点映射。
- 渠道 `setting` JSON 新增 `http_protocol`（空值/`auto`/`http1`）和 `http2_connection_shards`（0-8；`http1` 时不得大于 1）。非法值在保存渠道时直接拒绝。
- Token 创建、读取和更新支持 `auto_groups`。`GET /api/token/auto-groups` 返回当前用户可用分组和 `max_count`；空数组/空存储表示继承全局 Auto Groups。
- `relaykit` 是独立 Go module。CI、镜像和本地验证必须分别测试根模块与 `relaykit`，Docker 依赖下载前必须提供 `relaykit/go.mod`。

## Database migration

启动时 GORM AutoMigrate 在 `tokens` 表增加：

- `auto_groups`：TEXT，保存有序 JSON 字符串；旧 Token 迁移后为空并继续继承全局 Auto Groups。

rc.22 已引入的 `user_sessions`、`auth_flows`、`external_identity_claims` 和 `users.auth_version` 在本次升级中没有新增替代路径，仍是唯一会话与外部身份事实来源。

升级前记录 Token 数量并备份数据库：

```sql
SELECT COUNT(*) AS token_count FROM tokens;
```

升级后确认行数不变，并检查自定义 Auto Group 数据规模：

```sql
SELECT COUNT(*) AS token_count FROM tokens;
SELECT COUNT(*) AS custom_auto_group_tokens
FROM tokens
WHERE auto_groups IS NOT NULL AND TRIM(auto_groups) <> '';
```

大 `tokens` 表增加列可能持有 DDL 锁。MySQL/PostgreSQL 应在维护窗口观察锁等待和复制延迟；SQLite 应确保数据库文件所在磁盘有足够空间保存升级前备份。

## Required configuration

- 所有节点继续使用完全一致的 `SESSION_SECRET`；共享 Redis 节点继续使用一致的 `CRYPTO_SECRET`。
- HTTPS 入口继续精确配置 `SESSION_COOKIE_SECURE=true`、`SESSION_COOKIE_TRUSTED_URL` 和代理自身的 `TRUSTED_PROXIES`。
- Sentinel 同时配置 `REDIS_CONN_STRING`、`REDIS_SENTINEL_MASTER_NAME` 和 `REDIS_SENTINEL_ADDRS`；认证参数见 `docs/redis-sentinel.md`。
- 可用 `SQL_SLOW_THRESHOLD_MS` 配置 GORM 慢查询阈值；`0` 关闭，非法值回退到 200 ms。
- 关键操作限流沿用 `CRITICAL_RATE_LIMIT_ENABLE`、`CRITICAL_RATE_LIMIT` 和 `CRITICAL_RATE_LIMIT_DURATION`，升级前应确认现值符合访问令牌轮换和额度转移频率。

## Deployment and rollback

1. 进入维护窗口并停止 rc.22-pro 写流量，避免新旧节点同时更新 Token 时丢失 `auto_groups`。
2. 备份数据库，记录 Token 行数，核对 Session/Crypto secret、Cookie Origin、可信代理和 Sentinel 配置。
3. 仅启动一个 rc.24-pro 主节点完成 AutoMigrate，检查日志没有 DDL、RelayKit 配置或渠道校验错误。
4. 执行迁移后 SQL，验证现有 Token、PAT relay、登录刷新、设备撤销、Auto Group Token 和 New API 渠道。
5. 启动其余节点，验证 `/api/status`、音频 SSE、工具计费、分层重试和 Sentinel failover 监控。

回滚时停止全部 rc.24-pro 节点并恢复升级前数据库备份，再部署 rc.22-pro.1。仅切回旧二进制会保留 rc.24 写入的 Token 分组语义，无法构成完整回滚。

## Operational risks

- RelayKit 成为嵌套模块；只执行根目录 `go test ./...` 会漏掉协议转换测试，发布门禁已改为 `make test`。
- HTTP/2 自动重放依赖可重放 request body；上线后应关注大 multipart 请求的磁盘临时文件、上游 reset 和重复计费日志。
- 分层重试现在按最终分组结算；应抽查切组请求的日志分组、倍率和最终扣费一致。
- `auto_groups` 保存 Token 创建/更新时的有序快照；全局分组删除后会在读取时过滤，空值则动态继承全局设置。
- 关键操作限流按用户隔离，但仍使用固定窗口；窗口边界附近存在固定窗口固有的突发边界。
- New API 渠道应填写不带 `/v1` 后缀的服务根地址，避免协议路由产生歧义。

## Validation results

| Check | Result |
| --- | --- |
| Targeted Go tests | Passed: Sentinel, authentication, Token migration, channel, billing, audio and task packages |
| Root module `go test ./...` | Passed |
| RelayKit `go test ./...` | Passed |
| Final `make test` | Passed: root and RelayKit modules |
| SQLite Token and Session migration tests | Passed |
| MySQL/PostgreSQL Token and Session migration tests | Skipped: `TEST_MYSQL_DSN` and `TEST_POSTGRES_DSN` are not configured |
| `bun test` | Upstream rc.24 baseline failure: 124 passed, 9 failed, 6 loader errors across 31 files; this branch has no `web/` diff |
| `bun run typecheck` | Passed |
| `bun run lint` | Upstream rc.24 baseline failure; this branch has no `web/` diff |
| `bun run format:check` | Upstream rc.24 baseline failure in 4 files; this branch has no `web/` diff |
| `bun run build` | Passed with `preflight-v1.0.0-rc.24-pro` |
| Workflow YAML, path scan, and `git diff --check` | Passed |
| Preflight binary version | Passed: `preflight-v1.0.0-rc.24-pro` |
| Docker `builder` and `release` smoke | Passed: frontend builder, RelayKit-aware Go build, scratch layout, version and `/api/status`; temporary build context used `GOPROXY=https://goproxy.cn,direct` |
| GitHub release and GHCR image | Post-tag publication gate |

The failing frontend checks are byte-for-byte upstream rc.24 behavior: `git diff v1.0.0-rc.24 -- web` is empty. This release does not mix unrelated upstream frontend cleanup into the Pro patch queue.
