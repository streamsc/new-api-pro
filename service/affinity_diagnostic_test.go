package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-redis/redis/v8"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/gorm/schema"
)

func diagnosticTestDB(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(filepath.Join(t.TempDir(), "diagnostic.db") + "?_pragma=busy_timeout(5000)")
	case "mysql":
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("TEST_MYSQL_DSN not configured")
		}
		driver = mysql.Open(dsn)
	case "postgres":
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN not configured")
		}
		driver = postgres.Open(dsn)
	}
	db, err := gorm.Open(driver, &gorm.Config{Logger: logger.Default.LogMode(logger.Silent), NamingStrategy: schema.NamingStrategy{TablePrefix: "diagnostic_test_"}})
	require.NoError(t, err)
	oldDB, oldType := model.DB, common.MainDatabaseType()
	model.DB = db
	common.SetMainDatabaseType(common.DatabaseType(dialect))
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { model.DB = oldDB; common.SetMainDatabaseType(oldType); assert.NoError(t, sqlDB.Close()) })
	for _, table := range []any{&model.AffinityDiagnosticSample{}, &model.AffinityDiagnosticTask{}, &model.AffinityDiagnosticControl{}, &model.User{}} {
		require.NoError(t, db.Migrator().DropTable(table))
	}
	// Existing user data represents the released schema; additive migrations must
	// preserve it and repeated startup must not recreate or alter sample bytes.
	require.NoError(t, db.AutoMigrate(&model.User{}))
	require.NoError(t, db.Create(&model.User{Id: 17, Username: "diagnostic-target", Password: "unused"}).Error)
	for range 2 {
		require.NoError(t, db.AutoMigrate(&model.AffinityDiagnosticControl{}, &model.AffinityDiagnosticTask{}, &model.AffinityDiagnosticSample{}))
		require.NoError(t, model.InitAffinityDiagnostics(context.Background(), false))
	}
	return db
}
func diagnosticTestService(t *testing.T, client *redis.Client) *AffinityDiagnosticService {
	t.Helper()
	svc := NewAffinityDiagnosticService(client)
	old := affinityDiagnostics.Swap(svc)
	t.Cleanup(func() { affinityDiagnostics.Store(old) })
	return svc
}
func diagnosticRequest(body []byte) *gin.Context {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", 17)
	c.Set(common.RequestIdKey, "test-request")
	return c
}
func diagnosticTask(t *testing.T, svc *AffinityDiagnosticService) *model.AffinityDiagnosticTask {
	t.Helper()
	task, err := svc.Create(context.Background(), DiagnosticCreate{UserID: 17, CaptureBody: true}, 1)
	require.NoError(t, err)
	return task
}
func drainDiagnostic(t *testing.T, svc *AffinityDiagnosticService) *model.AffinityDiagnosticSample {
	t.Helper()
	select {
	case item := <-svc.queue:
		require.NoError(t, model.SaveAffinityDiagnosticSample(context.Background(), item.sample))
		svc.memory.Release(item.memory)
		<-svc.slots
		return item.sample
	default:
		t.Fatal("expected a queued sample")
		return nil
	}
}

func TestAffinityDiagnosticDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := diagnosticTestDB(t, dialect)
			ctx := context.Background()
			svc := diagnosticTestService(t, nil)
			versionSQL := "SELECT version()"
			if dialect == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			var version string
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Logf("database version: %s", version)
			var winners atomic.Int64
			var winner *model.AffinityDiagnosticTask
			var winnerMu sync.Mutex
			var wg sync.WaitGroup
			for range 8 {
				wg.Go(func() {
					task, err := svc.Create(ctx, DiagnosticCreate{UserID: 17, CaptureBody: true}, 1)
					if err == nil {
						winners.Add(1)
						winnerMu.Lock()
						winner = task
						winnerMu.Unlock()
					}
				})
			}
			wg.Wait()
			require.EqualValues(t, 1, winners.Load())
			body := []byte(" { \"duplicate\":1, \"duplicate\":2, \"number\":1e2, \"payload\":\"" + strings.Repeat("a", 70000) + "\" } \n")
			c := diagnosticRequest(body)
			storage, err := common.CreateBodyStorage(body)
			require.NoError(t, err)
			c.Set(common.KeyBodyStorage, storage)
			CaptureAffinityDiagnostic(c, "model", nil, false)
			CaptureAffinityDiagnostic(c, "changed", nil, false)
			require.Len(t, svc.queue, 1)
			require.NoError(t, svc.Stop(ctx, winner.ID, false))
			sample := drainDiagnostic(t, svc)
			require.NoError(t, db.AutoMigrate(&model.AffinityDiagnosticSample{}))
			require.NoError(t, db.AutoMigrate(&model.AffinityDiagnosticSample{}))
			saved, err := model.ReadAffinityDiagnosticSample(ctx, winner.ID, sample.ID)
			require.NoError(t, err)
			assert.Equal(t, body, saved.Body)
			rows, total, err := model.ListAffinityDiagnosticSamples(ctx, winner.ID, 0, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 1, total)
			require.Len(t, rows, 1)
			assert.Empty(t, rows[0].Body)
			assert.Empty(t, rows[0].Metadata)
			require.NoError(t, svc.Stop(ctx, winner.ID, true))
			err = model.SaveAffinityDiagnosticSample(ctx, &model.AffinityDiagnosticSample{TaskID: winner.ID, SizeBytes: 1})
			assert.ErrorIs(t, err, model.ErrDiagnosticUnavailable)
			_, err = model.ReadAffinityDiagnosticSample(ctx, winner.ID, sample.ID)
			assert.ErrorIs(t, err, model.ErrDiagnosticUnavailable)
			var ctl model.AffinityDiagnosticControl
			require.NoError(t, db.First(&ctl, 1).Error)
			assert.EqualValues(t, 20<<20, ctl.ReservedBytes)
			require.NoError(t, model.CleanupAffinityDiagnostics(ctx))
			require.NoError(t, db.First(&ctl, 1).Error)
			assert.Zero(t, ctl.ReservedBytes)
			// A restarted process stops in-flight local tasks without deleting saved data.
			task := diagnosticTask(t, svc)
			require.NoError(t, model.InitAffinityDiagnostics(ctx, true))
			var stopped model.AffinityDiagnosticTask
			require.NoError(t, db.First(&stopped, task.ID).Error)
			assert.Equal(t, "process_restart", stopped.Reason)
			assert.True(t, stopped.StatsIncomplete)
			// Expiry is enforced at read time, before the cleanup loop runs.
			require.NoError(t, db.Model(&stopped).Update("expires_at", time.Now().Add(-time.Second).UnixMilli()).Error)
			_, _, err = model.ListAffinityDiagnosticSamples(ctx, task.ID, 0, 20)
			assert.ErrorIs(t, err, model.ErrDiagnosticUnavailable)
			require.NoError(t, model.CleanupAffinityDiagnostics(ctx))
			// Whole-task reservations bound rapid stop/start churn, not just running tasks.
			for range 5 {
				task := diagnosticTask(t, svc)
				require.NoError(t, svc.Stop(ctx, task.ID, false))
			}
			_, err = svc.Create(ctx, DiagnosticCreate{UserID: 17}, 1)
			assert.ErrorIs(t, err, model.ErrDiagnosticCapacity)
		})
	}
}

type diagnosticReadSpy struct {
	reads  int
	source io.Reader
}

func (s *diagnosticReadSpy) Read(p []byte) (int, error) { s.reads++; return s.source.Read(p) }
func (s *diagnosticReadSpy) Close() error               { return nil }

func TestAffinityDiagnosticCaptureAndExtraction(t *testing.T) {
	diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	task := diagnosticTask(t, svc)
	t.Run("off and other user never read body", func(t *testing.T) {
		for _, off := range []bool{true, false} {
			c := diagnosticRequest(nil)
			spy := &diagnosticReadSpy{source: strings.NewReader(`{"key":"value"}`)}
			c.Request.Body = spy
			if off {
				affinityDiagnostics.Store(nil)
			} else {
				affinityDiagnostics.Store(svc)
				c.Set("id", 18)
			}
			CaptureAffinityDiagnostic(c, "model", nil, false)
			assert.Zero(t, spy.reads)
			assert.Same(t, spy, c.Request.Body)
		}
		affinityDiagnostics.Store(svc)
	})
	t.Run("passive read preserves bytes and headers and typed context", func(t *testing.T) {
		body := []byte(" {\"key\":\" value \",\"zero\":0,\"null\":null,\"obj\": {\"x\":1}} \n")
		c := diagnosticRequest(body)
		c.Request.Header["X-Session"] = []string{" first ", "second"}
		c.Request.Header.Set("Authorization", "Bearer secret")
		c.Request.Host = "example.test"
		c.Set("token_id", int64(4))
		c.Set("group", " group ")
		CaptureAffinityDiagnostic(c, "model", nil, false)
		assert.Empty(t, svc.queue)
		got, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		assert.Equal(t, body, got)
		require.NoError(t, c.Request.Body.Close())
		FinishAffinityDiagnostic(c)
		sample := drainDiagnostic(t, svc)
		assert.Equal(t, body, sample.Body)
		assert.NotContains(t, string(sample.Metadata), "Bearer secret")
		var metadata DiagnosticMetadata
		require.NoError(t, common.Unmarshal(sample.Metadata, &metadata))
		input := DiagnosticSampleInput{metadata, sample.Body}
		for _, src := range []operation_setting.ChannelAffinityKeySource{{Type: "gjson", Path: "key"}, {Type: "gjson", Path: "zero"}, {Type: "gjson", Path: "null"}, {Type: "gjson", Path: "obj"}, {Type: "gjson", Path: "absent"}, {Type: "context_int", Key: "token_id"}, {Type: "context_int", Key: "id"}, {Type: "context_string", Key: "group"}, {Type: "request_header", Key: "x-session"}, {Type: "request_header", Key: "Host"}} {
			// Rebuild a separate live request; its body is read by the real online evaluator.
			live := diagnosticRequest(body)
			live.Request.Header = c.Request.Header.Clone()
			live.Set("token_id", int64(4))
			live.Set("group", " group ")
			assert.Equal(t, extractChannelAffinityValue(live, src), EvaluateAffinityValue(input, src).Value, src)
		}
		assert.Equal(t, "not_collected", EvaluateAffinityValue(input, operation_setting.ChannelAffinityKeySource{Type: "request_header", Key: "Authorization"}).Status)
		assert.Equal(t, "type_mismatch", EvaluateAffinityValue(input, operation_setting.ChannelAffinityKeySource{Type: "context_int", Key: "token_id"}).Status)
	})
	t.Run("oversized body retains metadata without reading", func(t *testing.T) {
		c := diagnosticRequest(nil)
		spy := &diagnosticReadSpy{source: strings.NewReader("not read")}
		c.Request.Body = spy
		c.Request.ContentLength = task.MaxBodyBytes + 1
		CaptureAffinityDiagnostic(c, "model", nil, false)
		assert.Zero(t, spy.reads)
		sample := drainDiagnostic(t, svc)
		assert.Equal(t, "over_limit", sample.BodyStatus)
		assert.Empty(t, sample.Body)
	})
	t.Run("normalized websocket body each logical request", func(t *testing.T) {
		for _, locked := range []bool{false, true} {
			c := diagnosticRequest(nil)
			CaptureAffinityDiagnostic(c, "model", []byte(`{"prompt_cache_key":"session"}`), locked)
			CaptureAffinityDiagnostic(c, "model", nil, false)
			sample := drainDiagnostic(t, svc)
			assert.Equal(t, "responses_ws_normalized", sample.BodyKind)
			var m DiagnosticMetadata
			require.NoError(t, common.Unmarshal(sample.Metadata, &m))
			if locked {
				assert.Equal(t, "connection_locked", m.AffinityBypass)
			}
		}
	})
	t.Run("unread stream releases capture budget at cleanup", func(t *testing.T) {
		c := diagnosticRequest([]byte(`{}`))
		CaptureAffinityDiagnostic(c, "model", nil, false)
		FinishAffinityDiagnostic(c)
		sample := drainDiagnostic(t, svc)
		assert.Equal(t, "read_failed", sample.BodyStatus)
		assert.Empty(t, svc.slots)
	})
	t.Run("queue and memory exhaustion never block business", func(t *testing.T) {
		require.True(t, svc.memory.TryAcquire(diagnosticMemoryBudget))
		defer svc.memory.Release(diagnosticMemoryBudget)
		c := diagnosticRequest([]byte(`{}`))
		CaptureAffinityDiagnostic(c, "model", nil, false)
		got, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		assert.Equal(t, `{}`, string(got))
		assert.Empty(t, svc.queue)
		assert.Empty(t, svc.slots)
	})
}

func TestAffinityDiagnosticQuotaConcurrencyAndDelete(t *testing.T) {
	db := diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	task, err := svc.Create(context.Background(), DiagnosticCreate{UserID: 17, MaxCount: 10, MaxBytes: 1024}, 1)
	require.NoError(t, err)
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			if svc.admit(*task, 150) {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	assert.EqualValues(t, 6, admitted.Load())
	assert.EqualValues(t, 900, svc.localBytes)
	require.NoError(t, svc.Stop(context.Background(), task.ID, false))
	next := diagnosticTask(t, svc)
	start := make(chan struct{})
	var saved atomic.Int64
	for range 8 {
		wg.Go(func() {
			<-start
			if model.SaveAffinityDiagnosticSample(context.Background(), &model.AffinityDiagnosticSample{TaskID: next.ID, SizeBytes: 1}) == nil {
				saved.Add(1)
			}
		})
	}
	wg.Go(func() { <-start; _ = svc.Stop(context.Background(), next.ID, true) })
	close(start)
	wg.Wait()
	require.NoError(t, svc.Stop(context.Background(), next.ID, true))
	require.ErrorIs(t, model.SaveAffinityDiagnosticSample(context.Background(), &model.AffinityDiagnosticSample{TaskID: next.ID, SizeBytes: 1}), model.ErrDiagnosticUnavailable)
	require.NoError(t, model.CleanupAffinityDiagnostics(context.Background()))
	var count int64
	require.NoError(t, db.Model(&model.AffinityDiagnosticSample{}).Where("task_id = ?", next.ID).Count(&count).Error)
	assert.Zero(t, count)
}

func TestAffinityDiagnosticRedisGlobalAdmissionAndLoss(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not configured")
	}
	diagnosticTestDB(t, "sqlite")
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	t.Cleanup(func() { client.Del(ctx, diagnosticRedisKey); client.Close() })
	require.NoError(t, client.Del(ctx, diagnosticRedisKey).Err())
	a := diagnosticTestService(t, client)
	b := NewAffinityDiagnosticService(client)
	task, err := a.Create(ctx, DiagnosticCreate{UserID: 17, MaxCount: 10, MaxBytes: 1024}, 1)
	require.NoError(t, err)
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := range 30 {
		svc := a
		if i%2 == 0 {
			svc = b
		}
		wg.Go(func() {
			if svc.admit(*task, 150) {
				admitted.Add(1)
			}
		})
	}
	wg.Wait()
	// The production timeout deliberately permits lost replies under contention.
	assert.LessOrEqual(t, admitted.Load(), int64(6))
	for range 8 {
		_, err := client.Eval(ctx, diagnosticAdmissionScript, []string{diagnosticRedisKey}, task.ID, 150, task.MaxCount, task.MaxBytes).Result()
		require.NoError(t, err)
	}
	count, err := client.HGet(ctx, diagnosticRedisKey, "count").Int64()
	require.NoError(t, err)
	assert.EqualValues(t, 6, count)
	a.refresh()
	var stopped model.AffinityDiagnosticTask
	require.NoError(t, model.DB.First(&stopped, task.ID).Error)
	assert.Equal(t, "byte_limit", stopped.Reason)
	next := diagnosticTask(t, a)
	require.True(t, a.admit(*next, 100))
	require.NoError(t, client.Del(ctx, diagnosticRedisKey).Err())
	assert.False(t, b.admit(*next, 100))
	a.refresh()
	require.NoError(t, model.DB.Model(&model.AffinityDiagnosticTask{}).Where("id = ?", next.ID).Scan(&stopped).Error)
	assert.Equal(t, "redis_state_lost", stopped.Reason)
	assert.True(t, stopped.StatsIncomplete)
	// A changed run identity is terminal even if a key survived restart.
	next = diagnosticTask(t, a)
	require.NoError(t, model.DB.Model(next).Update("redis_run_id", "old-run").Error)
	a.refresh()
	var changed model.AffinityDiagnosticTask
	require.NoError(t, model.DB.First(&changed, next.ID).Error)
	assert.Equal(t, "redis_state_lost", changed.Reason)
}

func TestAffinityDiagnosticReaderFailurePreservesBusinessError(t *testing.T) {
	diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	diagnosticTask(t, svc)
	expected := errors.New("source failed")
	c := diagnosticRequest(nil)
	c.Request.Body = io.NopCloser(diagnosticErrorReader{expected})
	CaptureAffinityDiagnostic(c, "model", nil, false)
	_, err := io.ReadAll(c.Request.Body)
	assert.ErrorIs(t, err, expected)
	sample := drainDiagnostic(t, svc)
	assert.Equal(t, "read_failed", sample.BodyStatus)
}

type diagnosticErrorReader struct{ err error }

func (r diagnosticErrorReader) Read([]byte) (int, error) { return 0, r.err }

func BenchmarkAffinityDiagnostic(b *testing.B) {
	for _, mode := range []string{"off", "other_user", "memory_1KiB", "memory_1MiB", "disk_1MiB", "over_limit", "queue_full"} {
		b.Run(mode, func(b *testing.B) {
			svc := NewAffinityDiagnosticService(nil)
			task := model.AffinityDiagnosticTask{ID: 1, UserID: 17, CaptureBody: true, Deadline: time.Now().Add(time.Hour).UnixMilli(), MaxBodyBytes: 1 << 20, MaxCount: 1 << 60, MaxBytes: 1 << 60}
			svc.localID = 1
			svc.snapshot.Store(&diagnosticSnapshot{task: task, validUntil: time.Now().Add(time.Hour)})
			old := affinityDiagnostics.Swap(svc)
			defer affinityDiagnostics.Store(old)
			if mode == "off" {
				affinityDiagnostics.Store(nil)
			}
			size := 1024
			if mode == "memory_1MiB" || mode == "disk_1MiB" {
				size = (1 << 20) - 16
			}
			if mode == "over_limit" {
				size = 2 << 20
			}
			oldConfig := common.GetDiskCacheConfig()
			common.SetDiskCacheConfig(common.DiskCacheConfig{Enabled: mode == "disk_1MiB", ThresholdMB: 0, MaxSizeMB: 64, Path: b.TempDir()})
			defer common.SetDiskCacheConfig(oldConfig)
			if mode == "queue_full" {
				for range 32 {
					svc.slots <- struct{}{}
				}
			}
			body := []byte(fmt.Sprintf(`{"v":"%s"}`, strings.Repeat("x", size)))
			storage, err := common.CreateBodyStorage(body)
			if err != nil {
				b.Fatal(err)
			}
			defer storage.Close()
			c := diagnosticRequest(nil)
			c.Set(common.KeyBodyStorage, storage)
			if mode == "other_user" {
				c.Set("id", 18)
			}
			latencies := make([]int64, 0, 100000)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				c.Keys[diagnosticObservedKey] = nil
				delete(c.Keys, diagnosticObservedKey)
				started := time.Now()
				CaptureAffinityDiagnostic(c, "model", nil, false)
				if len(latencies) < cap(latencies) {
					latencies = append(latencies, time.Since(started).Nanoseconds())
				}
				select {
				case item := <-svc.queue:
					svc.memory.Release(item.memory)
					<-svc.slots
				default:
				}
			}
			b.StopTimer()
			slices.Sort(latencies)
			if len(latencies) > 0 {
				b.ReportMetric(float64(latencies[(len(latencies)-1)*95/100]), "p95-ns")
				b.ReportMetric(float64(latencies[(len(latencies)-1)*99/100]), "p99-ns")
			}
		})
	}
}

func TestAffinityDiagnosticDiskSnapshotAndHeaderBounds(t *testing.T) {
	diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	diagnosticTask(t, svc)
	old := common.GetDiskCacheConfig()
	common.SetDiskCacheConfig(common.DiskCacheConfig{Enabled: true, ThresholdMB: 0, MaxSizeMB: 64, Path: t.TempDir()})
	t.Cleanup(func() { common.SetDiskCacheConfig(old) })
	body := []byte(" {\"key\":\"a\"} \n")
	storage, err := common.CreateBodyStorage(body)
	require.NoError(t, err)
	require.True(t, storage.IsDisk())
	c := diagnosticRequest(nil)
	c.Set(common.KeyBodyStorage, storage)
	c.Request.Header.Set("X-Large", strings.Repeat("x", 4097))
	c.Request.Header["X-Multi"] = []string{"one", "two"}
	_, err = storage.Seek(4, io.SeekStart)
	require.NoError(t, err)
	CaptureAffinityDiagnostic(c, "model", nil, false)
	position, err := storage.Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	assert.EqualValues(t, 4, position)
	require.NoError(t, storage.Close())
	sample := drainDiagnostic(t, svc)
	assert.Equal(t, body, sample.Body)
	var metadata DiagnosticMetadata
	require.NoError(t, common.Unmarshal(sample.Metadata, &metadata))
	assert.Equal(t, "over_limit", metadata.HeaderStates["X-Large"])
	assert.Equal(t, []string{"one", "two"}, metadata.Headers["X-Multi"])
	assert.EqualValues(t, len(sample.Body)+len(sample.Metadata), sample.SizeBytes)
}

func TestAffinityDiagnosticExcludedHeadersOmitDuplicateMetadata(t *testing.T) {
	diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	_, err := svc.Create(context.Background(), DiagnosticCreate{UserID: 17, ExcludedHeaders: []string{"user-agent", "host", "x-private"}}, 1)
	require.NoError(t, err)
	c := diagnosticRequest(nil)
	c.Request.Host = "private-host"
	c.Request.Header.Set("User-Agent", "private-agent")
	c.Request.Header.Set("X-Private", "private-value")
	CaptureAffinityDiagnostic(c, "model", nil, false)
	sample := drainDiagnostic(t, svc)
	assert.NotContains(t, string(sample.Metadata), "private-host")
	assert.NotContains(t, string(sample.Metadata), "private-agent")
	assert.NotContains(t, string(sample.Metadata), "private-value")
	var metadata DiagnosticMetadata
	require.NoError(t, common.Unmarshal(sample.Metadata, &metadata))
	assert.Equal(t, "not_collected", metadata.HeaderStates["User-Agent"])
	assert.Equal(t, "not_collected", metadata.HeaderStates["X-Private"])
}

func TestAffinityDiagnosticQueueAndWriteFailuresRetainQuota(t *testing.T) {
	db := diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	task, err := svc.Create(context.Background(), DiagnosticCreate{UserID: 17}, 1)
	require.NoError(t, err)
	for range 33 {
		CaptureAffinityDiagnostic(diagnosticRequest(nil), "model", nil, false)
	}
	assert.Len(t, svc.queue, 32)
	assert.EqualValues(t, 32, svc.localCount)
	svc.flushCounters(context.Background())
	var state model.AffinityDiagnosticTask
	require.NoError(t, db.First(&state, task.ID).Error)
	assert.EqualValues(t, 1, state.QueueDropped)
	// A failed SQL insert rolls back saved counters without refunding admission.
	item := <-svc.queue
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("diagnostic_fail_insert", func(tx *gorm.DB) {
		if strings.HasSuffix(tx.Statement.Table, "affinity_diagnostic_samples") {
			tx.AddError(errors.New("database unavailable"))
		}
	}))
	require.Error(t, model.SaveAffinityDiagnosticSample(context.Background(), item.sample))
	require.NoError(t, db.Callback().Create().Remove("diagnostic_fail_insert"))
	svc.drop(task.ID, "write", false)
	svc.memory.Release(item.memory)
	<-svc.slots
	svc.flushCounters(context.Background())
	require.NoError(t, db.First(&state, task.ID).Error)
	assert.EqualValues(t, 32, state.RequestedCount)
	assert.Zero(t, state.SavedCount)
	assert.EqualValues(t, 1, state.WriteDropped)
	// Accepted queued samples can drain after stop, but never after deletion.
	require.NoError(t, svc.Stop(context.Background(), task.ID, false))
	drainDiagnostic(t, svc)
	require.NoError(t, svc.Stop(context.Background(), task.ID, true))
	item = <-svc.queue
	assert.ErrorIs(t, model.SaveAffinityDiagnosticSample(context.Background(), item.sample), model.ErrDiagnosticUnavailable)
}

func TestAffinityDiagnosticFirstPinnedSelectionDoesNotResampleRetry(t *testing.T) {
	db := diagnosticTestDB(t, "sqlite")
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	svc := diagnosticTestService(t, nil)
	diagnosticTask(t, svc)
	old := common.MemoryCacheEnabled
	common.MemoryCacheEnabled = false
	t.Cleanup(func() { common.MemoryCacheEnabled = old })
	channel := model.Channel{Name: "diagnostic-fixed", Key: "not-a-real-key", Type: 1, Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	body := []byte(`{"model":"before","prompt_cache_key":"first"}`)
	c := diagnosticRequest(body)
	GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken})
	retry := &RetryParam{Ctx: c, ModelName: "before", TokenGroup: "default"}
	selected, _, selectErr := SelectChannelForRequest(c, "before", retry)
	require.Nil(t, selectErr)
	assert.Equal(t, channel.Id, selected.Id)
	got, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	assert.Equal(t, body, got)
	c.Request.Body = io.NopCloser(strings.NewReader(`{"model":"after"}`))
	retry.SetRetry(0)
	_, _, selectErr = SelectChannelForRequest(c, "after", retry)
	require.Nil(t, selectErr)
	sample := drainDiagnostic(t, svc)
	assert.Equal(t, body, sample.Body)
	assert.Empty(t, svc.queue)
	var metadata DiagnosticMetadata
	require.NoError(t, common.Unmarshal(sample.Metadata, &metadata))
	assert.Equal(t, "fixed_channel", metadata.AffinityBypass)
}

func TestAffinityDiagnosticShutdownReleasesQueuedPayloads(t *testing.T) {
	db := diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	task := diagnosticTask(t, svc)
	for range 4 {
		CaptureAffinityDiagnostic(diagnosticRequest(nil), "model", []byte(`{"x":1}`), false)
	}
	svc.Close()
	assert.Empty(t, svc.queue)
	assert.Empty(t, svc.slots)
	require.True(t, svc.memory.TryAcquire(diagnosticMemoryBudget))
	svc.memory.Release(diagnosticMemoryBudget)
	var state model.AffinityDiagnosticTask
	require.NoError(t, db.First(&state, task.ID).Error)
	assert.EqualValues(t, 4, state.WriteDropped)
	assert.True(t, state.StatsIncomplete)
	assert.Nil(t, DiagnosticService())
	assert.False(t, svc.admit(*task, 1))
}

func TestAffinityDiagnosticStoppedCreationCannotActivate(t *testing.T) {
	diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	task := diagnosticTask(t, svc)
	require.NoError(t, svc.Stop(context.Background(), task.ID, false))
	assert.ErrorIs(t, model.ActivateAffinityDiagnostic(context.Background(), task.ID), model.ErrDiagnosticUnavailable)
}

func TestAffinityDiagnosticDelayedRedisInitializationCannotReplaceNewerTask(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not configured")
	}
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	ctx := context.Background()
	key := diagnosticRedisKey + ":initialization-test"
	t.Cleanup(func() { client.Del(ctx, key); client.Close() })
	require.NoError(t, client.Del(ctx, key).Err())
	deadline := time.Now().Add(time.Minute).UnixMilli()
	result, err := client.Eval(ctx, diagnosticInitializeScript, []string{key}, 20, deadline).Int64()
	require.NoError(t, err)
	require.EqualValues(t, 1, result)
	result, err = client.Eval(ctx, diagnosticInitializeScript, []string{key}, 19, deadline).Int64()
	require.NoError(t, err)
	assert.Zero(t, result)
	assert.Equal(t, "20", client.HGet(ctx, key, "id").Val())
}

func TestAffinityDiagnosticSlowWriterIsBoundedAndStopDrains(t *testing.T) {
	db := diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	task, err := svc.Create(context.Background(), DiagnosticCreate{UserID: 17}, 1)
	require.NoError(t, err)
	entered, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	require.NoError(t, db.Callback().Create().Before("gorm:create").Register("diagnostic_slow_insert", func(tx *gorm.DB) {
		if strings.HasSuffix(tx.Statement.Table, "affinity_diagnostic_samples") {
			once.Do(func() { close(entered); <-release })
		}
	}))
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan struct{})
	go func() { defer close(finished); svc.writer(ctx) }()
	t.Cleanup(func() { cancel(); <-finished; db.Callback().Create().Remove("diagnostic_slow_insert"); svc.Close() })
	CaptureAffinityDiagnostic(diagnosticRequest(nil), "model", nil, false)
	<-entered
	for range 40 {
		CaptureAffinityDiagnostic(diagnosticRequest(nil), "model", nil, false)
	}
	assert.Len(t, svc.slots, 32)
	assert.Len(t, svc.queue, 31)
	// Release the DB transaction before Stop takes its own lock.
	close(release)
	require.NoError(t, svc.Stop(context.Background(), task.ID, false))
	require.Eventually(t, func() bool {
		var state model.AffinityDiagnosticTask
		return db.First(&state, task.ID).Error == nil && state.SavedCount == 32
	}, 5*time.Second, 10*time.Millisecond)
	svc.flushCounters(context.Background())
	var state model.AffinityDiagnosticTask
	require.NoError(t, db.First(&state, task.ID).Error)
	assert.EqualValues(t, 9, state.QueueDropped)
	assert.EqualValues(t, 32, state.RequestedCount)
}

func TestAffinityDiagnosticCleanupFailureKeepsReservation(t *testing.T) {
	db := diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	task := diagnosticTask(t, svc)
	CaptureAffinityDiagnostic(diagnosticRequest(nil), "model", []byte(`{"x":1}`), false)
	drainDiagnostic(t, svc)
	require.NoError(t, svc.Stop(context.Background(), task.ID, true))
	require.NoError(t, db.Callback().Delete().Before("gorm:delete").Register("diagnostic_cleanup_failure", func(tx *gorm.DB) { tx.AddError(errors.New("storage unavailable")) }))
	require.Error(t, model.CleanupAffinityDiagnostics(context.Background()))
	require.NoError(t, db.Callback().Delete().Remove("diagnostic_cleanup_failure"))
	var state model.AffinityDiagnosticTask
	require.NoError(t, db.First(&state, task.ID).Error)
	assert.True(t, state.CleanupError)
	var ctl model.AffinityDiagnosticControl
	require.NoError(t, db.First(&ctl, 1).Error)
	assert.Equal(t, task.ReservedBytes, ctl.ReservedBytes)
	require.NoError(t, model.CleanupAffinityDiagnostics(context.Background()))
	require.NoError(t, db.First(&ctl, 1).Error)
	assert.Zero(t, ctl.ReservedBytes)
}

func TestAffinityDiagnosticRedisPauseRecoveryAndRestart(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not configured")
	}
	diagnosticTestDB(t, "sqlite")
	ctx := context.Background()
	client := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	t.Cleanup(func() { client.Del(ctx, diagnosticRedisKey); client.Close() })
	require.NoError(t, client.Del(ctx, diagnosticRedisKey).Err())
	svc := diagnosticTestService(t, client)
	task := diagnosticTask(t, svc)
	unavailable := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	require.NoError(t, unavailable.Close())
	svc.redis = unavailable
	svc.refresh()
	assert.True(t, svc.RedisPaused())
	assert.Nil(t, svc.snapshot.Load())
	request := diagnosticRequest([]byte(`{"v":"unchanged"}`))
	CaptureAffinityDiagnostic(request, "model", nil, false)
	body, err := io.ReadAll(request.Request.Body)
	require.NoError(t, err)
	assert.Equal(t, `{"v":"unchanged"}`, string(body))
	assert.Empty(t, svc.queue)
	svc.redis = client
	svc.refresh()
	assert.False(t, svc.RedisPaused())
	require.NotNil(t, svc.snapshot.Load())
	assert.Equal(t, task.ID, svc.snapshot.Load().task.ID)
	if container := os.Getenv("TEST_REDIS_RESTART_CONTAINER"); container != "" {
		out, err := exec.Command("docker", "restart", container).CombinedOutput()
		require.NoError(t, err, string(out))
		require.Eventually(t, func() bool { return client.Ping(ctx).Err() == nil }, 10*time.Second, 50*time.Millisecond)
		svc.refresh()
		var stopped model.AffinityDiagnosticTask
		require.NoError(t, model.DB.First(&stopped, task.ID).Error)
		assert.Equal(t, "redis_state_lost", stopped.Reason)
		assert.True(t, stopped.StatsIncomplete)
		assert.Nil(t, svc.snapshot.Load())
	}
}

// A synthetic HTTP routing workload, not provider latency or production capacity.
// The normal body cache, public selector, capture queue and SQLite writer all run.
func TestAffinityDiagnosticHTTPWorkload(t *testing.T) {
	if os.Getenv("TEST_DIAGNOSTIC_LOAD") != "1" {
		t.Skip("TEST_DIAGNOSTIC_LOAD not configured")
	}
	for _, mode := range []string{"off", "other_user", "memory_1KiB", "memory_1MiB", "disk_1MiB", "over_limit"} {
		t.Run(mode, func(t *testing.T) {
			db := diagnosticTestDB(t, "sqlite")
			require.NoError(t, db.AutoMigrate(&model.Channel{}))
			channel := model.Channel{Name: "load-fixed", Key: "fixture", Type: 1, Status: common.ChannelStatusEnabled}
			require.NoError(t, db.Create(&channel).Error)
			oldCache, oldConfig := common.MemoryCacheEnabled, common.GetDiskCacheConfig()
			common.MemoryCacheEnabled = false
			common.SetDiskCacheConfig(common.DiskCacheConfig{Enabled: mode == "disk_1MiB", ThresholdMB: 0, MaxSizeMB: 64, Path: t.TempDir()})
			t.Cleanup(func() { common.MemoryCacheEnabled = oldCache; common.SetDiskCacheConfig(oldConfig) })
			svc := diagnosticTestService(t, nil)
			svc.Start()
			t.Cleanup(svc.Close)
			var task *model.AffinityDiagnosticTask
			if mode != "off" {
				task = diagnosticTask(t, svc)
			}
			size := 1024
			if mode == "memory_1MiB" || mode == "disk_1MiB" {
				size = (1 << 20) - 16
			}
			if mode == "over_limit" {
				size = 2 << 20
			}
			body := []byte(fmt.Sprintf(`{"v":"%s"}`, strings.Repeat("x", size)))
			engine := gin.New()
			engine.POST("/v1/responses", func(c *gin.Context) {
				c.Set("id", 17)
				if mode == "other_user" {
					c.Set("id", 18)
				}
				storage, err := common.GetBodyStorage(c)
				if err != nil {
					c.Status(500)
					return
				}
				defer storage.Close()
				defer FinishAffinityDiagnostic(c)
				GetChannelConstraints(c).AddPin(dto.ChannelPin{ChannelId: channel.Id, Source: dto.PinSourceToken, Rank: dto.PinRankToken})
				_, _, selectionErr := SelectChannelForRequest(c, "model", &RetryParam{Ctx: c, ModelName: "model", TokenGroup: "default"})
				if selectionErr != nil {
					c.Status(500)
					return
				}
				actual, err := storage.Bytes()
				if err != nil || !bytes.Equal(body, actual) {
					c.Status(500)
					return
				}
				c.Status(204)
			})
			latencies := make([]int64, 100)
			started := time.Now()
			for i := range latencies {
				request := httptest.NewRequest("POST", "/v1/responses", bytes.NewReader(body))
				request.Header.Set("Content-Type", "application/json")
				recorder := httptest.NewRecorder()
				before := time.Now()
				engine.ServeHTTP(recorder, request)
				latencies[i] = time.Since(before).Nanoseconds()
				require.Equal(t, 204, recorder.Code)
			}
			elapsed := time.Since(started)
			require.Eventually(t, func() bool { return len(svc.slots) == 0 }, 10*time.Second, 10*time.Millisecond)
			svc.flushCounters(context.Background())
			slices.Sort(latencies)
			var state model.AffinityDiagnosticTask
			if task != nil {
				require.NoError(t, db.First(&state, task.ID).Error)
			}
			t.Logf("mode=%s requests=100 requests_per_second=%.1f p95_ms=%.3f p99_ms=%.3f admitted=%d saved=%d saved_bytes=%d queue_drops=%d write_drops=%d", mode, 100/elapsed.Seconds(), float64(latencies[94])/1e6, float64(latencies[98])/1e6, state.RequestedCount, state.SavedCount, state.SavedBytes, state.QueueDropped, state.WriteDropped)
		})
	}
}

func TestAffinityDiagnosticCompletedObserverReleasesCapturedFields(t *testing.T) {
	diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	diagnosticTask(t, svc)
	c := diagnosticRequest([]byte(`{"value":"original"}`))
	c.Request.Header.Set("X-Session", "retained-only-in-queue")
	CaptureAffinityDiagnostic(c, "model", nil, false)
	observer, ok := c.Request.Body.(*diagnosticBody)
	require.True(t, ok)
	_, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)
	assert.Nil(t, observer.body)
	assert.Empty(t, observer.capture.metadata)
	sample := drainDiagnostic(t, svc)
	assert.Contains(t, string(sample.Metadata), "retained-only-in-queue")
	require.True(t, svc.memory.TryAcquire(diagnosticMemoryBudget))
	svc.memory.Release(diagnosticMemoryBudget)
}

type diagnosticLostReplyConnection struct {
	net.Conn
	enabled   *atomic.Bool
	loseReply bool
}

func (c *diagnosticLostReplyConnection) Write(p []byte) (int, error) {
	if c.enabled.Load() && bytes.Contains(p, []byte("$4\r\neval\r\n")) {
		c.loseReply = true
	}
	return c.Conn.Write(p)
}
func (c *diagnosticLostReplyConnection) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	if c.loseReply && n > 0 {
		c.loseReply = false
		return 0, io.EOF
	}
	return n, err
}
func TestAffinityDiagnosticLostRedisReplyDoesNotRetryAdmission(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("TEST_REDIS_ADDR not configured")
	}
	diagnosticTestDB(t, "sqlite")
	var dropReply atomic.Bool
	client, err := common.NewRedisClient(&redis.Options{Addr: addr, MaxRetries: -1, Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &diagnosticLostReplyConnection{Conn: conn, enabled: &dropReply}, nil
	}})
	require.NoError(t, err)
	ctx := context.Background()
	inspector := redis.NewClient(&redis.Options{Addr: addr, MaxRetries: -1})
	t.Cleanup(func() { inspector.Del(ctx, diagnosticRedisKey); inspector.Close(); client.Close() })
	require.NoError(t, inspector.Del(ctx, diagnosticRedisKey).Err())
	svc := diagnosticTestService(t, client)
	task := diagnosticTask(t, svc)
	dropReply.Store(true)
	assert.False(t, svc.admit(*task, 100))
	count, err := inspector.HGet(ctx, diagnosticRedisKey, "count").Int64()
	require.NoError(t, err)
	assert.EqualValues(t, 1, count)
	assert.Empty(t, svc.queue)
}

func TestAffinityDiagnosticDeadlineStopsWithoutRedis(t *testing.T) {
	db := diagnosticTestDB(t, "sqlite")
	svc := diagnosticTestService(t, nil)
	task := diagnosticTask(t, svc)
	unavailable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1})
	require.NoError(t, unavailable.Close())
	svc.redis = unavailable
	require.NoError(t, db.Model(task).Update("deadline", time.Now().Add(-time.Second).UnixMilli()).Error)
	svc.refresh()
	var stopped model.AffinityDiagnosticTask
	require.NoError(t, db.First(&stopped, task.ID).Error)
	assert.Equal(t, "stopped", stopped.Status)
	assert.Equal(t, "deadline", stopped.Reason)
	assert.Nil(t, svc.snapshot.Load())
	active, err := model.ActiveAffinityDiagnostic(context.Background())
	require.NoError(t, err)
	assert.Nil(t, active)
}
