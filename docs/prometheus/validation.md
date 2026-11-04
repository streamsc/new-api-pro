# Issue #5 validation record

Date: 2026-09-07. Source base: `0f11c372a` plus the local Issue #5 changes.
Sentinel follow-up: 2026-09-08, same application build, with an added integration test.
Runtime: Go 1.26.1, macOS arm64; Linux arm64 cross-build also succeeded.
No production deployment or release was performed. Acceptance delivery was prepared
on 2026-09-08; the linked pull request and Issue #5 track merge and closure status.

## Automated verification

- Root `go vet ./...`, `go build` and `make test`: passed. The latter includes the existing Relay, routing, billing and concurrency suites and independent relaykit tests.
- Independent relaykit `GOWORK=off go vet ./...` and `go build ./...`: passed.
- Targeted `go test -race` for metrics collector, service, controller and model behavior: passed.
- Configuration projection: SQLite, MySQL 8.4 and PostgreSQL 16 passed. MySQL/PostgreSQL tests held a real table lock to verify driver deadline cancellation, followed by successful reads/updates/deletes. Tests require empty disposable databases.
- Dedicated Redis 7 integration: shared observations from separate clients, CLIENT PAUSE read timeout, recovery, active lease preservation and release: passed. No existing Redis data was used.
- Real TCP HTTP tests: successful exposition, keep-alive reuse, two-request concurrency cap, cancellation propagation, unsupported write deadlines, dependency-failure HTTP 200, and a slow reader triggering a write deadline and releasing its slot: passed.
- Prometheus 3.2.1 `promtool check config` and `test rules`: passed. Fixed inputs cover shared deduplication, local aggregation, unlimited channels, damaged peer configuration, differing limits, independent domains and partial/all target failures.
- A live Redis-backed scrape of 1000 configured channels emitted 3010 exposition lines, returned HTTP 200 in 23.495 ms, had both component success values equal to 1, and passed `promtool check metrics`.

## Routing and isolation

- Actual application instances: both embedded frontend and slave-node `FRONTEND_BASE_URL` mode returned HTTP 200 exposition at /metrics. The latter still redirected its root URL to the external frontend.
- A Linux application container on a dedicated internal Docker network was reachable by a scraper on that network and had no published backend port.
- A separate Nginx business proxy returned 404 for /metrics, /metrics?x=1 and /metrics/, while /api/status returned 200.
- This validates the sample local topology. No production/public deployment was inspected or changed.

## Relay load comparison

The same instrumented application build was compared with no scrapes versus a scrape
at start and every 30 seconds, for 65 seconds per run. Each instance saw the same
1000-channel directory in a shared temporary SQLite database. These load runs used
process-local concurrency; Redis correctness and one 1000-channel scrape were tested
separately. The workload was fixed at 40 requests/second total, round-robin across
instances: 80% synchronous responses delayed 100 ms and 20% SSE responses containing
five output events 100 ms apart, with actual usage and a final DONE event. Every
request traversed the actual authenticated Relay, channel selection, reservation and
billing path to the deterministic local upstream. Startup warmup was excluded from
the measured windows. In the five-instance sequence the every-fifth streaming
request lands on the same instance; both comparison runs use that same distribution.

CPU is the sum of application-process CPU time deltas. RSS is the peak sampled
aggregate RSS (every five seconds), not total system memory or isolated allocations.

| Instances | Scrapes | Successful requests | Failed requests | RPS | P95 ms | CPU seconds | Peak RSS KiB | Successful scrapes | Max scrape ms |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| 1 | none | 2600 | 0 | 40 | 514.670 | 28.04 | 90336 | 0 | 0.000 |
| 1 | 30s | 2600 | 0 | 40 | 514.874 | 28.48 | 87952 | 3 | 5.610 |
| 5 | none | 2600 | 0 | 40 | 515.316 | 28.97 | 341664 | 0 | 0.000 |
| 5 | 30s | 2600 | 0 | 40 | 515.287 | 29.69 | 354720 | 15 | 13.457 |

One earlier five-instance baseline was discarded: the host reached 96% disk usage
and the existing 95% disk guard rejected 1079 requests. Only the disposable test
database's `performance_setting.monitor_disk_threshold` was then set to 0; both
five-instance comparison runs above used that setting. Production configuration and
the guard implementation were not changed.

These are short development-machine observations, not a production capacity bound
or a statistically isolated estimate of overhead. Other validation/container work
ran on the same host. There were no request or scrape failures in the retained runs.
The workload generator and raw experiment inputs remain in
`/tmp/issue5-validation.DJo6Ct/` for this workspace session.

## Re-running verification

```sh
go test ./pkg/concurrencymetrics ./controller ./service ./model
go test -race ./pkg/concurrencymetrics ./controller ./service ./model -run 'Test(ParseConcurrencyLimit|ConcurrencySnapshot|ChannelConcurrencyConfigProjection|CollectorExposes|MetricsHTTP|MemoryChannelConcurrency|RedisChannelConcurrency|ChannelConcurrencyRead)'
```

Opt-in integration environment variables: `CONCURRENCY_TEST_MYSQL_DSN`,
`CONCURRENCY_TEST_POSTGRES_DSN` (empty disposable databases), and
`CONCURRENCY_TEST_REDIS_ADDR` (dedicated server; the test issues CLIENT PAUSE).
Run `TestChannelConcurrencyConfigProjection` and `TestChannelConcurrencyRealRedis`.
From this directory, run `promtool check config prometheus.example.yml` and
`promtool test rules concurrency.test.yml`.

## Actual Sentinel failover

Passed on 2026-09-08 using an isolated Docker network, Redis 7.4.11 Alpine, one master,
one replica and one Sentinel (quorum 1, down-after 5000 ms, failover-timeout
15000 ms). Two actual Linux arm64 application instances used the existing Sentinel
environment configuration, a disposable SQLite database, and Redis DB 13.
The database contained the previous 1000 channels plus disabled test channel 91502
with a configured limit of 10. HTTP ports were bound only to loopback.

`TestChannelConcurrencySentinelFailover` acquired a real service lease, observed its
score on the replica, then issued `SHUTDOWN NOSAVE` to the elected master and
confirmed that it stopped responding. The production `common.InitRedisClient`
created the tested failover client; a separate Sentinel client checked shared data.

- Before failure: both clients saw count 1; the lease was confirmed replicated.
- At 699 ms after shutdown: collection reported config success 1, concurrency
  success 0, preserved the limit, and omitted the count. The complete collector
  output passed comparison with the official Prometheus test parser.
- At 6.126 s: Sentinel had changed the master from `192.168.97.2:6379` to
  `192.168.97.3:6379`, and both clients saw count 1. Scope remained `redis`.
- At 30.127 s: the lease score increased on the promoted master, confirming actual
  renewal, and the lease had not been marked lost.
- A new acquisition raised the count to 2; releasing both leases produced real 0.

Concurrently, a 45-second HTTP observation fetched each application's real
`/metrics` endpoint, waiting 250 ms between completed requests. Each response was
checked for HTTP status, component values, test-channel limit/count, and total
in-flight sample count. All 266 responses returned HTTP 200; configuration remained
successful and the test-channel limit remained 10 throughout.

| Instance | Scrapes | Concurrency-failure scrapes | Maximum response ms | First observed recovered count 1, ms from observer start |
| --- | --- | --- | --- | --- |
| 19401 | 133 | 3 | 3023 | 8939 |
| 19402 | 133 | 3 | 3025 | 8942 |

Every concurrency-failure response omitted **all** in-flight samples. Successful
responses contained all 1001 count samples. Both applications observed the surviving
lease as 1 after recovery, and both ended at 0 after release. The HTTP observation
clock started before the integration test, so its timings are not failover duration.

The new test, `go vet ./service`, the service suite, and the existing targeted
metrics/concurrency race suite passed. The real Sentinel test ran as a Linux
cross-compiled test binary without race instrumentation; the ordinary native race
run skips it unless its integration environment is supplied.

For a repeat run, provision a **disposable** master/replica/Sentinel cluster named
`issue5`, using DB 13, and configure down-after at least 5000 ms. Set
`CONCURRENCY_TEST_SENTINEL_ADDR` and `CONCURRENCY_TEST_SENTINEL_REPLICA_ADDR`, then run
`go test ./service -run '^TestChannelConcurrencySentinelFailover$' -v -count=1` from
a host/container that can reach Sentinel's advertised Redis addresses. The test
shuts down the elected master; recreate the cluster before repeating. Temporary
configuration, test binary/log, Sentinel log, HTTP observer and all 266 observations
are retained in `/tmp/issue5-sentinel-validation/`. Test containers and network were
removed after verification.

This verifies an actual single-master outage and automatic promotion, not Sentinel
quorum resilience, network partitions or preservation of unreplicated writes.
No Prometheus server was attached during this follow-up; deployment/domain labels
remain static target configuration, and their query behavior was tested separately
with promtool. Multi-host Redis-backed load testing was not performed.

## Issue status

The previously missing Sentinel functional verification is now covered within the
topology and limits described above.
The Issue #5 acceptance scope is satisfied by the evidence below. Multi-host load,
Sentinel quorum/partition testing and production deployment are not added as
acceptance requirements. Closing this phase does not close parent #4 or implement
dependent phase #6.

## Acceptance matrix

| Issue #5 requirement | Evidence | Result |
| --- | --- | --- |
| Format and fixed metric contract | `TestCollectorExposesOnlyCurrentKnownValues`, Sentinel complete output comparison, live `promtool check metrics` | Passed |
| All channels, empty directory, creation/update/deletion | `TestChannelConcurrencyConfigProjection`, `TestMetricsHTTPCurrentSnapshotAndKeepAlive`, disabled Sentinel test channel | Passed |
| Limit parsing and no repairs | `TestParseConcurrencyLimit`, persisted damaged-setting HTTP assertion, finite-to-unlimited HTTP scrape | Passed |
| Existing reservation semantics and lower limits | Memory/Redis concurrency suites, real Relay/SSE load above, HTTP limit lowered to 1 while two live reservations remain visible and uncancelled; acquisition/release policy unchanged | Passed |
| Failure and recovery | Snapshot partial-failure cases, Redis uninitialized/failure tests, real paused Redis and Sentinel outage/recovery | Passed |
| Snapshot lifecycle and cancellation | Fresh-registry comparison, deletion HTTP assertion, collector cancellation, real DB lock deadlines, Redis timeout and live lease renewal | Passed |
| Two-request entry limit and slot release | Real TCP concurrency/cancellation and slow-reader tests, successful and dependency-error response tests | Passed |
| Local sum and Redis deduplication | Fixed promtool inputs: two local counts of 2 give 4; shared observations of 2 remain 2 with limit 10 and ratio 0.2 | Passed |
| Deployment/domain isolation and Sentinel identity | Queries retain deployment/domain grouping, fixed independent-domain inputs; actual Sentinel promotion with unchanged application scope and static target-label contract | Passed |
| Partial failures and configuration disagreement | Complete snapshot output tests and promtool cases: valid peer remains usable, conflicting limits suppress ratio, all failed targets produce no count | Passed |
| Routes and deployment isolation | Actual embedded/external frontend modes and isolated Docker/Nginx checks described above | Passed |
| Data minimization and sensitive output | Projection fixture has no credential columns; only ID/setting selected; no repairing getter; fixed-label collector and fixed HTTP errors | Passed |
| Regression and performance | Root/relaykit vet/build/tests, targeted race; SQLite/MySQL/PostgreSQL and Redis integration; recorded 1000-channel, 1/5-instance Relay comparisons | Passed |

Performance evidence is for the explicitly recorded process-local mode, with Redis
correctness and live collection verified separately. The existing Relay acquisition,
deferred release, retry, billing and streaming code is outside this change.

Delivery recheck on 2026-09-08: root and independent relaykit `go vet ./...` and
`go build ./...`, `make test`, and `go test -race ./controller -run '^TestMetricsHTTP'
-count=1` passed after adding the lower-limit HTTP assertion. Remote CI results are
recorded on the delivery pull request.

The first PR CI run passed backend checks but found two pre-existing
`Promise.withResolvers` calls in channel-list tests that are unavailable under the
project's ES2022 TypeScript library. Delivery includes a test-only correction using
the repository's existing deferred-Promise pattern; no frontend application logic,
compiler target or dependency was changed. Forced typecheck and affected-file lint
passed. All 38 frontend test files (196 tests) passed locally with
`NODE_OPTIONS=--no-experimental-webstorage bun run test`: the unmodified Node 26
experimental storage globals had caused eight jsdom storage failures. This local
environment override is not a production or CI configuration change.

## Issue #8 implementation validation — 2026-11-04

Workspace: `release/v1.0.0-rc.46-pro`. Existing `docs/diff-trace/` and prior
`output/` artifacts were retained. This patch was not deployed or published.

Observed passing checks:

- `go test ./controller ./service ./middleware ./model ./relay ./pkg/concurrencymetrics -count=1`.
  Covers ordinary relay cancellation, retries/final accounting, Responses WS,
  request-policy/billing regressions and existing legacy limiter behavior.
- `go test -race ./service -run 'UserResource|ResourceStore|ResourceRedisDatabases|MemoryChannelConcurrency' -count=1`.
- `CONCURRENCY_TEST_REDIS_ADDR=127.0.0.1:16389 go test ./service -run 'TestUserResourcesRealRedisProcesses|TestChannelConcurrencyRealRedis|TestUserResource|TestResource' -count=1 -v`.
  Dedicated Redis 7 Alpine container: independent OS processes share one user cap;
  exit leaves a slot, a new process cannot acquire it, accelerated server-side
  expiry frees concurrency without refunding window usage. Real CLIENT PAUSE
  causes failed reads, not cancellation. Unit cases additionally exercise all
  three-resource atomicity, rolling boundaries, zero allowance, unlimited-to-limit
  transition, idempotent release, domains, unavailable statistics and data loss.
- `CONCURRENCY_TEST_SENTINEL_ADDR=127.0.0.1:26390 CONCURRENCY_TEST_SENTINEL_REPLICA_ADDR=127.0.0.1:16391 go test ./service -run '^TestChannelConcurrencySentinelFailover$' -count=1 -v`.
  Dedicated master/replica/Sentinel, actual `SHUTDOWN NOSAVE`, promotion after about
  6.8 seconds. User window and slot were replicated before failure; new master
  retained them, channel expiry timestamp did not renew, and releases succeeded.
  This does **not** prove unreplicated writes survive failover.
- `CONCURRENCY_TEST_MYSQL_DSN=... CONCURRENCY_TEST_POSTGRES_DSN=... go test ./model -run TestUserResourceOptionsDatabaseMatrix -count=1 -v`.
  SQLite **3.50.4**, MySQL **8.0.46**, PostgreSQL **16.15**. Existing options table,
  repeated migration, prior unrelated row preservation, defaults/override update,
  stale-cache audit preimage and invalid limit rejection passed. No schema or
  driver change is introduced; this is not a whole released-database upgrade test.
- Additional focused controller checks cover RootAuth rejection, authenticated self
  identity despite another ID in query parameters, anonymous rejection, audit
  before/after/reason, current username mapping, absent unavailable metric samples,
  free/billing-disabled estimates, cross-key user RPM, and retry usage unchanged by
  upstream actual input counts. Legacy disabled limiter is tested with nil Redis.
- Frontend `bun run test src/features/dashboard/components/overview/__tests__ src/features/system-settings/request-limits/__tests__`: **17 tests passed**.
  Includes 39/40/79/80%, unlimited, failure/stale data suppression, hidden/unmounted
  polling, required reason, zero/unlimited overrides, invalid limits and failed
  save retention. `bun run typecheck`, changed-file `oxlint`, and `bun run build`
  passed. Production build reported approximately 67.4 MB raw / 20.8 MB gzip for
  the entire existing application, not the marginal size of this feature.
- `go build ./...`, `go test ./relay/... -count=1` and `git diff --check` passed.
- Playwright visually inspected actual feature components with isolated API
  fixtures: desktop, 390px dark mobile, and settings JSON editor. Screenshots are
  under `output/playwright/issue8-resources-*.png`. Reused PanelWrapper, ErrorState,
  Progress, JsonCodeEditor, settings form/actions, RHF/Zod and TanStack Query;
  no replacement general-purpose component or production dependency was added.
- Live acceptance used two compiled gateway processes, shared SQLite/Redis 7,
  a round-robin HTTP proxy and a controllable local OpenAI-compatible upstream.
  A real browser logged in with encrypted password login, saved limits with a
  reason, and displayed live usage. The persisted operation audit contains the
  root identity, reason and correct before/after values. Two API keys belonging
  to the same user were used against different replicas.
- `output/issue8-validation/live-dual-gateway.log`: cross-key concurrent rejection
  leaves all counters unchanged; both peers expose equal usage gauges; upstream
  actual input 1234 does not replace admission estimate 9; two upstream retry
  attempts consume one RPM; completed streaming releases concurrency; RPM/TPM
  and enabled-zero rejection occur before upstream; Retry-After is present for
  the exhausted rolling window; an actual 61-second wait expires RPM/TPM.
- Redis pause/recovery verified restricted 503, omitted unavailable usage/metrics,
  no cancellation of admitted work, and successful all-unlimited requests. A
  timed-out release was observed to execute after unpause, so timeout is not
  treated as proof that the write failed. Initial default-timeout trials also
  encountered existing authentication-cache delays and a global API limiter
  failure before the resource endpoint. The isolated resource-failure run used
  Redis URL read/write/dial timeouts of 200ms, no client retries, and
  `GLOBAL_API_RATE_LIMIT_ENABLE=false`; these were local fixture settings only.
- `output/issue8-validation/live-lifecycle.log`: an actual peer restart retains
  shared concurrency/window state; a streaming client's socket disconnect
  releases concurrency without refunding RPM; an unlimited user is still rejected
  with channel-store 503 when the selected channels are constrained during Redis
  failure. Config synchronization was set to two seconds for local acceptance.
- Live evidence: `output/playwright/issue8-live-{settings,overview}.png`,
  `output/issue8-validation/live-browser-audit.json`, and peer resource/metrics
  snapshots. The overview screenshot shows 1/1 concurrent, 9/10000 estimated
  input TPM, and 1/10 RPM. Only the upstream and fixture data were synthetic;
  authentication, config persistence, relay, Redis admission and the UI were real.
- Final review removed duplicate unlimited-state text from the overview.
  All 17 frontend tests, typecheck, affected-file oxlint, production frontend build,
  final embedded gateway build and `git diff --check` passed after this change.

Verification boundaries:

- No production traffic, sustained capacity benchmark, exhaustive network-partition
  matrix or half-hour wall-clock endurance test was run. The 30-minute expiry
  was accelerated with controlled clocks/server timestamps; the 60-second window
  was additionally verified with real elapsed time. Live browser acceptance used
  a local upstream and shared SQLite; MySQL/PostgreSQL and Sentinel were exercised
  separately by the integration checks above.
- Input is an estimate of client-visible DTO input; remote opaque references and
  provider-specific fields can differ from actual full model context. Redis loss,
  asynchronous replication and exceptional >30-minute requests can undercount;
  none of these trigger recovery coordination or additional request cancellation.
- Configuration propagation uses existing polling (default 60 seconds) and can lag
  longer under database failure. Scrapes are bounded, may omit samples on timeout,
  and have user-count-dependent cardinality/cost; no production-size scrape load
  benchmark was performed.
- Existing channel WebSocket coverage is unchanged; channel slots cover ordinary
  HTTP attempts. New user slots additionally cover Responses WebSocket turns.
- New user resource UI strings have English and Simplified Chinese text; the other
  locale catalogs include English fallback entries for the newly added strings.

Upstream merge considerations: the main HTTP relay adds one admission call outside
its retry loop, Responses WS adds one per-turn call, and option/metrics/routes gain
small registration points. Legacy model limiting is not rewritten. The channel
renewal/cancellation behavior is intentionally removed; do not restore it when
merging upstream. Redis deployments use existing database/instance isolation; no
`GATEWAY_DEPLOYMENT_ID` is required. Channel keys retain their existing names. Use
a drained upgrade when changing slot lifetime/renewal semantics. Review these
integration points and rerun the focused regression tests after upstream merges.

Follow-up correction: removed the deployment-ID option, resource key namespace,
self API domain field and gateway-generated domain metric labels. Independent
installations use separate Redis databases/instances. Prometheus examples now
retain an operator-provided `deployment` target label. Earlier live artifacts
above record the pre-correction implementation and are historical evidence.
Correction checks passed: `go test ./service ./controller ./relay
./pkg/concurrencymetrics -count=1`, focused service race tests, `go build ./...`,
and real Redis tests with independent OS processes sharing database 13 and an
independent admission in database 14. Missing deployment ID is accepted in default,
single and cluster modes with Redis configured. Logs are
`output/issue8-validation/remove-deployment-id-*.log`.

PR CI follow-up: the complete suite found missing declarations for four backend
translation keys and three personal-access-token routes. Added the key constants
and existing `profile:read` / `option:read` / `option:write` route grants without
changing role checks or the deny-by-default policy. Regression tests first
reproduced valid scoped tokens receiving `ACCESS_TOKEN_ROUTE_UNDECLARED`, then
verified allowed reads/writes, missing scopes, read-only write denial, non-root
configuration denial, and authenticated self identity despite a supplied user ID.

Authorization review references: [OWASP ASVS 5.0.0 V8](https://github.com/OWASP/ASVS/blob/v5.0.0/5.0/en/0x17-V8-Authorization.md)
8.2.1 (function permissions), 8.2.2 (object access), 8.3.1 (server enforcement),
and the OWASP [Authentication](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html),
[Session Management](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
and [Authorization](https://cheatsheetseries.owasp.org/cheatsheets/Authorization_Cheat_Sheet.html)
cheat sheets. This is a focused route-authorization check, not a full ASVS certification;
credential issuance, session rotation and authentication lifecycles are unchanged.

Follow-up verification passed: focused i18n/router/controller regressions;
`make test` across the root, relaykit and tokenkit modules; `GOWORK=off go vet ./...`
and `GOWORK=off go build ./...` independently in all three modules; and
`git diff --check`. Local logs: `output/issue8-validation/ci-fix-*.log`.
