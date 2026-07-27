# Upgrade to v1.0.0-rc.22-pro

本文用于审阅从 `v1.0.0-rc.21-pro.5` 升级到基于官方 `v1.0.0-rc.22` 的 Pro 分支。官方基线提交为 `bc14c18f6024e79cba1c08d02cd007796e12d668`。

## Change summary

- 面板鉴权从 Gin Cookie Session 改为短期 Access JWT、HttpOnly Refresh Cookie 和服务端 `user_sessions` 控制面。
- 增加 Refresh Token 轮换、登录设备查看与撤销、`auth_version`、一次性 `auth_flows`、外部身份唯一归属和 Security Proof。
- 删除 Classic 前端；默认前端从 `web/default` 扁平化到 `web`，构建和 Go embed 统一使用 `web/dist`。
- 增加可配置工具调用计费、Sub2API、`POST /v1/alpha/search`、Advanced Custom/Codex 模型发现、腾讯 TokenHub 和 Gemini GA 模型。
- 保留 Pro 的 Redis Sentinel、流式音频、Artifactory 7.71.21、多架构 OCI 和发布质量门禁。

## Public authentication contract

- 登录成功返回 `AuthBundle`，包含 `access_token`、`token_type`、`access_expires_at`、用户信息和当前 Session 信息。
- 面板 API 使用 `Authorization: Bearer <access_token>`；Refresh secret 只保存在 HttpOnly Cookie 中。
- Refresh 和 Logout 分别使用 `POST /api/user/auth/refresh`、`POST /api/user/auth/logout`。
- 登录设备管理使用 `GET /api/user/sessions`、`DELETE /api/user/sessions/:sid` 和 `POST /api/user/sessions/revoke-others`。
- 面板客户端不再依赖 `New-Api-User`。已有 PAT 仍兼容 Bearer 或原单值 Authorization，不要求重建。
- OAuth、2FA、Passkey 和 Telegram 流程使用有目的、用户和 Session 约束的 `flow_token`；自建面板客户端必须同步适配。

## Database migration

主节点启动时通过 GORM AutoMigrate 增加：

- `user_sessions`：Refresh 摘要、Session 版本、用户鉴权版本、状态、过期和撤销信息及复合索引。
- `auth_flows`：一次性 OAuth、2FA、Passkey 和绑定流程及过期/消费索引。
- `external_identity_claims`：外部身份 subject 与用户 provider slot 的双唯一索引。
- `users.auth_version`：迁移后所有现有用户归一为至少 `1`。

迁移会把所有历史 Telegram 绑定（包括软删除用户）写入唯一归属表。升级前必须执行以下跨 SQLite、MySQL、PostgreSQL 通用的预检；返回任何行都必须先人工消除歧义：

```sql
SELECT telegram_id, COUNT(*) AS owner_count
FROM users
WHERE telegram_id IS NOT NULL AND TRIM(telegram_id) <> ''
GROUP BY telegram_id
HAVING COUNT(*) > 1;
```

迁移完成后检查：

```sql
SELECT COUNT(*) AS invalid_auth_versions
FROM users
WHERE auth_version IS NULL OR auth_version < 1;

SELECT COUNT(*) AS telegram_claims
FROM external_identity_claims
WHERE provider = 'telegram';
```

`invalid_auth_versions` 必须为 `0`。Telegram claim 数量应与无重复的历史 Telegram 绑定数一致。

前端选项迁移会把 `ApiInfo`、`Announcements`、`FAQ`、`UptimeKumaUrl` 和 `UptimeKumaSlug` 转换为新的 `console_setting.*` 键，并把 `theme.frontend` 归一为 `default`。成功转换后会删除旧键；格式错误只记录日志且不阻止启动，因此升级后必须检查启动日志和后台展示。

## Required configuration

- 所有节点必须共享主数据库，并配置完全一致的高强度 `SESSION_SECRET`。更换该值会使 Access Token、Refresh Session、临时鉴权流程和 Security Proof 全部失效。
- 共享 Redis 的节点必须使用相同的 `CRYPTO_SECRET`；未显式配置时它跟随 `SESSION_SECRET`。
- HTTPS 生产入口设置 `SESSION_COOKIE_SECURE=true`，并在 `SESSION_COOKIE_TRUSTED_URL` 中列出全部精确 HTTPS Origin，不支持通配符或路径。
- `TRUSTED_PROXIES` 填写代理自身 IP/CIDR；`none` 表示不信任任何代理。非法值会阻止启动。
- Sentinel 部署同时配置 `REDIS_CONN_STRING`、`REDIS_SENTINEL_MASTER_NAME` 和 `REDIS_SENTINEL_ADDRS`；认证参数见 `docs/redis-sentinel.md`。

## Deployment procedure

1. 进入维护窗口，停止 rc.21-pro 面板节点和写流量；不要混合运行 rc.21 与 rc.22。
2. 备份主数据库，并单独导出 `users`、`options` 和现有鉴权相关配置以便快速核对。
3. 执行 Telegram 重复归属预检，确认各节点的 Session/Crypto secret、Cookie Origin 和代理配置一致。
4. 仅启动一个 rc.22-pro 主节点执行迁移；等待数据库迁移、身份 backfill 和前端选项迁移完成。
5. 执行迁移后 SQL，检查启动日志没有外部身份冲突、DDL 错误或前端选项转换错误。
6. 启动其余节点，验证密码/OAuth/2FA/Passkey 登录、Refresh、Logout、设备列表与撤销。
7. 验证 PAT relay、`/api/status`、工具计费日志、Sub2API/Alpha Search、模型发现和任务退款。

所有现有面板登录在升级后均视为失效，用户需要重新登录。这是预期行为，不应通过兼容旧 Cookie 绕过。

## Rollback

1. 停止所有 rc.22-pro 节点，避免新 Session 或选项继续写入。
2. 恢复升级前数据库备份，再部署 rc.21-pro.5；不要只切换旧二进制。
3. 恢复原环境变量和前端镜像，清理升级期间创建的 Redis Session/cache key（或切换到升级前 Redis snapshot/namespace）。
4. 验证旧面板、PAT relay 和后台选项后再恢复流量。

必须恢复数据库备份的原因是前端选项迁移会删除成功转换的旧键；新增鉴权表虽然是 additive，但单独保留它们不能保证旧前端配置完整回滚。

## Operational and billing risks

- GORM 在大 `users` 表上增加 `auth_version` 和索引时可能持有 DDL 锁，应预留维护窗口并监控复制延迟。
- Redis 通用限流改为原子固定窗口，窗口边界附近可能允许约两倍瞬时流量；滚动升级一个窗口内还可能存在时间戳语义差异，因此禁止混部。
- Session Redis 读取失败会回退数据库，但撤销 tombstone、鉴权 fence 和 Lua 在 Sentinel 主从切换期间仍需生产拓扑演练。
- 工具调用附加费会改变 OpenAI/Claude 流式与非流式请求、Alpha Search 和 Sub2API 的最终扣费；上线后按 `other.tool_surcharges` 对账。
- Classic 前端和运行时主题切换已删除；旧 `/console/*` 路由只做兼容跳转，不应继续部署 Classic 静态资源。
- 任务失败退款使用 rc.22 最终状态转换逻辑；上线后抽查并发回调，确认每个任务最多退款一次。

## Validation results

| Check | Result |
| --- | --- |
| `go test ./common ./controller ./middleware ./model ./router ./service` | Passed |
| `go test ./dto ./relay/... ./service ./setting/operation_setting` | Passed |
| `go test ./...` | Passed |
| SQLite Session migration test | Passed |
| MySQL/PostgreSQL Session migration tests | Skipped: `TEST_MYSQL_DSN` and `TEST_POSTGRES_DSN` were not configured |
| `bun test` | Passed: 104 tests |
| `bun run typecheck` | Passed |
| `bun run format:check` | Passed: 1046 files |
| `bun run build` | Passed |
| `bun run lint` | Failed on existing rc.22 upstream files; this branch contains no `web/src` changes |
| Workflow YAML parsing and `git diff --check` | Passed |
| Docker `builder` target | Passed: pinned Bun image, frozen install, and production frontend build completed; context was `83.89kB`, confirming retired `web/default` and `web/classic` artifacts were excluded |
| Docker `release` target and `/api/status` smoke | Passed: the pinned Go base was pulled through `mtw6hbbpueh4iz.xuanyuan.run`, container module download used the reachable local `GOPROXY`, the scratch image returned `preflight-v1.0.0-rc.22-pro` from `-version`, runtime layout checks passed, and `/api/status` returned HTTP 200 with the same version |

The full frontend lint failure is retained as an explicit baseline risk rather than expanding this upgrade into an unrelated cleanup of upstream rc.22 files.
