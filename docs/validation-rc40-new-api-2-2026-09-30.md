# new-api-2 升级演练记录

日期：2026-09-30（Asia/Shanghai）。用户授权在 `ubuntu@129.146.74.208` 的 `new-api-2` 测试，外部地址为 <https://mapix.aiinall6.top/>。

## 部署结果

- 原版本：`v1.0.0-rc.25-pro.3`。
- 当前测试版本：`v1.0.0-rc.40-pro.test.53f32bf3f`，代码提交 `53f32bf3f`，含响应体取消后停止重试修复。
- 测试镜像：`new-api-pro:rc40-test-53f32bf3f`，保留旧镜像的运行环境，替换本地构建的 Linux arm64 二进制及内嵌前端；不是正式多架构、签名发布镜像。
- 构建：前端设置上述版本后执行 `bun run build`；后端使用 `CGO_ENABLED=0 GOOS=linux GOARCH=arm64 GOWORK=off GOEXPERIMENT=greenteagc go build -p 2` 并注入版本。
- 二进制 SHA-256：`68e76cc70a95b4f1f3e7842887bd77b9c1d2a41907faba0952cf3537793fb480`，本机与远端一致。
- 修改远端 `/home/ubuntu/new-api-workspace/docker-compose.yml` 中 `new-api-2` 的 image，解析后的 Compose 配置对比确认没有其他服务配置变化。仅执行该服务的 `up -d --no-deps --pull never`。
- 设置 `quota_setting.trust_quota_usd=0`。原 Redis、SESSION_SECRET、数据和日志挂载保持原配置。
- 2026-09-30 11:36 左右完成切换，停写到健康恢复约 **4.5 秒**。原 `new-api` 容器仍保持 2026-08-26 的启动时间。

## 数据库与回滚演练

实际数据库为 MySQL **8.4.10**，主库与日志共库；3 个用户、2 个渠道、3 个 Token。钱包四列均已为 BIGINT，无 options 重复或空 key。

1. 在线一致性备份：`mysqldump --single-transaction --routines --triggers --hex-blob --set-gtid-purged=OFF --no-tablespaces`。
2. 创建独立 MySQL 容器，恢复备份；使用 internal Docker 网络阻止演练期间向真实上游发送请求。
3. rc.40 启动两次，比较用户钱包、Token 额度与 key、渠道 key/setting/header_override/状态/模型、options 的有序查询 SHA-256，全部一致。第二次启动列 schema 指纹一致。
4. 在该隔离库中完整恢复原备份，再启动旧 rc.25-pro.3 镜像，健康检查通过，关键数据指纹一致。
5. 实际切换前优雅停止 `new-api-2`，生成最终停写备份和 data-2 归档；先用不发布端口的迁移容器升级实际库，检查健康与上述指纹，再启动对外服务。关键数据一致。

最初隔离副本的宿主机端口探测不可达，改用容器内 HTTP 健康检查确认成功；未因此改变隔离网络策略或跳过迁移检查。

## 应用验证与限制

通过：

- 本机访问外部域名 `/api/status` 返回新版本；服务器本地端口 3001 同样返回新版本，容器健康状态为 healthy。
- 浏览器首页和登录页正常渲染；没有登录管理员账户，因此未验收登录后的渠道编辑页面。
- 使用现有 Token 调用 `/v1/models` 成功。
- `/metrics` 中配置与并发采样成功值均为 1，scope 为 redis，两渠道占位均回到 0。
- 失败请求后用户余额、用户已用额度、Token 剩余额度和已用额度的变化均为 0。

**未通过真实推理验收：** rc.40 调用 `gpt-5.6-luna` 和 `gpt-6-sol` 的 Responses 非流式请求均收到上游 HTTP 403 / `bad_response_status_code`。为辨别升级回归，另将旧镜像连接至恢复后的独立副本，允许其访问原上游，用相同 Token 与 `gpt-5.6-luna` 请求复测，也返回相同 403。错误发生在渠道 1；此前近期成功记录的 token_id 为 0（渠道测试），不能作为同一 Token 的成功基线。未修改渠道密钥、路由或上游权限来掩盖此问题。

因此本次确认了迁移、启动、页面、鉴权、失败退款与并发释放，但**未确认真实成功计费、SSE、音频转录或任务插件端到端可用**。上游 403 需要单独排查，解决后补做有限成功请求及余额核对。服务器自身经域名的 urllib 请求另遇 403，本机 curl 和浏览器访问正常；未更改边缘安全配置。

## 备份与运行维护

远端目录：`/home/ubuntu/new-api-upgrade-53f32bf3f/`，目录权限 0700；含数据库及部署凭据的文件仅在该服务器保留，不纳入 Git。

- `cutover.sql`：实际切换前的停写备份。
- `data-2.tar.gz`：数据目录归档。
- `docker-compose.yml.backup`、`.env.backup`（存在时）及容器 inspect：原部署配置。
- `rehearsal.py`、`rehearsal-result.json`：副本升级、重复启动与旧版恢复验证。
- `rollout.py`、`rollout-result.json`：实际切换记录。
- `smoke-result.json`、`post-upgrade.log`：请求对照及应用日志。
- 旧镜像保留为 `new-api-pro:rc25-pro3-rollback-53f32bf3f`。

隔离对照容器、MySQL 匿名卷及 internal 网络已删除。对外 `new-api-2` 保留新测试版本。

若现在回滚，先停写并备份升级后的增量，核对升级后是否已有真实业务；不能直接恢复 `cutover.sql` 丢弃这些写入。只有确认增量处理方案后，才恢复同一时间点的数据库/数据目录与旧 Compose 镜像。副本上的恢复演练通过不代表可以忽略切换后的业务增量。
