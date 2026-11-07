package service

import (
	"context"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/prometheus/client_golang/prometheus"
)

var (
	walletBalanceDesc   = prometheus.NewDesc("new_api_user_wallet_balance_quota", "Database wallet balance in quota units; shared across replicas.", []string{"user_id"}, nil)
	walletUsedDesc      = prometheus.NewDesc("new_api_user_used_quota", "Accumulated wallet and subscription usage in quota units; refunds can decrease this gauge.", []string{"user_id"}, nil)
	walletInfoDesc      = prometheus.NewDesc("new_api_user_info", "Current username mapping.", []string{"scope", "user_id", "username"}, nil)
	walletSuccessDesc   = prometheus.NewDesc("new_api_wallet_snapshot_success", "Whether the latest wallet snapshot query succeeded.", nil, nil)
	walletTimestampDesc = prometheus.NewDesc("new_api_wallet_snapshot_timestamp_seconds", "Unix time of the last successfully completed wallet snapshot; zero before first success.", nil, nil)
)

type walletSnapshot struct {
	users     []model.UserWalletSnapshot
	success   float64
	timestamp float64
}

// WalletMetrics publishes immutable snapshots. Only Run writes; concurrent
// scrapes each read one complete state, including its health and timestamp.
type WalletMetrics struct {
	snapshot atomic.Pointer[walletSnapshot]
}

var DefaultWalletMetrics WalletMetrics

// Run must be started once per process, after database initialization. Queries
// run serially, independently of scrapes, and stop before the database closes.
func (m *WalletMetrics) Run(ctx context.Context) {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := m.refresh(ctx, model.ReadUserWalletSnapshot); err != nil && ctx.Err() == nil {
			logger.LogError(ctx, common.LogText("wallet snapshot query failed: %s", err.Error()))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *WalletMetrics) refresh(ctx context.Context, read func(context.Context) ([]model.UserWalletSnapshot, error)) error {
	queryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	users, err := read(queryCtx)
	if err == nil {
		err = queryCtx.Err()
	}
	next := &walletSnapshot{}
	if err != nil {
		if previous := m.snapshot.Load(); previous != nil {
			next.users = previous.users
			next.timestamp = previous.timestamp
		}
	} else {
		next.users = users
		next.success = 1
		next.timestamp = float64(time.Now().Unix())
	}
	m.snapshot.Store(next)
	return err
}

func (*WalletMetrics) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range []*prometheus.Desc{walletBalanceDesc, walletUsedDesc, walletInfoDesc, walletSuccessDesc, walletTimestampDesc} {
		ch <- desc
	}
}

func (m *WalletMetrics) Collect(ch chan<- prometheus.Metric) {
	snapshot := m.snapshot.Load()
	if snapshot == nil {
		snapshot = &walletSnapshot{}
	}
	ch <- prometheus.MustNewConstMetric(walletSuccessDesc, prometheus.GaugeValue, snapshot.success)
	ch <- prometheus.MustNewConstMetric(walletTimestampDesc, prometheus.GaugeValue, snapshot.timestamp)
	scope := "memory"
	if common.RedisEnabled {
		scope = "redis"
	}
	for _, user := range snapshot.users {
		id := strconv.Itoa(user.Id)
		ch <- prometheus.MustNewConstMetric(walletBalanceDesc, prometheus.GaugeValue, float64(user.Quota), id)
		ch <- prometheus.MustNewConstMetric(walletUsedDesc, prometheus.GaugeValue, float64(user.UsedQuota), id)
		ch <- prometheus.MustNewConstMetric(walletInfoDesc, prometheus.GaugeValue, 1, scope, id, user.Username)
	}
}
