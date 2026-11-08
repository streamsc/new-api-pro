package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/go-redis/redis/v8"
	"golang.org/x/sync/semaphore"
	"gorm.io/gorm"
)

const diagnosticRedisKey = "new-api:affinity_diagnostic:v1"
const diagnosticMemoryBudget int64 = 8 << 20
const diagnosticMetadataLimit int64 = 64 << 10
const diagnosticSnapshotTTL = 2 * time.Second

// One Lua call atomically spends count AND bytes. Lost replies are never retried.
// Redis state is never reconstructed from saved samples or an old SQL snapshot.
const diagnosticAdmissionScript = `
local id=redis.call('HGET',KEYS[1],'id')
if id~=ARGV[1] then return {-1,0,0} end
local count=tonumber(redis.call('HGET',KEYS[1],'count'))
local bytes=tonumber(redis.call('HGET',KEYS[1],'bytes'))
if redis.call('HGET',KEYS[1],'state')~='running' then return {0,count,bytes} end
local t=redis.call('TIME'); local now=t[1]*1000+math.floor(t[2]/1000)
local reason=''
if now>=tonumber(redis.call('HGET',KEYS[1],'deadline')) then reason='deadline'
elseif count>=tonumber(ARGV[3]) then reason='count_limit'
elseif bytes+tonumber(ARGV[2])>tonumber(ARGV[4]) then reason='byte_limit' end
if reason~='' then redis.call('HSET',KEYS[1],'state',reason); return {0,count,bytes} end
count=count+1; bytes=bytes+tonumber(ARGV[2])
redis.call('HSET',KEYS[1],'count',count,'bytes',bytes)
if count>=tonumber(ARGV[3]) then redis.call('HSET',KEYS[1],'state','count_limit')
elseif bytes>=tonumber(ARGV[4]) then redis.call('HSET',KEYS[1],'state','byte_limit') end
return {1,count,bytes}
`

// A delayed creator must never replace a newer task's admission state.
const diagnosticInitializeScript = `
local current=redis.call('HGET',KEYS[1],'id')
if current and tonumber(current)>=tonumber(ARGV[1]) then return 0 end
redis.call('DEL',KEYS[1])
redis.call('HSET',KEYS[1],'id',ARGV[1],'count',0,'bytes',0,'state','running','deadline',ARGV[2])
redis.call('EXPIRE',KEYS[1],3600)
return 1
`
const diagnosticStopScript = `if redis.call('HGET',KEYS[1],'id')==ARGV[1] then redis.call('HSET',KEYS[1],'state',ARGV[2]) end return 1`

type DiagnosticCreate struct {
	UserID          int      `json:"user_id"`
	DurationSeconds int64    `json:"duration_seconds"`
	MaxCount        int64    `json:"max_count"`
	MaxBodyBytes    int64    `json:"max_body_bytes"`
	MaxBytes        int64    `json:"max_bytes"`
	CaptureBody     bool     `json:"capture_body"`
	ExcludedHeaders []string `json:"excluded_headers"`
}

type diagnosticSnapshot struct {
	task       model.AffinityDiagnosticTask
	validUntil time.Time
}
type diagnosticCounters struct {
	requested, bytes, queue, write, admission int64
	incomplete                                bool
	updated                                   time.Time
}
type diagnosticItem struct {
	sample *model.AffinityDiagnosticSample
	memory int64
}

type AffinityDiagnosticService struct {
	ownsRedis                       bool
	redisPaused                     atomic.Bool
	closed                          atomic.Bool
	redis                           *redis.Client
	snapshot                        atomic.Pointer[diagnosticSnapshot]
	memory                          *semaphore.Weighted
	slots                           chan struct{}
	queue                           chan diagnosticItem
	mu                              sync.Mutex
	counters                        map[int64]*diagnosticCounters
	localCount, localBytes, localID int64
	localReason                     string
	cancel                          context.CancelFunc
	wg                              sync.WaitGroup
}

var affinityDiagnostics atomic.Pointer[AffinityDiagnosticService]

func NewAffinityDiagnosticService(client *redis.Client) *AffinityDiagnosticService {
	return &AffinityDiagnosticService{redis: client, memory: semaphore.NewWeighted(diagnosticMemoryBudget), slots: make(chan struct{}, 32), queue: make(chan diagnosticItem, 32), counters: make(map[int64]*diagnosticCounters)}
}

func StartAffinityDiagnostics() {
	var client *redis.Client
	if common.RedisEnabled {
		options, err := redis.ParseURL(os.Getenv("REDIS_CONN_STRING"))
		if err != nil {
			common.SysError("Affinity diagnostic Redis configuration failed")
			return
		}
		options.MaxRetries = -1
		options.PoolSize = 4
		client, err = common.NewRedisClient(options)
		if err != nil {
			common.SysError("Affinity diagnostic Redis configuration failed")
			return
		}
	}
	if os.Getenv("GATEWAY_REPLICA_MODE") == "cluster" && client == nil {
		common.SysError("Affinity diagnostics require Redis in cluster mode")
		return
	}
	svc := NewAffinityDiagnosticService(client)
	svc.ownsRedis = client != nil
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	err := model.InitAffinityDiagnostics(ctx, client == nil)
	cancel()
	if err != nil {
		common.SysError("Affinity diagnostics initialization failed")
		if client != nil {
			_ = client.Close()
		}
		return
	}
	svc.Start()
}
func (s *AffinityDiagnosticService) Start() {
	affinityDiagnostics.Store(s)
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.refresh()
	s.wg.Go(func() { s.writer(ctx) })
	s.wg.Go(func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.refresh()
			}
		}
	})
}
func (s *AffinityDiagnosticService) Close() {
	s.mu.Lock()
	s.closed.Store(true)
	s.mu.Unlock()
	affinityDiagnostics.CompareAndSwap(s, nil)
	s.snapshot.Store(nil)
	if s.cancel != nil {
		s.cancel()
		s.wg.Wait()
	}
	// Enqueue is fenced by closed under mu. Release every queued payload even
	// when shutdown interrupts the writer; lost admissions remain spent.
	for {
		select {
		case item := <-s.queue:
			s.drop(item.sample.TaskID, "write", true)
			s.memory.Release(item.memory)
			<-s.slots
		default:
			ctx, cancel := diagnosticContext()
			s.flushCounters(ctx)
			cancel()
			if s.ownsRedis {
				_ = s.redis.Close()
			}
			return
		}
	}
}

func diagnosticContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 2*time.Second)
}
func (s *AffinityDiagnosticService) redisRunID(ctx context.Context) (string, error) {
	info, err := s.redis.Info(ctx, "server").Result()
	if err != nil {
		return "", err
	}
	for line := range strings.SplitSeq(info, "\n") {
		if id, ok := strings.CutPrefix(strings.TrimSpace(line), "run_id:"); ok && id != "" {
			return id, nil
		}
	}
	return "", errors.New("Redis server identity is unavailable")
}

func (s *AffinityDiagnosticService) Create(ctx context.Context, in DiagnosticCreate, admin int) (*model.AffinityDiagnosticTask, error) {
	if in.DurationSeconds == 0 {
		in.DurationSeconds = 600
	}
	if in.MaxCount == 0 {
		in.MaxCount = 100
	}
	if in.MaxBodyBytes == 0 {
		in.MaxBodyBytes = 1 << 20
	}
	if in.MaxBytes == 0 {
		in.MaxBytes = 20 << 20
	}
	if in.UserID <= 0 || in.DurationSeconds < 1 || in.DurationSeconds > 600 || in.MaxCount < 1 || in.MaxCount > 100 || in.MaxBodyBytes < 1 || in.MaxBodyBytes > 1<<20 || in.MaxBytes < 1024 || in.MaxBytes > 20<<20 || len(in.ExcludedHeaders) > 32 {
		return nil, errors.New("Invalid diagnostic limits")
	}
	for _, key := range in.ExcludedHeaders {
		if len(key) == 0 || len(key) > 128 || strings.ContainsAny(key, " \t\r\n:") {
			return nil, errors.New("Invalid excluded header name")
		}
	}
	excluded, err := common.Marshal(in.ExcludedHeaders)
	if err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	task := &model.AffinityDiagnosticTask{UserID: in.UserID, AdminID: admin, StartedAt: now, Deadline: now + in.DurationSeconds*1000, ExpiresAt: now + 3600000, Status: "starting", CaptureBody: in.CaptureBody, MaxCount: in.MaxCount, MaxBodyBytes: in.MaxBodyBytes, MaxBytes: in.MaxBytes, ReservedBytes: max(in.MaxBytes, 64<<10), ExtraExcludedHeaders: string(excluded)}
	if s.redis != nil {
		task.RedisRunID, err = s.redisRunID(ctx)
		if err != nil {
			return nil, errors.New("Diagnostic Redis is unavailable")
		}
	}
	if err = model.CreateAffinityDiagnostic(ctx, task); err != nil {
		return nil, err
	}
	if s.redis != nil {
		var initialized int64
		initialized, err = s.redis.Eval(ctx, diagnosticInitializeScript, []string{diagnosticRedisKey}, task.ID, task.Deadline).Int64()
		if err == nil && initialized != 1 {
			err = model.ErrDiagnosticUnavailable
		}
		if err != nil {
			model.StopAffinityDiagnostic(ctx, task.ID, "redis_unavailable", false, true)
			return nil, errors.New("Diagnostic Redis is unavailable")
		}
	} else {
		s.mu.Lock()
		s.localID = task.ID
		s.localCount = 0
		s.localBytes = 0
		s.localReason = ""
		s.mu.Unlock()
	}
	if err = model.ActivateAffinityDiagnostic(ctx, task.ID); err != nil {
		return nil, err
	}
	task.Status = "running"
	s.snapshot.Store(&diagnosticSnapshot{task: *task, validUntil: time.Now().Add(diagnosticSnapshotTTL)})
	return task, nil
}

func (s *AffinityDiagnosticService) Stop(ctx context.Context, id int64, remove bool) error {
	s.snapshot.Store(nil)
	// Persist first: even if Redis is down, no refresh may keep the task alive.
	reason := "manual_stop"
	if remove {
		reason = "manual_delete"
	}
	if err := model.StopAffinityDiagnostic(ctx, id, reason, remove, false); err != nil {
		return err
	}
	if s.redis != nil {
		rctx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		defer cancel()
		s.redis.Eval(rctx, diagnosticStopScript, []string{diagnosticRedisKey}, id, reason)
	}
	s.mu.Lock()
	if s.localID == id {
		s.localReason = reason
	}
	s.mu.Unlock()
	return nil
}

func (s *AffinityDiagnosticService) refresh() {
	started := time.Now()
	ctx, cancel := diagnosticContext()
	defer cancel()
	defer func() {
		s.flushCounters(ctx)
		if err := model.CleanupAffinityDiagnostics(ctx); err != nil {
			common.SysError("Affinity diagnostic cleanup failed")
		}
	}()
	task, err := model.ActiveAffinityDiagnostic(ctx)
	if err != nil {
		s.snapshot.Store(nil)
		return
	}
	if task == nil {
		s.snapshot.Store(nil)
	} else {
		reason := ""
		incomplete := false
		if task.Status == "starting" {
			s.snapshot.Store(nil)
			if started.UnixMilli()-task.StartedAt > 5000 {
				reason = "initialization_failed"
				incomplete = true
			}
		} else if task.Status != "running" {
			s.snapshot.Store(nil)
		} else {
			// The SQL deadline remains authoritative while Redis is unavailable.
			// Do not let an outage keep the global active task occupied.
			if started.UnixMilli() >= task.Deadline {
				reason = "deadline"
			} else if s.redis != nil {
				runID, redisErr := s.redisRunID(ctx)
				if redisErr != nil {
					s.redisPaused.Store(true)
					s.snapshot.Store(nil)
					return
				}
				values, redisErr := s.redis.HGetAll(ctx, diagnosticRedisKey).Result()
				if redisErr != nil {
					s.redisPaused.Store(true)
					s.snapshot.Store(nil)
					return
				}
				s.redisPaused.Store(false)
				if runID != task.RedisRunID || values["id"] != strconv.FormatInt(task.ID, 10) {
					reason = "redis_state_lost"
					incomplete = true
				} else {
					count, e1 := strconv.ParseInt(values["count"], 10, 64)
					bytes, e2 := strconv.ParseInt(values["bytes"], 10, 64)
					if e1 != nil || e2 != nil || count < task.RequestedCount || bytes < task.RequestedBytes {
						reason = "redis_state_lost"
						incomplete = true
					} else {
						s.recordQuota(task.ID, count, bytes)
						if values["state"] != "running" {
							reason = values["state"]
						}
					}
				}
			} else {
				s.mu.Lock()
				if s.localID != task.ID {
					reason = "process_restart"
					incomplete = true
				} else {
					reason = s.localReason
				}
				s.mu.Unlock()
			}
			if reason == "" {
				s.snapshot.Store(&diagnosticSnapshot{task: *task, validUntil: started.Add(diagnosticSnapshotTTL)})
			}
		}
		if reason != "" {
			s.snapshot.Store(nil)
			model.StopAffinityDiagnostic(ctx, task.ID, reason, false, incomplete)
		}
	}
}
func (s *AffinityDiagnosticService) counter(id int64) *diagnosticCounters {
	c := s.counters[id]
	if c == nil {
		c = &diagnosticCounters{}
		s.counters[id] = c
	}
	c.updated = time.Now()
	return c
}
func (s *AffinityDiagnosticService) recordQuota(id, count, bytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counter(id)
	c.requested = max(c.requested, count)
	c.bytes = max(c.bytes, bytes)
}
func (s *AffinityDiagnosticService) drop(id int64, reason string, incomplete bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.counter(id)
	switch reason {
	case "queue":
		c.queue++
	case "write":
		c.write++
	default:
		c.admission++
	}
	c.incomplete = c.incomplete || incomplete
}
func (s *AffinityDiagnosticService) flushCounters(ctx context.Context) {
	// Never hold the capture mutex over SQL. Deltas are subtracted only after commit.
	s.mu.Lock()
	snapshots := make(map[int64]diagnosticCounters, len(s.counters))
	for id, c := range s.counters {
		snapshots[id] = *c
	}
	s.mu.Unlock()
	for id, c := range snapshots {
		updates := map[string]any{"queue_dropped": gorm.Expr("queue_dropped + ?", c.queue), "write_dropped": gorm.Expr("write_dropped + ?", c.write), "admission_dropped": gorm.Expr("admission_dropped + ?", c.admission), "dropped_count": gorm.Expr("dropped_count + ?", c.queue+c.write+c.admission)}
		if c.incomplete {
			updates["stats_incomplete"] = true
		}
		err := model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&model.AffinityDiagnosticTask{}).Where("id = ?", id).Updates(updates).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.AffinityDiagnosticTask{}).Where("id = ? AND requested_count < ?", id, c.requested).Update("requested_count", c.requested).Error; err != nil {
				return err
			}
			return tx.Model(&model.AffinityDiagnosticTask{}).Where("id = ? AND requested_bytes < ?", id, c.bytes).Update("requested_bytes", c.bytes).Error
		})
		if err == nil {
			s.mu.Lock()
			current := s.counters[id]
			if current != nil {
				current.queue -= c.queue
				current.write -= c.write
				current.admission -= c.admission
				if current.updated.Equal(c.updated) {
					delete(s.counters, id)
				}
			}
			s.mu.Unlock()
		}
	}
}
func (s *AffinityDiagnosticService) admit(task model.AffinityDiagnosticTask, size int64) bool {
	if s.closed.Load() {
		return false
	}
	if s.redis != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		result, err := s.redis.Eval(ctx, diagnosticAdmissionScript, []string{diagnosticRedisKey}, task.ID, size, task.MaxCount, task.MaxBytes).Int64Slice()
		if err != nil || len(result) != 3 {
			s.redisPaused.Store(true)
			s.drop(task.ID, "admission", true)
			s.snapshot.Store(nil)
			return false
		}
		s.recordQuota(task.ID, result[1], result[2])
		if result[0] != 1 {
			s.drop(task.ID, "admission", result[0] < 0)
			return false
		}
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.localID != task.ID || s.localReason != "" {
		return false
	}
	if time.Now().UnixMilli() >= task.Deadline {
		s.localReason = "deadline"
		return false
	}
	if s.localCount >= task.MaxCount {
		s.localReason = "count_limit"
		return false
	}
	if s.localBytes+size > task.MaxBytes {
		s.localReason = "byte_limit"
		return false
	}
	s.localCount++
	s.localBytes += size
	c := s.counter(task.ID)
	c.requested = s.localCount
	c.bytes = s.localBytes
	if s.localCount == task.MaxCount {
		s.localReason = "count_limit"
	} else if s.localBytes == task.MaxBytes {
		s.localReason = "byte_limit"
	}
	return true
}
func (s *AffinityDiagnosticService) writer(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case item := <-s.queue:
			writeCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			err := model.SaveAffinityDiagnosticSample(writeCtx, item.sample)
			cancel()
			if err != nil {
				s.drop(item.sample.TaskID, "write", false)
			}
			s.memory.Release(item.memory)
			<-s.slots
		}
	}
}

func DiagnosticService() *AffinityDiagnosticService { return affinityDiagnostics.Load() }

// Body downloads must never be interpreted inline or cached by the management UI.
func DiagnosticDownloadHeaders(header http.Header) {
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Disposition", `attachment; filename="affinity-sample.json"`)
}

func (s *AffinityDiagnosticService) RedisPaused() bool { return s.redisPaused.Load() }
