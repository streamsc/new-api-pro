# User resource limits

## Scope and semantics

One user ID shares concurrency, estimated input TPM and RPM across API keys,
models and gateway replicas. An optional exact-model rule adds a separate allowance per user/model.
Admission is atomic across all six total/model resources;
channel selection and channel limits are a separate admission step. Rejected
admissions do not consume user resources. No queue or output-token cap is added.

Covered: ordinary synchronous/streaming HTTP relay requests: OpenAI chat,
completions, Responses/compact, embeddings, rerank, moderation, images and audio;
Claude messages; Gemini generate/stream/embed; Alpha Search and DeepSeek tools.
Responses WebSocket counts each `response.create`, not the idle connection.
Asynchronous/task-plugin routes, Midjourney and Realtime are excluded, including
task plugins that expose an OpenAI-compatible path. Token-count utility endpoints
are not inference admissions.

Input is estimated before forwarding, using protocol `GetTokenCountMeta` and
`tokenkit`. Text includes supplied conversation/tool input represented by those
DTOs; OpenAI formatting uses the existing message/tool overhead. Images use the
existing model default-size estimates (no remote download during admission).
Audio/video/file metadata use existing fixed estimates (256/8192/4096 tokens);
transcription/translation uploads use the existing audio-duration estimate.
Image edits include supplied JSON or multipart images. Estimates can under- or
over-count media and provider-specific fields; opaque server-side conversation,
file or prompt references cannot reveal their full contents to the gateway.
They are not a strict bound on actual provider input tokens. Billing flags,
prices, balance, cache discounts, output limits and actual usage never modify
resource counts. No actual-usage adjustment is performed.

RPM and TPM are rolling 60-second windows, not minute buckets. Completion,
failure, cancellation and billing refusal after admission do not refund them.
Concurrency lasts until logical completion across serial retries, or 30 minutes,
whichever comes first. Channel slots span individual ordinary HTTP attempts.
Releasing twice or after expiry is harmless. Client resubmission is a new logical
request. Normal timeout/client cancellation still propagates to upstream; releasing
a gateway slot does not prove the remote engine stopped executing.

## Deployment and configuration

- `GATEWAY_REPLICA_MODE=single` (default): memory if Redis is absent, shared Redis
  otherwise. Memory counters reset on process restart; do not run multiple
  independent memory replicas and expect a global limit.
- `GATEWAY_REPLICA_MODE=cluster`: shared Redis required; missing/failed Redis
  initialization fails startup. Existing single Redis and Sentinel connection
  settings apply; Redis Cluster sharding is not added.
- All replicas of one deployment use the same Redis instance and database number.
  Independent deployments must use separate Redis databases or instances, as for
  existing user/token caches. No deployment-ID environment variable is needed.
  User keys use `user_resources:v1:<user_id>:...`; channel keys retain
  `channel_concurrency:v1:<channel_id>`. Startup never deletes slots.

Changing Redis database or memory/shared mode changes the effective counters.
Drain traffic before such changes. Use a drained coordinated upgrade when changing
channel slot lifetime/renewal semantics; do not mix old and new implementations.
Restarting replicas against the same Redis database preserves slots. No
compatibility or recovery coordinator is installed.

Root administrators use **System settings → Security → User resource limits**.
The visual editor manages total defaults, total user overrides, exact-model
defaults and model-specific user overrides. Switch
to JSON to edit the same draft directly. Search by username or ID to select a user;
labels show usernames and IDs while the configuration remains keyed by user ID.
User overrides use a compact table with 20 rows per page. Only visible rows
resolve usernames. Search and select a username or ID to locate its override.
Add/edit drawers apply changes to the shared draft; saving the page publishes them.
The JSON editor always contains the complete configuration, not just the current page.
Existing overrides retain their IDs and settings if a username lookup fails.
Each user resource can inherit its default,
be unlimited, or use an explicit limit; a limit of zero blocks new requests.
Invalid JSON must be corrected before switching back to visual editing. Example:

```json
{
  "defaults": {
    "concurrency": {"enabled": true, "limit": 10},
    "input_tpm": {"enabled": true, "limit": 100000},
    "rpm": {"enabled": true, "limit": 120}
  },
  "users": {
    "42": {"rpm": {"enabled": false, "limit": 0}},
    "43": {"concurrency": {"enabled": true, "limit": 0}}
  },
  "models": {
    "gpt-4o": {
      "defaults": {
        "concurrency": {"enabled": true, "limit": 2},
        "input_tpm": {"enabled": false, "limit": 0},
        "rpm": {"enabled": true, "limit": 20}
      },
      "users": {"42": {"rpm": {"enabled": true, "limit": 30}}}
    }
  }
}
```

Missing user fields inherit defaults. `enabled=false` means unlimited and still
counted. `enabled=true, limit=0` is zero allowance, not unlimited. Limits are
nonnegative integers up to 2147483647. User ID 42 above inherits concurrency/TPM;
43 blocks new admissions. Removing an override restores inheritance. Renaming a
user does not change their resource identity. Reducing limits does not cancel
admitted requests or clear counters.

GET/PUT `/api/option/user_resources` uses existing RootAuth. Personal access
tokens additionally require `option:read` for GET and `option:write` for PUT.
The self-resource endpoint requires `profile:read` for personal access tokens.
Browser sessions continue through existing session authentication. PUT accepts
`{"expected_version":"<GET version>","config":{...},"reason":"capacity review"}`.
The content version is checked against the persisted configuration in the write
transaction. A stale or absent version returns 409 without changing configuration;
the editor keeps the draft so the operator can reload and reconcile it. Existing operation audits record
operator, time, reason and before/after config; the prior config is read under the
same database row lock as the update. Persistence reuses the options table without
schema changes. Other replicas use the existing option polling interval
`SYNC_FREQUENCY` (60 seconds by default): under healthy database access, propagation
is at most one polling interval plus database latency. During DB outage a maximum
propagation time cannot be guaranteed. Generic option writes cannot bypass the
reason/audit endpoint.

`GET /api/user/self/resources` uses current dashboard identity, never a query user
ID. It returns final limits, username/ID, memory/Redis mode and timestamp.
Unavailable reads omit `usage` and set `available=false`. The overview card polls
every five seconds while visible; background/unmounted polling stops. Failed
refreshes hide old numeric values. The 40%/80% colors are UI hints, not alerts.
The legacy ModelRequestRateLimit remains unchanged and independently enforced;
when disabled it does not access its store or group settings.

## Failure behavior

- Confirmed excess: 429 with `user_concurrency_exceeded`,
  `user_input_tpm_exceeded` or `user_rpm_exceeded`.
- A constrained user/store check fails: 503 `user_resource_store_unavailable`.
  Channel checks retain their existing stable error codes and the same 429/503 rule.
- Either total or model limits enabled: a failed joint store operation rejects
  closed. Only when both are unlimited can a statistics failure admit traffic.
- Admitted requests never acquire additional cancellation from Redis failures,
  release failures or slot expiry. Releases use a bounded independent context.
- A timed-out Redis command has an unknown outcome: it may execute after the
  caller gives up. A failed admission can therefore leave a slot/window entry,
  and a timed-out release may still remove its slot. Otherwise the fixed slot
  expiry bounds abandoned occupancy; no retry/rebuild protocol is added.
- Redis recovery immediately resumes checks against existing state. Missing data,
  async replication, failover and partitions can over/under-count, including
  temporary excess. No rebuild barrier or human recovery confirmation exists.
- Window rejection includes Retry-After when enough existing admissions will
  expire to make room. A request larger than its entire TPM budget, zero RPM, or
  unknown concurrency completion gets no fabricated countdown.

## Exact-model rules and lifecycle

Total defaults → total user overrides and model defaults → model user overrides
are independent chains. Both final limits apply; there is no borrowing across
models. Missing fields inherit, disabled fields are unlimited, and enabled zero
blocks every new admission, including a zero-token input. Rules use the exact
client `OriginModelName` before channel mapping. Aliases, wildcards and normalized
provider names are not merged. Model names are valid UTF-8, 1–512 bytes, with no
edge whitespace or control characters.

A model present anywhere in configuration is recorded for **all actual requesting
users**, even if only one user has an override and model defaults are unlimited.
No Cartesian product of users/models is pre-created. Adding a model begins its
history with the next request. Adding a default later can reuse that recorded
window. Deleting a model stops new model recording; existing windows and captured
slots expire normally, and completion still releases the original slots. Re-adding
the same name reuses unexpired state. Limits never cancel admitted requests.

Use a coordinated, drained upgrade: older replicas do not enforce model rules.
Configuration propagation still follows the existing option polling interval.
Model Redis keys encode the exact name with base64url under
`user_resources:v1:<user_id>:model:<encoded>:slots|window|tokens`. The existing
total-user keys are retained. One Lua admission checks both buckets before writing
any capacity. go-redis script retries reuse the same random member; an automatic
replay does not double-charge. This does not make a new client submission idempotent.

## Read APIs and monitoring

`GET /api/user/self/resources?p=1&page_size=20&model=<exact-name>` returns only the
authenticated user's total and model rows, final limits and per-field sources.
A supplied user_id cannot change that identity. Model rows include `configured`,
`available`, optional `usage`, and `sources`. Removed rules remain visible while
activity survives. `models_complete=false` signals an incomplete directory.

`GET /api/user/resources?user_id=&model=&p=1&page_size=20` requires AdminAuth and
PAT `user:read`. It returns sparse `items`, `total`, `complete`, mode and timestamp;
a selected user additionally gets `user_total`. Page size is bounded to 100.
The admin user picker searches by username/ID. The self/admin tables filter exact
models, paginate, poll every 5 seconds only while visible, and hide stale usage
on failed refreshes. The self overview displays effective limits and usage; rule
sources appear only in the admin overview. A successful response with unavailable
usage retains limits and shows Unknown; zero is never substituted for missing usage.
The self model rows share the total-resource utilization meter: green below 40%,
yellow from 40%, and red from 80%. Unlimited or unavailable usage has no percentage
or progress bar.

Both APIs have a 5-second budget. Discovery uses 30-minute sorted-set activity
indexes, recent local events and explicit overrides, never Redis KEYS or an
all-user × all-model scan. Self queries also include configured model names so an
inactive user's limits are visible. Inventory discovery is capped at 10,000 rows,
with `complete=false` on failure/truncation. Filtering applies to this bounded
inventory; beyond the cap, a model filter may also be incomplete. Selecting a user narrows the Redis activity index. Local memory/event
scans also have a 10,000-entry work budget and can report incomplete discovery. Usage reads are atomic per batch of at most 100 rows, not a
transaction across the whole inventory. Reads prune expiry without extending TTLs.

Total-user Gauges retain their `scope,user_id` labels. Model Gauges add `model`;
limits also add `resource=concurrency|rpm|input_tpm`. Scope is memory or redis.

| Total metric | Model metric | Meaning |
| --- | --- | --- |
| `new_api_user_inflight_requests` | `new_api_user_model_inflight_requests` | Live recorded concurrency |
| `new_api_user_window_requests` | `new_api_user_model_window_requests` | Rolling 60-second admissions |
| `new_api_user_window_input_tokens` | `new_api_user_model_window_input_tokens` | Rolling estimated input tokens |
| `new_api_user_limit` | `new_api_user_model_limit` | Effective allowance, only meaningful when enabled |
| `new_api_user_limit_enabled` | `new_api_user_model_limit_enabled` | 0/1, distinguishes unlimited and blocked |
| `new_api_user_usage_available` | `new_api_user_model_usage_available` | Read success; failed numeric usage is omitted |
| `new_api_user_admitted_requests_total` | `new_api_user_model_admitted_requests_total` | Process-local final joint admissions |
| `new_api_user_rejected_requests_total` | `new_api_user_model_rejected_requests_total` | Process-local final joint rejections by reason |

Counter labels are user_id, optional model and rejection reason, with target labels
identifying each process. Rejections use the final winning total/model reason or
`store_unavailable`; no per-attempt or per-layer double counting. Model counters
reset on restart or after 24 hours without an event, including after removal;
reappearance starts at zero. Gauges omit deleted users. Wallet metrics own
`new_api_user_info{scope,user_id,username}` as described below.

Each model combination has 10 Gauge series and 8 Counter series per target.
The Gauge directory is capped at 10,000 total/model combinations per scrape.
Local model Counter emission has a separate 10,000-combination cap; exceeding it
omits all model Counters for that scrape, rather than reporting a changing arbitrary
subset. Expired Counter entries are evicted from memory. Existing total-user
Counters retain their process-lifetime lifecycle. A single configured model used
by many users can still have high cardinality; size Prometheus accordingly.

`new_api_user_resource_scrape_success{scope,component}` reports config, directory,
usage and counters completeness. Store failures omit usage samples and set known
rows' available flag to zero. Directory truncation, DB errors and budget exhaustion
must never be interpreted as zero traffic. At most 100 indexed user existence
lookups/Redis usage batches are needed for 10,000 rows; read batches make one Redis
round trip each (the first NOSCRIPT may add script loading). Admissions remain a
single Redis script, releases a single command, independently of combination count.
All monitoring uses the existing private-network-protected port 9080; no
application-level authentication has been added.

## PromQL and Grafana

See [model recording/alert rules](user-model-resources.rules.yml) and the importable
[Grafana dashboard](user-model-resources.grafana.json). Set a static Prometheus
`deployment` target label shared by replicas of one deployment; different data
stores must have different labels. Select **one healthy instance** for all Gauge
usage, limits, enabled and identity queries. Never sum shared Redis Gauges or take
independent maxima of usage and limit. Memory scope must retain instance.

The supplied dashboard pins one deployment/instance for Gauges and gates usage on
up, config/directory/usage health and per-row availability. It retains enabled-zero
and unlimited states separately. It shows config disagreement using min/max across
healthy Redis targets; during propagation, investigate disagreement before using
occupancy for capacity decisions. Pinned healthy nodes can still have a stale
configuration during polling. Gauge sample times are not simultaneous. For an
automatic source choose one `topk by(deployment)(1, up and ...healthy...)` marker
and apply the **same marker** to every Gauge operand; never select operands
independently. Stable pinned sources avoid selection churn.

For model admissions/rejections, calculate rate on each process first, then sum:

```promql
sum by (deployment,user_id,model) (
  rate(new_api_user_model_admitted_requests_total[5m])
)
sum by (deployment,user_id,model,reason) (
  rate(new_api_user_model_rejected_requests_total[5m])
)
```

Use the total-user counterparts for total demand. Counter gaps/resets after idle
or process restart mean sampled observations, not a durable audit ledger. Avoid
using a window that straddles missing Counter scrapes as a complete daily count.

The rules retain instance for occupancy and use limits from the same evaluation.
Sampled daily peaks use `max_over_time(new_api:user_model_concurrency_ratio[1d])`.
Approximate seconds above 80% use
`sum_over_time((new_api:user_model_concurrency_ratio >= bool 0.8)[1d:30s]) * 30`.
Also report `count_over_time(new_api:user_model_concurrency_ratio[1d])`; missing
samples are missing coverage. Enabled zero is a blocked policy, not an infinite
ratio. Neither unlimited nor unknown usage should be graphed as 0% occupancy.
Configure notifications outside the gateway and use No Data/Error states.

## Wallet and accumulated usage snapshots

Every gateway queries all non-deleted users immediately after its monitoring
listener is bound, then every 60 seconds. Queries run serially with a five-second
context deadline. One query reads only ID, username, quota and used_quota, including
disabled, zero-balance and inactive users. Scrapes read an immutable memory
snapshot, never trigger this query, and never refresh user Redis balances.
No billing, settlement, log or schema changes are involved. Batch writes can delay
what is visible in the database; the snapshot is not an accounting ledger.

| Metric | Labels | Meaning |
| --- | --- | --- |
| `new_api_user_wallet_balance_quota` | `user_id` | Wallet balance, Gauge |
| `new_api_user_used_quota` | `user_id` | Accumulated wallet **and subscription** usage, Gauge |
| `new_api_user_info` | `scope,user_id,username` | All non-deleted users, value 1 |
| `new_api_wallet_snapshot_success` | none | Latest attempt succeeded: 1; otherwise 0 |
| `new_api_wallet_snapshot_timestamp_seconds` | none | Last successful completion, Unix seconds; initially 0 |

The wallet collector is the sole producer of `new_api_user_info`. Its historical
`scope=memory|redis` label describes resource storage, not wallet ownership.
Username changes are visible on the next successful snapshot without breaking
amount series. Deleted users disappear at that point; historical samples remain
in Prometheus. A successful empty result removes all user samples.

Failed/expired queries discard even partially read rows, preserve the previous
complete rows and success time, and publish success=0. Before first success only
the two health metrics exist. Scrapes return one state for amounts, names and
health. Errors are logged; no alert or notification is installed in the app.
Port 9080 still relies on private-network/firewall isolation, not application
authentication; do not expose wallet metrics publicly.

### Units and windows

All amounts use internal quota. Divide by the deployment's actual `QuotaPerUnit`
for USD (default 500000); set a Grafana constant variable `quota_per_unit` to that
value. Display currency conversions are separate and may change over time. Days
and relative speed ratios do not need currency conversion. Prometheus uses
float64; extremely large accumulated integer values above 2^53 lose unit precision.

`used_quota` can decrease on refunds or administrative adjustment. **Never use
rate/increase on it.** The supplied rules use current value minus `offset 5m`,
`offset 15m` or `offset 7d`, not `delta` (which extrapolates). Offsets select the
most recent sample at or before the historical evaluation time, not an exact
transaction timestamp. Sixty-second snapshots and scrape timing make these
minute-level observations; refunds inside a window can offset spending.

Load the wallet group in `concurrency.rules.yml`. Rules retain target identity;
normal values require `up=1`, latest query success, and snapshot age in [0,180)
seconds. Both endpoints must be healthy for a window difference. A failure
immediately suppresses predictions even if the retained snapshot is still young.
A middle-of-window outage does not invalidate healthy endpoints: cumulative
endpoint differences still represent net usage. Never fill missing intervals with
zero. Recording rules need their own seven-day history before 7d results appear;
retain Prometheus data for more than seven days. No pre-monitoring history is
backfilled. Keep the supplied 30s rule interval and use a scrape interval of 30s.

### Grafana panels

Use one Prometheus data source. Define single-select `deployment` and `instance`
variables from `up{job="new-api"}`; disable All and multi-value for both. Define a
multi-select `user_id` variable from
`new_api_user_info{job="new-api",deployment="$deployment",instance="$instance"}`,
with All value `.*`. The **same** target selector must be used in every panel and
alert. Queries below abbreviate it as `S`; replace `{S}` with:

```promql
{job="new-api",deployment="$deployment",instance="$instance",user_id=~"$user_id"}
```

| Panel | Query (replace `{S}`) | Configuration |
| --- | --- | --- |
| Wallet list A | `new_api:user_info:wallet_valid{S}` | Instant; format Table; retain user_id and username |
| Wallet list B | `new_api:user_wallet_balance_quota:valid{S} / $quota_per_unit` | Instant; USD, 4 decimals |
| Wallet list C | `new_api:user_net_quota:daily7d{S} / $quota_per_unit` | Instant; USD/day |
| Wallet list D | `new_api:user_wallet:days7d{S}` | Instant; days, 1 decimal |
| Balance history | `new_api:user_wallet_balance_quota:valid{S} / $quota_per_unit` | Time series; legend user_id; connect null values Never |
| Net usage 5m / 15m | `new_api:user_net_quota:5m{S} / $quota_per_unit` and `new_api:user_net_quota:15m{S} / $quota_per_unit` | Signed USD; retain negative values |
| Hourly speed comparison | `new_api:user_net_quota:5m{S} * 12 / $quota_per_unit` and `new_api:user_net_quota:daily7d{S} / 24 / $quota_per_unit` | USD/hour; distinguish recent and baseline |
| Short-term runway | `new_api:user_wallet:seconds15m{S}` | Duration seconds; separate from 7d prediction |

For the wallet list, apply **Outer join by user_id**, then Rename by regex and
Filter fields by name to show username, user_id, balance, daily usage and days.
These transforms also handle empty frames; Grafana 12.3 Organize fields can fail
when an empty join leaves multiple frames. Set the user ID unit to None and apply
currency/day units only to the corresponding columns. Sort by any numeric column
(default days ascending). A missing projection must not drop the user's row.
If fixed table columns are needed even without history, use a table-only NaN
placeholder (`forecast or (raw_balance * NaN)`) and map special NaN to an explicit
unavailable state. This is not a numeric fallback; never use it in alert rules.
For a table that retains users during collection failure, use raw info for the
row identity and `valid_balance or (raw_balance * NaN)` for its amount, mapping
NaN to Collection unavailable. Do not present the retained raw balance as fresh. For diagnosis during failure, add a separate raw
balance table with the same target filter, prominently labeled **Last snapshot —
may be stale**; do not feed raw values to forecasts.

Add health Stat panels with the target filter (without user_id): raw success,
`time() - new_api_wallet_snapshot_timestamp_seconds`, and timestamp multiplied by
1000 with Grafana unit Date & time. Map success 0 to “Query failed / not initialized”.
Use `up` to distinguish endpoint failure. Preserve null as “Unavailable”, not zero.
Use these state queries alongside the list; the order below is the display priority:

1. Current `new_api:wallet_snapshot:healthy` absent: “Collection unavailable”.
   Raw success=0 means latest query failed/not initialized; age >=180 means stale;
   up=0 or absent means target unavailable. Check these independently.
2. `new_api:user_wallet_balance_quota:valid{S} <= 0`: “Balance exhausted”.
3. Healthy balance `unless` `new_api_user_used_quota{S} offset 7d`: “Missing 7d
   endpoint — insufficient history or historical data missing”. Snapshot metrics
   alone cannot always distinguish a new account from historical data loss.
4. Healthy balance with a historical user sample, but no healthy target at
   `offset 7d`: “Historical snapshot unavailable”.
5. `new_api:user_net_quota:7d{S} <= 0`: “No valid consumption speed”; negative
   differences additionally mean net decrease, not a proven top-up.

A wallet balance divided by seven-day daily **total net** spending includes
subscription usage. It is an accepted conservative estimate, not a wallet-only
burn rate or a promised exhaustion date. Manual resets can distort positive
windows too; snapshots cannot automatically detect every reset/refund.

### Alert examples (operator configured)

These are examples only; configure notification routing and thresholds yourself.
Use a fixed target for alert rules, not Grafana template variables. For example,
replace `{S}` with `{job="new-api",deployment="gateway-production",instance="new-api-1:9080"}`.
Quota thresholds below assume the default 500000 quota/USD; adjust for deployment.

```promql
# More than USD 10 net usage in five minutes; works without seven-day history.
new_api:user_net_quota:5m{S} > 5000000

# >5x seven-day average AND >USD 1/5m. Suggested for: 3m.
(new_api:user_net_quota:5m{S} / 300
  > 5 * ((new_api:user_net_quota:7d{S} > 0) / 604800))
and (new_api:user_net_quota:5m{S} > 500000)

# Positive wallet would last less than one hour at the last 15m net speed.
new_api:user_wallet:seconds15m{S} < 3600

# Already exhausted, independent of consumption speed.
new_api:user_wallet_balance_quota:valid{S} <= 0
```

For collection alerts, use the same fixed target selector without user_id:
`new_api_wallet_snapshot_success == 0` with `for: 3m`,
`time() - new_api_wallet_snapshot_timestamp_seconds >= 180`, and `up == 0`.
Also cover a missing expected target/metric with `absent_over_time(up{...}[3m])`
and `absent_over_time(new_api_wallet_snapshot_success{...}[3m])` using concrete
job/instance/deployment matchers. Missing data is not zero consumption. Grafana
alerts should route No Data/Error as monitoring failures, not “Normal”.

### Multiple replicas and resource correlation

Wallet snapshots represent the **same database**, unlike process-local relay and
admission Counters. Never sum wallet balances/usage across replicas, and do not
independently take max(balance) and max(used_quota). Query times, scrape times and
failed snapshots differ. Default examples intentionally pin one target.

An optional automatic source selection can use this expression as a recording
rule named `new_api:wallet_source:selected` (one job and one database per deployment):

```promql
topk by (deployment) (
  1,
  new_api_wallet_snapshot_timestamp_seconds{job="new-api"}
  and on(job, instance, deployment) (new_api:wallet_snapshot:healthy == 1)
)
```

Apply the **same** marker to each valid balance, info and net-usage result using
`and on(job, instance, deployment) new_api:wallet_source:selected`. Calculate the
endpoint difference **before** source selection, retaining instance. If the
chosen instance lacks history, omit its forecast; never borrow another instance's
old endpoint. Tied timestamps may select either target; pinning is more stable.
Separate databases require separate deployment labels. Downstream HA Prometheus
replicas still require normal backend deduplication.

Compare consumption with `new_api_user_window_requests` (rolling RPM),
`new_api_user_window_input_tokens` (estimated input TPM) and
`new_api_user_inflight_requests` for the same user. Redis scope is shared; memory
scope is process-local. Reuse the resource queries above for their aggregation.
Local admission/relay Counters can use `sum(rate(...))`; relay metrics have no
user label. Usage plus RPM growth suggests request growth; usage plus input TPM
suggests larger inputs; otherwise inspect model prices/output/logs as available.
These are diagnostic correlations, not automatic cause classification. Logs can
be disabled without affecting wallet monitoring.
