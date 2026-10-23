# Channel concurrency metrics

`GET /metrics` exposes the current gateway concurrency reservations. It does not
measure upstream model queues, GPU activity, or traffic that bypasses the existing
channel concurrency service (including Realtime and asynchronous tasks).

## Metrics

All four families are Gauges. No credentials are required. No Go/process collectors
or request instrumentation are registered by this endpoint.

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

The collector queries only `id, setting`. The setting column is read as a whole,
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

Only a private network should reach the application's backend port. The supplied
project Compose file publishes `3000:3000`; blocking an HTTP proxy path alone does
not prevent bypass through that port. For containers sharing a private Docker
network, omit the application's host port publication and use container DNS from
Prometheus. If a host-local reverse proxy needs it, bind to `127.0.0.1:3000:3000`
and arrange a separate private route for scraping. Do not expose metrics through
the normal business proxy. Example Nginx rules:

```nginx
location = /metrics { return 404; }
location ^~ /metrics/ { return 404; }
location / { proxy_pass http://private_new_api; }
```

Nginx matches locations without query parameters, so `/metrics?x=1` is blocked.
Verify private `/metrics` returns exposition text, and public `/metrics`,
`/metrics?x=1`, `/metrics/` and direct backend-port access are blocked. Test both
the embedded frontend and `FRONTEND_BASE_URL` mode. This document does not change
or deploy existing production configuration.

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
