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

**初次真实推理验收失败（下述对照已定位）：** rc.40 调用 `gpt-5.6-luna` 和 `gpt-6-sol` 的 Responses 非流式请求均收到上游 HTTP 403 / `bad_response_status_code`。为辨别升级回归，另将旧镜像连接至恢复后的独立副本，允许其访问原上游，用相同 Token 与 `gpt-5.6-luna` 请求复测，也返回相同 403。错误发生在渠道 1；此前近期成功记录的 token_id 为 0（渠道测试），不能作为同一 Token 的成功基线。未修改渠道密钥、路由或上游权限来掩盖此问题。

进一步对照发现：渠道 1 配置 `header_override={"*":true}`，普通 Token 请求会透传 Python urllib 的默认 User-Agent，而后台渠道测试 `IsChannelTest=true` 跳过通配透传。保持同一 Token、模型、Responses 路径与请求体，只将测试客户端 User-Agent 改为 `Go-http-client/1.1`，非流式和 SSE 请求均返回 **HTTP 200**，终态为 `completed` / `response.completed`。因此之前的 403 与上游链路对 User-Agent 的处理有关，并非 Responses 路径不支持，也不是本次升级独有问题；尚未区分具体是上游应用还是边缘规则返回 403。

两笔请求各返回 input_tokens=4391、output_tokens=5（包含上游注入/缓存的输入），用户余额减少、用户已用增加、Token 剩余减少及 Token 已用增加均为 **1228 quota**。真实非流式、SSE 与成功扣费一致性已通过。只修改测试请求头，没有修改渠道通配透传设置、路由或上游安全规则。新增证据在远端 `smoke-user-agent-result.json`。

仍未验收音频转录、任务插件及登录后的管理 UI。服务器自身经域名的 urllib 请求另遇 403，本机 curl 和浏览器访问正常；未更改边缘安全配置。

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


## 13:27 完整请求复测

使用 `Go-http-client/1.1`，模型 `gpt-6-sol`，相同现有 Token，未修改渠道配置：

| 请求 | 结果 | 耗时 |
| --- | --- | --- |
| Chat Completions 非流式 | HTTP 200，有效 choices | 1.74 秒 |
| Chat Completions 流式 | HTTP 200，有文本及 `[DONE]`、usage | 1.88 秒 |
| Responses 非流式 | HTTP 200，completed | 1.52 秒 |
| Responses 流式 | HTTP 200，response.completed、usage | 1.64 秒 |

四笔成功日志共 1300 quota；用户余额减少、用户已用增加、Token 剩余减少、Token 已用增加均为 1300，与日志一致。两渠道 Redis 并发计数归零，配置/并发采样成功值均为 1；容器 healthy，外部域名状态接口返回正确测试版本。原始结果保存在远端 `full-smoke-retest.json`。本轮未覆盖音频、任务插件或并发压测。

## 下游补丁专项验收

在运行中的 `new-api-2` 创建临时 OpenAI 渠道，空模型列表、不添加 abilities，通过现有管理员 Token 指定渠道访问。原渠道 1/2 的状态、setting、header_override 和模型列表逐项保持一致。可控上游只监听该容器网络命名空间的 `127.0.0.1:19340`，未修改防火墙、开放公网端口或向真实上游发送此批请求。

最终结果（远端 `specialist-result.json`）：

- **并发上限**：设置 max_concurrency=1，第一笔阻塞请求占位为 1；第二笔返回 429 / `channel_concurrency_limit_exceeded`，可控上游只收到第一笔。
- **客户端取消**：响应头前和响应体中途断开，均观察到上游连接关闭，计数分别约 0.188/0.162 秒归零；渠道保持启用，后续请求成功。这里使用管理员指定渠道，天然禁止跨渠道重试；不单凭此结果证明普通路由重试策略，未固定渠道的取消回归由仓库 controller 测试覆盖。
- **HMAC**：按部署中实际密钥独立计算 user_id/token_id 签名，收到的请求头逐一匹配；客户端伪造的同名签名头被正确覆盖，两个来源签名不同；普通 `X-Passthrough` 透传值保留。签名和密钥未写入验证报告。
- **音频流**：上传 1 秒 WAV，转录和翻译均验证 multipart 的文件及 `stream=true` 到达上游，响应为 SSE。首事件分别约 0.006/0.005 秒到达，总耗时 1.018/1.012 秒，确认持续转发；结束后计数归零。
- **音频 usage**：两条成功账务日志均记录上游给出的 4 个输入、5 个输出 token。这里验证的是协议转发和 usage 入账，不能替代真实语音模型识别质量或厂商账单验收。

首轮宿主机端口方案被网络规则阻断，已清理并改用回环监听。一次成功请求返回后立即发起下一笔曾触发并发 429；补充等待前一笔结算/释放占位后，音频复测通过，没有修改应用实现或并发限制。

临时渠道与可控上游均已删除，保留少量专项测试账务/错误日志作为证据，没有回写修改余额。管理页面验收尝试连接已有 Edge 会话时超时，未获得登录后的 UI；当前不能声称渠道编辑、采样展示或插件配置已完成浏览器验收。Redis/Sentinel 故障注入未在共用运行实例上执行，沿用此前隔离环境验证结果。
