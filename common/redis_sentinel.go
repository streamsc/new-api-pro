package common

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"github.com/go-redis/redis/v8"
)

const (
	redisSentinelMasterNameEnv = "REDIS_SENTINEL_MASTER_NAME"
	redisSentinelAddrsEnv      = "REDIS_SENTINEL_ADDRS"
	redisSentinelUsernameEnv   = "REDIS_SENTINEL_USERNAME"
	redisSentinelPasswordEnv   = "REDIS_SENTINEL_PASSWORD"
)

type redisSentinelConfig struct {
	masterName string
	addrs      []string
	username   string
	password   string
}

func hasRedisSentinelConfig() bool {
	return os.Getenv(redisSentinelMasterNameEnv) != "" ||
		os.Getenv(redisSentinelAddrsEnv) != "" ||
		os.Getenv(redisSentinelUsernameEnv) != "" ||
		os.Getenv(redisSentinelPasswordEnv) != ""
}

func parseRedisSentinelConfig() (*redisSentinelConfig, error) {
	masterName := strings.TrimSpace(os.Getenv(redisSentinelMasterNameEnv))
	rawAddrs := strings.TrimSpace(os.Getenv(redisSentinelAddrsEnv))
	username := strings.TrimSpace(os.Getenv(redisSentinelUsernameEnv))
	password := os.Getenv(redisSentinelPasswordEnv)

	if masterName == "" && rawAddrs == "" {
		if username != "" || password != "" {
			return nil, fmt.Errorf("%s and %s are required when Redis Sentinel authentication is configured", redisSentinelMasterNameEnv, redisSentinelAddrsEnv)
		}
		return nil, nil
	}
	if masterName == "" || rawAddrs == "" {
		return nil, fmt.Errorf("%s and %s must be configured together", redisSentinelMasterNameEnv, redisSentinelAddrsEnv)
	}
	if username != "" && password == "" {
		return nil, fmt.Errorf("%s requires %s", redisSentinelUsernameEnv, redisSentinelPasswordEnv)
	}

	rawAddrList := strings.Split(rawAddrs, ",")
	addrs := make([]string, 0, len(rawAddrList))
	for _, rawAddr := range rawAddrList {
		addr := strings.TrimSpace(rawAddr)
		if addr == "" {
			return nil, fmt.Errorf("%s contains an empty address", redisSentinelAddrsEnv)
		}
		host, portText, err := net.SplitHostPort(addr)
		if err != nil || host == "" {
			return nil, fmt.Errorf("invalid Redis Sentinel address %q: expected host:port", addr)
		}
		port, err := strconv.Atoi(portText)
		if err != nil || port < 1 || port > 65535 {
			return nil, fmt.Errorf("invalid Redis Sentinel address %q: port must be between 1 and 65535", addr)
		}
		addrs = append(addrs, addr)
	}

	return &redisSentinelConfig{
		masterName: masterName,
		addrs:      addrs,
		username:   username,
		password:   password,
	}, nil
}

func newRedisClient(opt *redis.Options) (*redis.Client, *redisSentinelConfig, error) {
	sentinelConfig, err := parseRedisSentinelConfig()
	if err != nil {
		return nil, nil, err
	}
	if sentinelConfig == nil {
		return redis.NewClient(opt), nil, nil
	}

	return redis.NewFailoverClient(newRedisFailoverOptions(opt, sentinelConfig)), sentinelConfig, nil
}

func newRedisFailoverOptions(opt *redis.Options, sentinelConfig *redisSentinelConfig) *redis.FailoverOptions {
	return &redis.FailoverOptions{
		MasterName:         sentinelConfig.masterName,
		SentinelAddrs:      sentinelConfig.addrs,
		SentinelUsername:   sentinelConfig.username,
		SentinelPassword:   sentinelConfig.password,
		Dialer:             opt.Dialer,
		OnConnect:          opt.OnConnect,
		Username:           opt.Username,
		Password:           opt.Password,
		DB:                 opt.DB,
		MaxRetries:         opt.MaxRetries,
		MinRetryBackoff:    opt.MinRetryBackoff,
		MaxRetryBackoff:    opt.MaxRetryBackoff,
		DialTimeout:        opt.DialTimeout,
		ReadTimeout:        opt.ReadTimeout,
		WriteTimeout:       opt.WriteTimeout,
		PoolFIFO:           opt.PoolFIFO,
		PoolSize:           opt.PoolSize,
		MinIdleConns:       opt.MinIdleConns,
		MaxConnAge:         opt.MaxConnAge,
		PoolTimeout:        opt.PoolTimeout,
		IdleTimeout:        opt.IdleTimeout,
		IdleCheckFrequency: opt.IdleCheckFrequency,
		TLSConfig:          opt.TLSConfig,
	}
}
