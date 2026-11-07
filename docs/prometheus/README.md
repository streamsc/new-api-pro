# Channel concurrency metrics

`GET :9080/metrics` (independent monitoring listener) exposes the current gateway concurrency reservations. It does not
measure upstream model queues, GPU activity, or traffic that bypasses the existing
channel concurrency service (including Realtime and asynchronous tasks).

The same endpoint also exposes [user resources and wallet snapshots](user-resources.md#wallet-and-accumulated-usage-snapshots).
Wallet data is refreshed in the background every 60 seconds and has its own
success/freshness checks; unlike process-local Counters, it must not be summed
across replicas. The wallet recording rules are included in `concurrency.rules.yml`.

## Metrics

The four concurrency families below are Gauges. No credentials are required.
No Go/process collectors are registered. Logical relay counters and durations
are described below; scraping this endpoint does not itself count as a relay.

| Name | Labels | Meaning |
| --- | --- | --- |
| `new_api_channel_concurrency_in_flight` | `channel_id`, `scope` | Current reservations; a successfully read empty count is 0. |
| `new_api_channel_concurrency_limit` | `channel_id`, `scope` | Positive persisted limit; absent for unlimited or invalid settings. |
| `new_api_channel_concurrency_limit_enabled` | `channel_id`, `scope` | 1 for a finite limit, 0 for unlimited; absent for invalid settings. |
| `new_api_concurrency_scrape_success` | `component`, `scope` | Complete collection of `channel_config` or `concurrency`: 1 success, 0 failure. |

`scope` is `process` without Redis and `redis` with Redis. Channel IDs are stable
decimal IDs, not names. All database channels are included, including disabled
channels that may still have active requests. A deleted channel disappears from
the next successful scrape; orphaned reservations are not separately enumerated.

The collector queries only `id, name, setting`. The setting column is read as a whole,
but only `max_concurrency` is interpreted. Other properties, raw settings and
credentials are not exposed. It never calls the repairing `Channel.GetSetting()`.
SQL NULL, blank settings, an absent property, and integer zero mean unlimited.
JSON null (root or property), non-object roots, negative/non-integer values, wrong
types, overflow and malformed JSON mean invalid configuration. Unknown properties
are ignored. No setting is repaired or saved by a scrape.

The limit is the **persisted configuration**, not a measurement of the exact value
each routing worker is using. Existing memory-cache synchronization uses
`SYNC_FREQUENCY` (normally 60 seconds). Configuration propagation can lag, and a
limit reduction does not revoke existing reservations. Occupancy may exceed 100%.
Database configuration and Redis counts are not an atomic cross-store snapshot.

## Failure behavior and resource limits

Each request builds a fresh snapshot. Failed reads never replay old values or
substitute zero. Database failure omits all channel samples and emits both success
values as 0. An empty successfully read directory emits both as 1 without Redis I/O.
A bad setting omits only that channel's limit families and makes `channel_config`
0; other valid settings and all successfully read counts remain usable. Redis
failure, missing batch entries or invalid counts omit the entire count batch;
valid configuration remains available and `concurrency` is 0.

These dependency failures return HTTP 200: Prometheus `up=1` means the endpoint
was scraped, not that all components succeeded. Check both component Gauges.
Do not gate every channel on `channel_config == 1`: that would hide valid peers
when one channel has damaged configuration.

Two requests per application instance may collect/output concurrently; excess
requests receive 503 without queuing. Collection has a 5-second request-derived
budget, with at most 2 seconds for the database and 3 for Redis, always bounded by
the earlier parent deadline. Cancellation prevents subsequent stages; in-progress
driver I/O exits within its deadline, not necessarily immediately on disconnect.
The shared Redis client is never closed to cancel a scrape. Existing Redis
expired-lease pruning and TTL maintenance remain in place.

Only `/metrics` gets an 8-second write deadline measured from request entry. The
deadline is cleared after output, including flushing the HTTP buffer; global SSE
write timeouts are unchanged. A writer that cannot support a deadline gets 500
without collecting. Encoding uses the official handler directly. Errors detected
before committing the response can return non-2xx. After committing, a write error
cannot change the status; it is logged and output terminates. Not every truncated
response is guaranteed to be recognized by the scraper.

## Deployment

Use `prometheus.example.yml` and `concurrency.rules.yml` together. Scrape every
application instance directly every 30 seconds with a 10-second scrape timeout;
do not use a load-balanced target. The tested scale is documented in the validation
report, not enforced as a channel-count limit.

Set target labels consistently:

- `deployment`: one business channel directory. Independent deployments differ.
- `concurrency_domain`: one logical Redis database and concurrency-key namespace;
  use the same opaque label across replicas and Sentinel failover, without server
  addresses or credentials. Different domains differ. Use `local` without Redis.
- `instance`: retain Prometheus's per-target identity.

These are target labels, not application configuration. The application cannot
validate them. Sharing a Redis count domain while using unrelated channel
directories is a deployment error. HA Prometheus servers also require normal
replica deduplication in any shared downstream metrics backend.

The monitoring listener defaults to port **9080**, configurable with
`METRICS_PORT`. The business listener continues to use `PORT` (default 3000).
The business port returns 404 for `/metrics`, `/metrics?x=1`, and `/metrics/`,
including embedded-frontend and `FRONTEND_BASE_URL` modes. There is no fallback
or redirect to the monitoring port. The monitoring port serves only `GET /metrics`;
API, frontend, pprof and other paths are not registered there.

Only a private network should reach port 9080. Containers on the same private
Docker network can scrape `new-api:9080` without publishing that port to the host.
Do not add a public `9080:9080` mapping or proxy monitoring through the business
entry point. For a host-based installation, restrict 9080 with the host firewall.
The monitoring listener itself does not implement authentication.

Both listeners must bind successfully before serving. A bind failure fails startup
and closes any listener already opened. SIGINT/SIGTERM stops both listeners and
drains active requests within `SHUTDOWN_TIMEOUT_SECONDS`; expired shutdowns force
connections closed. Metrics retain their existing two-request concurrency cap,
five-second collection deadline and eight-second write deadline. The monitoring
server also applies a five-second header-read timeout and 30-second idle timeout.

When upgrading one gateway at a time, update only that gateway's scrape target
from its business port to 9080. Older gateways remain on their existing target
until upgraded. Verify business metrics paths return 404, the private monitoring
target is up, and monitoring is inaccessible through the public entry point.

## Queries

Load the recording rules first. Their `:valid` series match target identity against
`up == 1`; component failures are reported separately. Missing values remain
missing. The rules preserve other valid channels when one configuration is bad.

1. Per-instance counts and persisted limits: `new_api:channel_concurrency_in_flight:valid`
   and `new_api:channel_concurrency_limit:valid`; retain `instance` in the legend.
2. Shared Redis counts: `new_api:redis_concurrency_in_flight:deduplicated`. This is
   a maximum across replica scrape observations, not a simultaneous global read.
3. Local total: `new_api:process_concurrency_in_flight:sum`. Inspect the per-instance
   series as well; summing local counts is legitimate, summing shared counts is not.
4. Occupancy: `new_api:redis_concurrency_utilization:ratio` or
   `new_api:process_concurrency_utilization:ratio`. Multiply by 100 for percent.
   Local occupancy divides each instance by its own limit. Shared limits are never
   added across replicas. Unlimited channels have no ratio; display them using
   `new_api:channel_concurrency_limit_enabled:valid == 0`. Configuration disagreement
   suppresses the shared ratio instead of selecting a misleading denominator.
5. Endpoint failures: `up{job="new-api"} == 0`. Component failures:
   `new_api_concurrency_scrape_success{job="new-api"} == 0`.
   Available count observations per domain:

```promql
sum by (deployment, concurrency_domain, scope) (
  (new_api_concurrency_scrape_success{component="concurrency",job="new-api"} == bool 1)
  and on(job, instance, deployment, concurrency_domain) (up == 1)
)
```

Combine this with `sum by(deployment, concurrency_domain) (up{job="new-api"} == bool 1)`
and the total target count `count by(deployment, concurrency_domain) (up{job="new-api"})`.
One failed replica can leave a usable shared observation; all failed replicas must
not become zero occupancy. Disappearing service-discovery targets require the
deployment's expected-target monitoring; the exporter cannot enumerate them.

6. Configuration disagreement: `new_api:redis_concurrency_config:inconsistent`.
   It compares finite/unlimited flags as well as differing finite limits. A missing
   configuration is represented by the component failure, not a fabricated limit.

Prometheus staleness markers, lookback, recording-rule evaluation intervals and
historical graph windows affect when old observations disappear. A successful
scrape omitting a deleted/invalid channel is different from a failed scrape. No
query here uses `or vector(0)` to turn absence into zero. Fixed rule tests cover
deduplication, domain isolation, partial failure and configuration disagreement.

## User resources (Issue #8)

The same endpoint now also exports user resource gauges and per-process admission
counters. See [user resource limits](user-resources.md) for scope, configuration,
failure behavior, lifecycle, and PromQL. Existing channel series retain their
labels: set a stable deployment label in the scrape target configuration and do
not combine distinct deployments. Independent deployments use separate Redis
databases or instances; no deployment-ID variable is required. Channel Redis key
names are unchanged. Ordinary request slots expire after 30 minutes without
renewal. Redis failure or expired slots no longer cancel admitted requests.

## Logical HTTP relay metrics (Issue #6)

| Name | Type | Labels |
| --- | --- | --- |
| `new_api_channel_info` | Gauge, always 1 | `channel_id`, `channel_name` |
| `new_api_channel_concurrency_rejected_total` | Counter | `model`, `channel_id` |
| `new_api_relay_requests_total` | Counter | `model`, `result` |
| `new_api_relay_request_duration_seconds` | Histogram | `model`, `result` |

Histogram boundaries in seconds are **0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1,
2.5, 5, 10, 30, 60, 120, 300, 600, 1800**, plus the implicit `+Inf` bucket.
The exported histogram includes `_bucket{le}`, `_sum`, and `_count`. These buckets
cover short synchronous calls and generation streams up to 30 minutes; requests
can last longer, but quantiles in the overflow bucket are not precise.

The boundary starts at entry to `controller.Relay` and ends in its outer defer,
after its cleanup/error-response defers. It includes validation within Relay,
user admission, preparation, internal routing, all serial retries and cleanup.
It excludes middleware authentication, initial middleware channel selection and
network delivery after Relay returns. Each entry records one result and one
elapsed observation, including early returns, cancellation, panic and unknown.
An HTTP 200 does not establish stream success. Existing performance-page health
sampling is independent and unchanged.

Covered paths that actually reach this function: standard HTTP OpenAI
completions/chat, Responses and compact Responses; Claude messages; Gemini
models/generate/stream/embed and engines embeddings; embeddings, rerank, audio,
moderations, alpha search, tools search/fetch; legacy edits and image
generations/edits when the host protocol dispatcher falls back to ordinary Relay;
and playground chat. Task-plugin dispatch that intercepts these paths is not
included. Realtime, Responses WebSocket, asynchronous tasks, channel test probes,
count_tokens, `/metrics`, and all rejections before Relay are excluded. Thus the
denominator is **finished supported Relay entries**, not all gateway HTTP traffic.
An in-progress stream has not yet contributed to either request metric.

`model` is frozen at Relay entry, before upstream model mapping. The selected
channel's configured model list acts as the allowlist; both the client model and
configured names use the existing `RoutingMatchModelName` normalization (reasoning
modifiers/legacy aliases and existing wildcard families). Only an exact match of
these normalized names is emitted. Missing channel/model recognition, including
arbitrary models on a pinned channel, becomes the fixed label `unknown`.
Administrator-configured model/channel churn can still grow series over a process
lifetime; client modifiers, error messages and arbitrary request models cannot.
No new model lookup or database/cache refresh is performed for each request.

### Result classification and limits

The fixed results are `success`, `failure`, `cancelled`, `unknown`. Classification
uses existing Relay errors and `StreamStatus`; no extra protocol parser is added.

- `success`: synchronous handler returns without a Relay error or known business
  rejection; stream has a completed outcome with normal EOF/DONE and no errors,
  or existing scanner DONE with no contrary facts; recognized `max_tokens` /
  `max_output_tokens` incomplete outcomes also count as normal bounded generation.
- `failure`: panic, Relay error, known protocol failure, business rejection,
  stream errors, timeout/scanner failure, safety/content-policy incomplete outcome,
  or EOF missing a terminal event when the adapter explicitly requires one.
- `cancelled`: the **original incoming request context** is `context.Canceled`
  before reliable completion, unless an explicit protocol failure or business rejection is already
  known. A completed stream or a synchronous handler that returns normally wins over
  a late cancellation (a client may close after reading its full response while
  Relay cleanup still runs). Synchronous cancellation relies on the handler
  returning a Relay error; adapters that swallow write errors cannot provide
  that distinction. The channel lease's
  child context is never used; releasing a slot cannot create this result.
- `unknown`: no sufficient completion/failure fact: for example legacy stream EOF
  without a terminal contract, missing stream status, handler stop, unconfirmed
  disconnect, upstream-reported cancellation alone, or an unfamiliar incomplete
  reason. An internal `context.Canceled` returned as a Relay error is failure,
  not client cancellation. Incoming deadline expiry is failure, not cancellation.

Current OpenAI chat/Responses, Claude and Gemini adapters have richer existing
terminal state than some other providers. HTTP DONE reflects the gateway's
existing transport convention, not a new guarantee of valid content, full usage
or successful client delivery. Synchronous success likewise trusts the handler's
existing validation. `PerformanceBusinessRejection` is a failure fact here; the
performance module's broader `ignored` category is never translated to cancelled.
No claim is made that all adapters have identical semantic precision.

### Rejections and names

A rejection increments only when the ordinary Relay slot acquisition returns
`ErrChannelConcurrencyLimit`, before calling the upstream handler. Each rejected
acquisition increments once. Redis errors, user limits and upstream 429 responses
do not increment it. It is an **event count**, not upstream attempts or final
failed requests. A later successful request/attempt cannot erase it. At this
baseline a rejected slot stops the Relay (existing skip-retry behavior); this
feature does not add a retry after rejection.

Names come from the same fresh database projection as concurrency configuration.
Empty/missing names emit no info mapping. Configuration read failure emits no
mapping; a Redis count failure still permits the independently read name. A
successful rename replaces the mapping on the next scrape, without touching any
business counter. Names are current display metadata, not historical identity.
Same-name channels remain distinct by ID. Names are administrator configuration
and should not contain secrets.

### PromQL: process totals, results and latency

Use the `deployment` and `concurrency_domain` target labels in
`prometheus.example.yml` on **every** target. A deployment identifies one gateway
installation; a concurrency domain identifies one shared Redis database/instance
(or the documented local domain). Do not reuse them for unrelated installations.
Counters and histograms are process-local even with Redis: scrape every instance
directly, never through a load balancer. Apply `rate`/`increase` **before** `sum`
so individual process resets are handled correctly. New label combinations appear
on their first event; rates require enough samples. A scrape outage can lose
unobserved increments, especially across restart. These are not durable audit data.

Per-model result distribution (requests/second, all four results retained):

```promql
sum by(deployment, concurrency_domain, model, result) (
  rate(new_api_relay_requests_total[5m])
)
```

All four result proportions, each divided by **all four outcomes for that model**:

```promql
new_api:relay_requests:rate5m
/ on(deployment, concurrency_domain, model) group_left
sum by(deployment, concurrency_domain, model) (new_api:relay_requests:rate5m)
```

Filter the numerator with `{result="success"}`, `failure`, `cancelled` or `unknown`
for individual panels. The denominator must remain unfiltered. No traffic gives
an undefined ratio; do not label it 100% success. Unknown is visible uncertainty,
not success and not silently removed. Any optional success/(success+failure)
panel must explicitly be labelled as excluding cancelled and unknown.

P95 in seconds for each model/result (substitute `0.50` / `0.99` for P50 / P99):

```promql
histogram_quantile(0.95,
  sum by(deployment, concurrency_domain, model, result, le) (
    rate(new_api_relay_request_duration_seconds_bucket[5m])
  )
)
```

To include **all** outcomes in one latency distribution, omit `result` from the
aggregation; to show successful latency only, filter `{result="success"}` first.
Do not average per-instance quantiles. Cancelled and unknown have real duration
samples and are not omitted from the all-outcome distribution.

Per-model/channel rejection events per second and events over an hour:

```promql
sum by(deployment, concurrency_domain, model, channel_id) (
  rate(new_api_channel_concurrency_rejected_total[5m])
)
sum by(deployment, concurrency_domain, model, channel_id) (
  increase(new_api_channel_concurrency_rejected_total[1h])
)
```

Here “rate” means events/second, not a fraction of channel attempts. An attempt
count denominator is deliberately outside this phase; dividing by logical
requests would mix different populations.

### PromQL: unique names and rename propagation

`concurrency.rules.yml` first deduplicates names by deployment/domain/ID/name,
filters targets with `up != 1`, then excludes keys with more than one current
name. `new_api:channel_info:unique` therefore has at most one RHS series per
join key, even during replica disagreement or rename propagation. The conflicting
keys remain visible in `new_api:channel_info:conflicting`.

Use an inner join when only reliably named rows are desired:

```promql
new_api:channel_concurrency_rejected:rate5m
* on(deployment, concurrency_domain, channel_id) group_left(channel_name)
new_api:channel_info:unique
```

To retain ID-only rows while the mapping is missing/ambiguous, use a left-join
pattern (recommended for alerts; always include `channel_id` in display):

```promql
(new_api:channel_concurrency_rejected:rate5m
 * on(deployment, concurrency_domain, channel_id) group_left(channel_name)
 new_api:channel_info:unique)
or on(deployment, concurrency_domain, model, channel_id)
new_api:channel_concurrency_rejected:rate5m
```

The same unique RHS can label `new_api:redis_concurrency_in_flight:deduplicated`
and existing deduplicated limits. For local process gauges retain the instance
until summing occupancy; never sum Redis-shared occupancy/limits across replicas.
Do not join raw multi-replica info, join only by `channel_id`, or take `max` over
names without detecting conflicts. During a rename the old name may remain until
all targets are scraped; conflicting names are suppressed until agreement. No
rule can detect a stale name when the only healthy target still reports it;
the mapping is observational, not a cross-replica atomic configuration snapshot.

The existing private-network `/metrics` isolation, 30-minute slot expiry,
Redis outage/data-loss boundaries, collection status and user-resource metrics
remain unchanged. No lease renewal/cancellation, persistence, log scanning,
actual usage adjustment, attempt metric or first-content timing is introduced.
