package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/glebarez/sqlite"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// External DSNs must name disposable, isolated databases.
func TestWalletMetricsDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector = sqlite.Open(":memory:")
			if dialect != "sqlite" {
				dsn := os.Getenv("TEST_WALLET_" + strings.ToUpper(dialect) + "_DSN")
				if dsn == "" {
					t.Skip("isolated database DSN not configured")
				}
				if dialect == "mysql" {
					driver = mysql.Open(dsn)
				} else {
					driver = postgres.Open(dsn)
				}
			}
			db, err := gorm.Open(driver, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			oldDB, oldRedis, oldOptions := model.DB, common.RedisEnabled, common.OptionMap
			model.DB, common.RedisEnabled, common.OptionMap = db, false, map[string]string{}
			t.Cleanup(func() { model.DB, common.RedisEnabled, common.OptionMap = oldDB, oldRedis, oldOptions })
			// Minimal real users table exercises the production projection and GORM's
			// soft-delete scope without unrelated schema migrations or authentication.
			type userRow struct {
				Id        int
				Username  string
				Quota     int
				UsedQuota int
				Status    int
				DeletedAt gorm.DeletedAt
			}
			require.NoError(t, db.Table("users").AutoMigrate(&userRow{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable("users")) })
			users := []userRow{{Id: 1, Username: "inactive", Quota: 100, UsedQuota: 500, Status: 1}, {Id: 2, Username: "disabled", Status: 2}, {Id: 3, Username: "deleted", Quota: 90, DeletedAt: gorm.DeletedAt{Time: time.Now(), Valid: true}}}
			require.NoError(t, db.Table("users").Create(&users).Error)
			versionSQL := "select version()"
			if dialect == "sqlite" {
				versionSQL = "select sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Logf("%s %s", dialect, version)
			queryCount := 0
			require.NoError(t, db.Callback().Query().Before("gorm:query").Register("wallet_test_count", func(*gorm.DB) { queryCount++ }))
			var metrics WalletMetrics
			require.NoError(t, metrics.refresh(context.Background(), model.ReadUserWalletSnapshot))
			assert.Equal(t, 1, queryCount)
			require.Len(t, metrics.snapshot.Load().users, 2)
			expected := `
# HELP new_api_user_wallet_balance_quota Database wallet balance in quota units; shared across replicas.
# TYPE new_api_user_wallet_balance_quota gauge
new_api_user_wallet_balance_quota{user_id="1"} 100
new_api_user_wallet_balance_quota{user_id="2"} 0
`
			require.NoError(t, testutil.CollectAndCompare(&metrics, strings.NewReader(expected), "new_api_user_wallet_balance_quota"))
			require.NoError(t, testutil.CollectAndCompare(&metrics, strings.NewReader(expected), "new_api_user_wallet_balance_quota"))
			assert.Equal(t, 1, queryCount, "scrapes must not query the database")
			// Coexist with active resource metrics: exactly one username producer.
			userResourceEvents.Lock()
			previousIDs := userResourceEvents.ids
			userResourceEvents.ids = map[int]struct{}{1: {}}
			userResourceEvents.Unlock()
			t.Cleanup(func() { userResourceEvents.Lock(); userResourceEvents.ids = previousIDs; userResourceEvents.Unlock() })
			registry := prometheus.NewPedanticRegistry()
			registry.MustRegister(&metrics, UserResourcesCollector{Context: context.Background()})
			families, err := registry.Gather()
			require.NoError(t, err)
			for _, family := range families {
				if family.GetName() == "new_api_user_info" {
					assert.Len(t, family.Metric, 2)
				}
			}
			require.NoError(t, db.Table("users").Where("id = ?", 1).Updates(map[string]any{"username": "renamed", "quota": 120, "used_quota": 480}).Error)
			require.NoError(t, db.Delete(&model.User{}, 2).Error)
			require.NoError(t, metrics.refresh(context.Background(), model.ReadUserWalletSnapshot))
			require.Equal(t, []model.UserWalletSnapshot{{Id: 1, Username: "renamed", Quota: 120, UsedQuota: 480}}, metrics.snapshot.Load().users)
			require.NoError(t, testutil.CollectAndCompare(&metrics, strings.NewReader(`
# HELP new_api_user_info Current username mapping.
# TYPE new_api_user_info gauge
new_api_user_info{scope="memory",user_id="1",username="renamed"} 1
# HELP new_api_user_used_quota Accumulated wallet and subscription usage in quota units; refunds can decrease this gauge.
# TYPE new_api_user_used_quota gauge
new_api_user_used_quota{user_id="1"} 480
# HELP new_api_user_wallet_balance_quota Database wallet balance in quota units; shared across replicas.
# TYPE new_api_user_wallet_balance_quota gauge
new_api_user_wallet_balance_quota{user_id="1"} 120
`), "new_api_user_info", "new_api_user_used_quota", "new_api_user_wallet_balance_quota"))
			previous := metrics.snapshot.Load()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			require.ErrorIs(t, metrics.refresh(ctx, model.ReadUserWalletSnapshot), context.Canceled)
			assert.Equal(t, previous.users, metrics.snapshot.Load().users)
			assert.Equal(t, previous.timestamp, metrics.snapshot.Load().timestamp)
			assert.Zero(t, metrics.snapshot.Load().success)
			// Exhaust the real SQL pool: cancellation must reach database/sql,
			// not just be checked before starting the query.
			connection, err := sqlDB.Conn(context.Background())
			require.NoError(t, err)
			blockedCtx, stop := context.WithTimeout(context.Background(), 20*time.Millisecond)
			err = metrics.refresh(blockedCtx, model.ReadUserWalletSnapshot)
			stop()
			require.NoError(t, connection.Close())
			require.ErrorIs(t, err, context.DeadlineExceeded)
			assert.Equal(t, previous.users, metrics.snapshot.Load().users)
			assert.Equal(t, previous.timestamp, metrics.snapshot.Load().timestamp)
			require.NoError(t, db.Unscoped().Delete(&model.User{}, 1).Error)
			require.NoError(t, metrics.refresh(context.Background(), model.ReadUserWalletSnapshot))
			assert.Empty(t, metrics.snapshot.Load().users)
			assert.Equal(t, 1.0, metrics.snapshot.Load().success)
		})
	}
}

func TestWalletMetricsFailureAndAtomicCollection(t *testing.T) {
	var metrics WalletMetrics
	assert.Equal(t, 2, testutil.CollectAndCount(&metrics))
	failure := errors.New("partial read failed")
	readFailure := func(context.Context) ([]model.UserWalletSnapshot, error) {
		return []model.UserWalletSnapshot{{Id: 99}}, failure
	}
	require.ErrorIs(t, metrics.refresh(context.Background(), readFailure), failure)
	assert.Equal(t, 2, testutil.CollectAndCount(&metrics))
	assert.Zero(t, metrics.snapshot.Load().timestamp)
	read := func(context.Context) ([]model.UserWalletSnapshot, error) {
		return []model.UserWalletSnapshot{{Id: 1, Username: "old", Quota: 100, UsedQuota: 200}}, nil
	}
	require.NoError(t, metrics.refresh(context.Background(), read))
	old := metrics.snapshot.Load()
	ch := make(chan prometheus.Metric)
	done := make(chan struct{})
	go func() { metrics.Collect(ch); close(ch); close(done) }()
	first := <-ch // Collect has captured the old snapshot and is blocked on send.
	require.ErrorIs(t, metrics.refresh(context.Background(), readFailure), failure)
	read = func(context.Context) ([]model.UserWalletSnapshot, error) {
		return []model.UserWalletSnapshot{{Id: 2, Username: "new", Quota: 300}}, nil
	}
	require.NoError(t, metrics.refresh(context.Background(), read))
	got := []prometheus.Metric{first}
	for metric := range ch {
		got = append(got, metric)
	}
	<-done
	// Gather the captured collection, rather than a second call to Collect.
	registry := prometheus.NewPedanticRegistry()
	registry.MustRegister(walletCapturedMetrics(got))
	families, err := registry.Gather()
	require.NoError(t, err)
	for _, family := range families {
		switch family.GetName() {
		case "new_api_wallet_snapshot_success":
			assert.Equal(t, 1.0, family.Metric[0].Gauge.GetValue())
		case "new_api_wallet_snapshot_timestamp_seconds":
			assert.Equal(t, old.timestamp, family.Metric[0].Gauge.GetValue())
		default:
			require.Len(t, family.Metric, 1)
			for _, label := range family.Metric[0].Label {
				if label.GetName() == "user_id" {
					assert.Equal(t, "1", label.GetValue())
				}
				if label.GetName() == "username" {
					assert.Equal(t, "old", label.GetValue())
				}
			}
		}
	}
}

type walletCapturedMetrics []prometheus.Metric

func (walletCapturedMetrics) Describe(chan<- *prometheus.Desc) {}
func (m walletCapturedMetrics) Collect(ch chan<- prometheus.Metric) {
	for _, metric := range m {
		ch <- metric
	}
}

func TestWalletMetricsQueryDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var metrics WalletMetrics
		started := time.Now()
		err := metrics.refresh(context.Background(), func(ctx context.Context) ([]model.UserWalletSnapshot, error) {
			<-ctx.Done()
			return []model.UserWalletSnapshot{{Id: 1}}, ctx.Err()
		})
		require.ErrorIs(t, err, context.DeadlineExceeded)
		assert.Equal(t, 5*time.Second, time.Since(started))
		assert.Empty(t, metrics.snapshot.Load().users)
	})
}

func BenchmarkWalletMetrics(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
			require.NoError(b, err)
			sqlDB, err := db.DB()
			require.NoError(b, err)
			b.Cleanup(func() { _ = sqlDB.Close() })
			require.NoError(b, db.Table("users").AutoMigrate(&struct {
				Id        int
				Username  string
				Quota     int
				UsedQuota int
				DeletedAt gorm.DeletedAt
			}{}))
			rows := make([]model.UserWalletSnapshot, count)
			for i := range rows {
				rows[i] = model.UserWalletSnapshot{Id: i + 1, Username: fmt.Sprintf("user%d", i), Quota: 100, UsedQuota: 200}
			}
			require.NoError(b, db.Table("users").CreateInBatches(rows, 100).Error)
			previous := model.DB
			model.DB = db
			b.Cleanup(func() { model.DB = previous })
			var metrics WalletMetrics
			b.Run("query", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					require.NoError(b, metrics.refresh(context.Background(), model.ReadUserWalletSnapshot))
				}
			})
			registry := prometheus.NewRegistry()
			registry.MustRegister(&metrics)
			b.Run("gather", func(b *testing.B) {
				b.ReportAllocs()
				for b.Loop() {
					_, err := registry.Gather()
					require.NoError(b, err)
				}
			})
		})
	}
}

func TestWalletMetricsRunLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
		require.NoError(t, err)
		sqlDB, err := db.DB()
		require.NoError(t, err)
		defer sqlDB.Close()
		require.NoError(t, db.Table("users").AutoMigrate(&struct {
			Id        int
			Username  string
			Quota     int
			UsedQuota int
			DeletedAt gorm.DeletedAt
		}{}))
		previous := model.DB
		model.DB = db
		defer func() { model.DB = previous }()
		var queries atomic.Int32
		require.NoError(t, db.Callback().Query().Before("gorm:query").Register("wallet_lifecycle", func(tx *gorm.DB) {
			if queries.Add(1) == 2 {
				<-tx.Statement.Context.Done()
			}
		}))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var metrics WalletMetrics
		done := make(chan struct{})
		go func() { metrics.Run(ctx); close(done) }()
		synctest.Wait()
		assert.Equal(t, int32(1), queries.Load(), "first query must not wait for the first tick")
		assert.Equal(t, 1.0, metrics.snapshot.Load().success)
		time.Sleep(59 * time.Second)
		assert.Equal(t, int32(1), queries.Load())
		time.Sleep(time.Second)
		synctest.Wait()
		assert.Equal(t, int32(2), queries.Load())
		cancel()
		<-done
		assert.Equal(t, int32(2), queries.Load(), "cancellation must stop the worker and in-flight query")
	})
}
