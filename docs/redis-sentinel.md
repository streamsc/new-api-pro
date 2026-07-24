# Redis Sentinel Configuration

New API can use Redis Sentinel to discover the current Redis master and follow automatic failovers. The application continues to expose the same Redis behavior to caches, rate limiters, and metrics regardless of whether standalone or Sentinel mode is selected.

## Standalone mode

Standalone mode remains the default. Configure only the existing connection string:

```dotenv
REDIS_CONN_STRING=redis://redis-user:redis-password@redis:6379/0
REDIS_POOL_SIZE=10
```

If `REDIS_CONN_STRING` and all Sentinel variables are unset, Redis is disabled as before.

## Sentinel mode

Sentinel mode is enabled when both `REDIS_SENTINEL_MASTER_NAME` and `REDIS_SENTINEL_ADDRS` are configured:

```dotenv
REDIS_CONN_STRING=redis://redis-user:redis-password@redis:6379/0
REDIS_POOL_SIZE=10

REDIS_SENTINEL_MASTER_NAME=mymaster
REDIS_SENTINEL_ADDRS=sentinel-1:26379,sentinel-2:26379,sentinel-3:26379
```

`REDIS_SENTINEL_ADDRS` is a comma-separated seed list. Each entry must use `host:port` syntax. IPv6 addresses must be bracketed, for example `[2001:db8::1]:26379`.

In Sentinel mode, `REDIS_CONN_STRING` supplies the Redis data-node username, password, database number, TLS settings, retry settings, and timeouts. Its host is not used to pin the client to a Redis master; master discovery uses the Sentinel address list.

`REDIS_POOL_SIZE` controls the connection pool size and applies to both modes.

## Sentinel authentication

Sentinel authentication is independent from Redis data-node authentication:

```dotenv
REDIS_CONN_STRING=redis://redis-user:redis-password@redis:6379/0
REDIS_SENTINEL_MASTER_NAME=mymaster
REDIS_SENTINEL_ADDRS=sentinel-1:26379,sentinel-2:26379,sentinel-3:26379
REDIS_SENTINEL_USERNAME=sentinel-user
REDIS_SENTINEL_PASSWORD=sentinel-password
```

- Set only `REDIS_SENTINEL_PASSWORD` for legacy Sentinel `requirepass` authentication.
- Set both `REDIS_SENTINEL_USERNAME` and `REDIS_SENTINEL_PASSWORD` for Sentinel ACL authentication.
- Configuring a Sentinel username without a password is rejected at startup.

## TLS

Use the existing `rediss://` connection-string scheme to enable TLS:

```dotenv
REDIS_CONN_STRING=rediss://redis-user:redis-password@redis.internal:6379/0
REDIS_SENTINEL_MASTER_NAME=mymaster
REDIS_SENTINEL_ADDRS=sentinel-1.internal:26379,sentinel-2.internal:26379,sentinel-3.internal:26379
```

The current go-redis client uses one TLS configuration for Redis data nodes and Sentinel nodes. Their certificates must therefore be valid for the server name derived from the `REDIS_CONN_STRING` host, or share a certificate naming policy that accepts it.

## Validation and startup behavior

The application stops during startup instead of silently falling back to standalone mode when:

- only one of `REDIS_SENTINEL_MASTER_NAME` and `REDIS_SENTINEL_ADDRS` is configured;
- Sentinel authentication is configured without the master name and address list;
- the address list contains an empty entry, a missing host or port, or a port outside `1-65535`;
- Sentinel variables are configured without `REDIS_CONN_STRING`;
- the startup Redis `PING` cannot reach the current master through Sentinel.

When debug logging is enabled, startup logs include the Sentinel master name, Sentinel addresses, and selected Redis database. Credentials are never logged.
