package common

import (
	"context"
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func clearRedisSentinelEnv(t *testing.T) {
	t.Helper()
	t.Setenv(redisSentinelMasterNameEnv, "")
	t.Setenv(redisSentinelAddrsEnv, "")
	t.Setenv(redisSentinelUsernameEnv, "")
	t.Setenv(redisSentinelPasswordEnv, "")
}

func TestInitRedisClientWithoutConfigurationDisablesRedis(t *testing.T) {
	oldRedisEnabled := RedisEnabled
	oldRDB := RDB
	t.Cleanup(func() {
		RedisEnabled = oldRedisEnabled
		RDB = oldRDB
	})

	t.Setenv("REDIS_CONN_STRING", "")
	clearRedisSentinelEnv(t)
	RedisEnabled = true

	err := InitRedisClient()

	require.NoError(t, err)
	assert.False(t, RedisEnabled)
}

func TestInitRedisClientRequiresConnectionStringForSentinel(t *testing.T) {
	t.Setenv("REDIS_CONN_STRING", "")
	clearRedisSentinelEnv(t)
	t.Setenv(redisSentinelMasterNameEnv, "mymaster")
	t.Setenv(redisSentinelAddrsEnv, "sentinel-1:26379")

	err := InitRedisClient()

	require.EqualError(t, err, "REDIS_CONN_STRING is required when Redis Sentinel is configured")
}

func TestParseRedisSentinelConfigDisabled(t *testing.T) {
	clearRedisSentinelEnv(t)

	config, err := parseRedisSentinelConfig()

	require.NoError(t, err)
	assert.Nil(t, config)
}

func TestParseRedisSentinelConfig(t *testing.T) {
	clearRedisSentinelEnv(t)
	t.Setenv(redisSentinelMasterNameEnv, "  mymaster  ")
	t.Setenv(redisSentinelAddrsEnv, " sentinel-1:26379, [2001:db8::1]:26380 ")
	t.Setenv(redisSentinelUsernameEnv, " sentinel-user ")
	t.Setenv(redisSentinelPasswordEnv, "sentinel-password")

	config, err := parseRedisSentinelConfig()

	require.NoError(t, err)
	require.NotNil(t, config)
	assert.Equal(t, "mymaster", config.masterName)
	assert.Equal(t, []string{"sentinel-1:26379", "[2001:db8::1]:26380"}, config.addrs)
	assert.Equal(t, "sentinel-user", config.username)
	assert.Equal(t, "sentinel-password", config.password)
}

func TestParseRedisSentinelConfigRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name       string
		masterName string
		addrs      string
		username   string
		password   string
		wantError  string
	}{
		{
			name:      "addresses without master",
			addrs:     "sentinel-1:26379",
			wantError: "REDIS_SENTINEL_MASTER_NAME and REDIS_SENTINEL_ADDRS must be configured together",
		},
		{
			name:       "master without addresses",
			masterName: "mymaster",
			wantError:  "REDIS_SENTINEL_MASTER_NAME and REDIS_SENTINEL_ADDRS must be configured together",
		},
		{
			name:      "authentication without sentinel",
			password:  "sentinel-password",
			wantError: "REDIS_SENTINEL_MASTER_NAME and REDIS_SENTINEL_ADDRS are required when Redis Sentinel authentication is configured",
		},
		{
			name:       "username without password",
			masterName: "mymaster",
			addrs:      "sentinel-1:26379",
			username:   "sentinel-user",
			wantError:  "REDIS_SENTINEL_USERNAME requires REDIS_SENTINEL_PASSWORD",
		},
		{
			name:       "empty address",
			masterName: "mymaster",
			addrs:      "sentinel-1:26379, ",
			wantError:  "REDIS_SENTINEL_ADDRS contains an empty address",
		},
		{
			name:       "missing port",
			masterName: "mymaster",
			addrs:      "sentinel-1",
			wantError:  "invalid Redis Sentinel address \"sentinel-1\": expected host:port",
		},
		{
			name:       "non-numeric port",
			masterName: "mymaster",
			addrs:      "sentinel-1:redis",
			wantError:  "invalid Redis Sentinel address \"sentinel-1:redis\": port must be between 1 and 65535",
		},
		{
			name:       "port out of range",
			masterName: "mymaster",
			addrs:      "sentinel-1:65536",
			wantError:  "invalid Redis Sentinel address \"sentinel-1:65536\": port must be between 1 and 65535",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			clearRedisSentinelEnv(t)
			t.Setenv(redisSentinelMasterNameEnv, test.masterName)
			t.Setenv(redisSentinelAddrsEnv, test.addrs)
			t.Setenv(redisSentinelUsernameEnv, test.username)
			t.Setenv(redisSentinelPasswordEnv, test.password)

			config, err := parseRedisSentinelConfig()

			assert.Nil(t, config)
			require.EqualError(t, err, test.wantError)
		})
	}
}

func TestNewRedisClientKeepsStandaloneOptions(t *testing.T) {
	clearRedisSentinelEnv(t)
	opt := &redis.Options{
		Addr:     "redis.example:6379",
		Username: "redis-user",
		Password: "redis-password",
		DB:       3,
		PoolSize: 17,
	}

	client, sentinelConfig, err := newRedisClient(opt)
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	assert.Nil(t, sentinelConfig)
	assert.Same(t, opt, client.Options())
}

func TestNewRedisFailoverOptionsCopiesConnectionSettings(t *testing.T) {
	dialer := func(ctx context.Context, network, addr string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	onConnect := func(context.Context, *redis.Conn) error { return nil }
	tlsConfig := &tls.Config{ServerName: "redis.example"}
	opt := &redis.Options{
		Dialer:             dialer,
		OnConnect:          onConnect,
		Username:           "redis-user",
		Password:           "redis-password",
		DB:                 4,
		MaxRetries:         5,
		MinRetryBackoff:    10 * time.Millisecond,
		MaxRetryBackoff:    time.Second,
		DialTimeout:        2 * time.Second,
		ReadTimeout:        3 * time.Second,
		WriteTimeout:       4 * time.Second,
		PoolFIFO:           true,
		PoolSize:           23,
		MinIdleConns:       2,
		MaxConnAge:         10 * time.Minute,
		PoolTimeout:        5 * time.Second,
		IdleTimeout:        6 * time.Minute,
		IdleCheckFrequency: 30 * time.Second,
		TLSConfig:          tlsConfig,
	}
	sentinelConfig := &redisSentinelConfig{
		masterName: "mymaster",
		addrs:      []string{"sentinel-1:26379", "sentinel-2:26379"},
		username:   "sentinel-user",
		password:   "sentinel-password",
	}

	got := newRedisFailoverOptions(opt, sentinelConfig)

	assert.Equal(t, sentinelConfig.masterName, got.MasterName)
	assert.Equal(t, sentinelConfig.addrs, got.SentinelAddrs)
	assert.Equal(t, sentinelConfig.username, got.SentinelUsername)
	assert.Equal(t, sentinelConfig.password, got.SentinelPassword)
	assert.NotNil(t, got.Dialer)
	assert.NotNil(t, got.OnConnect)
	assert.Equal(t, opt.Username, got.Username)
	assert.Equal(t, opt.Password, got.Password)
	assert.Equal(t, opt.DB, got.DB)
	assert.Equal(t, opt.MaxRetries, got.MaxRetries)
	assert.Equal(t, opt.MinRetryBackoff, got.MinRetryBackoff)
	assert.Equal(t, opt.MaxRetryBackoff, got.MaxRetryBackoff)
	assert.Equal(t, opt.DialTimeout, got.DialTimeout)
	assert.Equal(t, opt.ReadTimeout, got.ReadTimeout)
	assert.Equal(t, opt.WriteTimeout, got.WriteTimeout)
	assert.Equal(t, opt.PoolFIFO, got.PoolFIFO)
	assert.Equal(t, opt.PoolSize, got.PoolSize)
	assert.Equal(t, opt.MinIdleConns, got.MinIdleConns)
	assert.Equal(t, opt.MaxConnAge, got.MaxConnAge)
	assert.Equal(t, opt.PoolTimeout, got.PoolTimeout)
	assert.Equal(t, opt.IdleTimeout, got.IdleTimeout)
	assert.Equal(t, opt.IdleCheckFrequency, got.IdleCheckFrequency)
	assert.Same(t, opt.TLSConfig, got.TLSConfig)
}
