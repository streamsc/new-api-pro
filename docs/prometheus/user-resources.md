# User resource limits

## Scope and semantics

One user ID shares concurrency, estimated input TPM and RPM across API keys,
models and gateway replicas. Admission is atomic across these three resources;
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
The visual editor manages default limits and optional per-user overrides. Switch
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
`{"config":{...},"reason":"capacity review"}`. Existing operation audits record
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
- No limits at that layer: failed statistics writes are logged but admitted.
  The other layer still independently enforces its limits.
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

## Metrics and lifecycle

User gauges have `scope,user_id`, where scope is `memory` or `redis`.
Limits add `resource=concurrency|input_tpm|rpm`; identity adds `username`.

| Metric | Type | Meaning |
| --- | --- | --- |
| `new_api_user_inflight_requests` | Gauge | Recorded live user slots |
| `new_api_user_window_requests` | Gauge | Admissions in last 60 seconds |
| `new_api_user_window_input_tokens` | Gauge | Estimated input admitted in last 60 seconds |
| `new_api_user_limit{resource}` | Gauge | Limit, interpreted only when enabled |
| `new_api_user_limit_enabled{resource}` | Gauge | 0 or 1; enabled zero is distinguishable |
| `new_api_user_usage_available` | Gauge | Read succeeded; failed usage samples omitted |
| `new_api_user_info{username}` | Gauge | Current database username mapping, value 1 |
| `new_api_user_admitted_requests_total` | Counter | Local process admissions |
| `new_api_user_rejected_requests_total{reason}` | Counter | Local rejections by finite reason |

Counters have `user_id` and rejection `reason`; Prometheus target labels
identify replicas. Store failures use `store_unavailable`, not a quota reason.
Counters reset on process restart. No API-key, request-ID or model metric labels.
Series are bounded by users seen by a process since restart; shared Redis's active
user index additionally discovers users seen by other replicas for 30 minutes.
Deleted users lose gauge/info samples; historical event counters remain until
restart. Renames change only the info label, not usage/counter identity. Expect
roughly 12 gauge series plus up to 5 counter series per observed user per replica.
This is intended for moderate user populations, not unbounded multi-million-user
scrapes. Scrapes use the existing five-second budget, indexed user lookups in
batches of 100, and per-user Redis calls; timed-out/unavailable samples are absent.
No full user-table or Redis keyspace scan is performed. The active index is not an
in-flight recovery system and never gates admission.

## PromQL

Use target `instance` to distinguish process counters. Configure a static `deployment`
target label shared by replicas of one deployment and distinct across independent
deployments. This is a Prometheus scrape label, not a gateway environment variable.
Keep it when aggregating and drop instance labels with `max by` for Redis gauges; **never sum replica gauges**.
For memory scope, retain `instance` because counters are process-local. Examples
below concern shared Redis. Gauge reads are not simultaneous; `max` favors the
higher observed value and does not create an atomic distributed scrape.

```promql
# Current shared usage (substitute window_requests/window_input_tokens as needed)
max by (deployment,user_id) (new_api_user_inflight_requests{scope="redis"})

# Concurrency occupancy; enabled zero allowance is handled separately
max by (deployment,user_id) (new_api_user_inflight_requests{scope="redis"})
/ on (deployment,user_id)
(max by (deployment,user_id) (new_api_user_limit{scope="redis",resource="concurrency"}) > 0)
and on (deployment,user_id)
(max by (deployment,user_id) (new_api_user_limit_enabled{scope="redis",resource="concurrency"}) == 1)

# Admission and excess counts, sum replica-local events after rate/increase
sum by (deployment,user_id) (increase(new_api_user_admitted_requests_total[1h]))
sum by (deployment,user_id,reason) (
  increase(new_api_user_rejected_requests_total{reason=~"user_.*_exceeded"}[1h])
)

# Excess rejection ratio, excluding store/authentication failures; zero denominator stays NaN
sum by (deployment,user_id) (rate(new_api_user_rejected_requests_total{reason=~"user_.*_exceeded"}[5m]))
/
(
 sum by (deployment,user_id) (rate(new_api_user_admitted_requests_total[5m]))
 + sum by (deployment,user_id) (rate(new_api_user_rejected_requests_total{reason=~"user_.*_exceeded"}[5m]))
)

# Sampled daily peak
max_over_time((max by (deployment,user_id) (new_api_user_inflight_requests{scope="redis"}))[1d:30s])

# Current username association
max by (deployment,user_id) (new_api_user_inflight_requests{scope="redis"})
* on (deployment,user_id) group_left(username)
max by (deployment,user_id,username) (new_api_user_info{scope="redis"})
```

Record the occupancy expression above as `gateway:user_concurrency_ratio`, then:

```promql
# Approximate observed high-occupancy seconds, not an exact accumulated duration
sum_over_time((gateway:user_concurrency_ratio >= bool 0.80)[1d:30s]) * 30
sum_over_time((gateway:user_concurrency_ratio >= bool 0.95)[1d:30s]) * 30
```

Sampling misses bursts; outages/staleness reduce coverage. Report coverage with
`count_over_time(gateway:user_concurrency_ratio[1d])`, preserve historical limit
samples, and never divide a historical usage series by today's config. No special
high-occupancy cumulative-seconds metric, daily report, ranking or dashboard is
introduced. During a rename, stale target samples can briefly provide two info
mappings; use current healthy targets and account for the scrape staleness window.

Validation evidence and remaining limits are recorded in `validation.md`.
