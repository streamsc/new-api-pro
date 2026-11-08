package model

import (
	"context"
	"errors"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

const DiagnosticStorageBudget int64 = 100 << 20

var ErrDiagnosticUnavailable = errors.New("Diagnostic task is unavailable or expired")
var ErrDiagnosticCapacity = errors.New("Another diagnostic task is active or diagnostic storage is full")

// The fixed row is the cross-database serialization point for starts and capacity.
// A real UPDATE, rather than a SELECT lock alone, also serializes SQLite writers.
type AffinityDiagnosticControl struct {
	ID            int `gorm:"primaryKey;autoIncrement:false"`
	ActiveID      int64
	ReservedBytes int64
	Revision      int64
}

type AffinityDiagnosticTask struct {
	ID                   int64  `json:"id" gorm:"primaryKey"`
	UserID               int    `json:"user_id"`
	AdminID              int    `json:"admin_id"`
	StartedAt            int64  `json:"started_at"`
	Deadline             int64  `json:"deadline"`
	ExpiresAt            int64  `json:"expires_at" gorm:"index"`
	Status               string `json:"status" gorm:"size:24;index"`
	Reason               string `json:"reason" gorm:"size:48"`
	CaptureBody          bool   `json:"capture_body"`
	MaxCount             int64  `json:"max_count"`
	MaxBodyBytes         int64  `json:"max_body_bytes"`
	MaxBytes             int64  `json:"max_bytes"`
	ReservedBytes        int64  `json:"reserved_bytes"`
	RequestedCount       int64  `json:"requested_count"`
	RequestedBytes       int64  `json:"requested_bytes"`
	SavedCount           int64  `json:"saved_count"`
	SavedBytes           int64  `json:"saved_bytes"`
	SkippedBody          int64  `json:"skipped_body"`
	DroppedCount         int64  `json:"dropped_count"`
	QueueDropped         int64  `json:"queue_dropped"`
	WriteDropped         int64  `json:"write_dropped"`
	AdmissionDropped     int64  `json:"admission_dropped"`
	StatsIncomplete      bool   `json:"stats_incomplete"`
	CleanupError         bool   `json:"cleanup_error"`
	RedisRunID           string `json:"-" gorm:"size:64"`
	ExtraExcludedHeaders string `json:"-" gorm:"type:text"`
	Revision             int64  `json:"-"`
}

type AffinityDiagnosticSample struct {
	ID         int64  `json:"id" gorm:"primaryKey"`
	TaskID     int64  `json:"task_id" gorm:"index:idx_diagnostic_task_sample,priority:1"`
	CapturedAt int64  `json:"captured_at"`
	RequestID  string `json:"request_id" gorm:"size:128"`
	Model      string `json:"model" gorm:"type:text"`
	Path       string `json:"path" gorm:"type:text"`
	BodyStatus string `json:"body_status" gorm:"size:32"`
	BodyKind   string `json:"body_kind" gorm:"size:32"`
	SizeBytes  int64  `json:"size_bytes"`
	// Bytes use the dialect's binary mapping (MySQL longblob, PostgreSQL bytea,
	// SQLite blob). Never JSON columns: whitespace and duplicate keys must survive.
	Body     []byte `json:"-"`
	Metadata []byte `json:"-"`
}

func InitAffinityDiagnostics(ctx context.Context, local bool) error {
	db := DB.WithContext(ctx)
	if err := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&AffinityDiagnosticControl{ID: 1}).Error; err != nil {
		return err
	}
	if !local {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&AffinityDiagnosticControl{}).Where("id = ?", 1).Updates(map[string]any{"active_id": 0, "revision": gorm.Expr("revision + 1")}).Error; err != nil {
			return err
		}
		return tx.Model(&AffinityDiagnosticTask{}).Where("status IN ?", []string{"starting", "running"}).Updates(map[string]any{"status": "stopped", "reason": "process_restart", "stats_incomplete": true}).Error
	})
}

func CreateAffinityDiagnostic(ctx context.Context, task *AffinityDiagnosticTask) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&AffinityDiagnosticControl{}).Where("id = ? AND active_id = 0 AND reserved_bytes <= ?", 1, DiagnosticStorageBudget-task.ReservedBytes).Updates(map[string]any{"reserved_bytes": gorm.Expr("reserved_bytes + ?", task.ReservedBytes), "revision": gorm.Expr("revision + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDiagnosticCapacity
		}
		var count int64
		if err := tx.Model(&User{}).Where("id = ?", task.UserID).Count(&count).Error; err != nil {
			return err
		}
		if count != 1 {
			return errors.New("Diagnostic target user does not exist")
		}
		if err := tx.Create(task).Error; err != nil {
			return err
		}
		return tx.Model(&AffinityDiagnosticControl{}).Where("id = ?", 1).Update("active_id", task.ID).Error
	})
}

func ActivateAffinityDiagnostic(ctx context.Context, id int64) error {
	result := DB.WithContext(ctx).Model(&AffinityDiagnosticTask{}).Where("id = ? AND status = ?", id, "starting").Update("status", "running")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrDiagnosticUnavailable
	}
	return nil
}

func ActiveAffinityDiagnostic(ctx context.Context) (*AffinityDiagnosticTask, error) {
	var ctl AffinityDiagnosticControl
	if err := DB.WithContext(ctx).First(&ctl, 1).Error; err != nil {
		return nil, err
	}
	if ctl.ActiveID == 0 {
		return nil, nil
	}
	var task AffinityDiagnosticTask
	if err := DB.WithContext(ctx).First(&task, ctl.ActiveID).Error; err != nil {
		return nil, err
	}
	return &task, nil
}

func StopAffinityDiagnostic(ctx context.Context, id int64, reason string, remove, incomplete bool) error {
	return DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// All transactions that need both rows take control before task.
		if err := tx.Model(&AffinityDiagnosticControl{}).Where("id = ?", 1).UpdateColumn("revision", gorm.Expr("revision + 1")).Error; err != nil {
			return err
		}
		query := tx.Model(&AffinityDiagnosticTask{}).Where("id = ?", id)
		state := "stopped"
		if remove {
			state = "deleting"
		} else {
			query = query.Where("status IN ?", []string{"starting", "running"})
		}
		updates := map[string]any{"status": state, "reason": reason, "revision": gorm.Expr("revision + 1")}
		if incomplete {
			updates["stats_incomplete"] = true
		}
		if err := query.Updates(updates).Error; err != nil {
			return err
		}
		return tx.Model(&AffinityDiagnosticControl{}).Where("id = ? AND active_id = ?", 1, id).Update("active_id", 0).Error
	})
}

func SaveAffinityDiagnosticSample(ctx context.Context, s *AffinityDiagnosticSample) error {
	// SQL error logging can interpolate printable []byte bind parameters. Sample
	// contents must never escape into the ordinary process log, even on failure.
	return DB.WithContext(ctx).Session(&gorm.Session{Logger: DB.Logger.LogMode(logger.Silent)}).Transaction(func(tx *gorm.DB) error {
		// This write races deletion on the task row. Stopped tasks still drain.
		result := tx.Model(&AffinityDiagnosticTask{}).Where("id = ? AND status IN ? AND expires_at > ? AND saved_count < max_count AND saved_bytes <= max_bytes - ?", s.TaskID, []string{"running", "stopped"}, time.Now().UnixMilli(), s.SizeBytes).Updates(map[string]any{"saved_count": gorm.Expr("saved_count + 1"), "saved_bytes": gorm.Expr("saved_bytes + ?", s.SizeBytes), "revision": gorm.Expr("revision + 1")})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrDiagnosticUnavailable
		}
		if s.BodyStatus != "captured" {
			if err := tx.Model(&AffinityDiagnosticTask{}).Where("id = ?", s.TaskID).UpdateColumn("skipped_body", gorm.Expr("skipped_body + 1")).Error; err != nil {
				return err
			}
		}
		return tx.Create(s).Error
	})
}

func ListAffinityDiagnosticTasks(ctx context.Context, offset, limit int) ([]AffinityDiagnosticTask, int64, error) {
	var rows []AffinityDiagnosticTask
	var total int64
	db := DB.WithContext(ctx).Model(&AffinityDiagnosticTask{})
	if err := db.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	err := db.Order("id desc").Offset(offset).Limit(limit).Find(&rows).Error
	return rows, total, err
}

func ReadAffinityDiagnosticTask(ctx context.Context, id int64) (*AffinityDiagnosticTask, error) {
	var task AffinityDiagnosticTask
	err := DB.WithContext(ctx).Where("id = ? AND expires_at > ? AND status <> ?", id, time.Now().UnixMilli(), "deleting").First(&task).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrDiagnosticUnavailable
	}
	return &task, err
}

func ListAffinityDiagnosticSamples(ctx context.Context, id int64, offset, limit int) ([]AffinityDiagnosticSample, int64, error) {
	var rows []AffinityDiagnosticSample
	var total int64
	err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var task AffinityDiagnosticTask
		if err := tx.Where("id = ? AND expires_at > ? AND status <> ?", id, time.Now().UnixMilli(), "deleting").First(&task).Error; err != nil {
			return ErrDiagnosticUnavailable
		}
		db := tx.Model(&AffinityDiagnosticSample{}).Where("task_id = ?", id)
		if err := db.Count(&total).Error; err != nil {
			return err
		}
		return db.Omit("body", "metadata").Order("id desc").Offset(offset).Limit(limit).Find(&rows).Error
	})
	return rows, total, err
}

func ReadAffinityDiagnosticSample(ctx context.Context, taskID, sampleID int64) (*AffinityDiagnosticSample, error) {
	var row AffinityDiagnosticSample
	// Subquery keeps lifetime validation and sample lookup in one statement.
	db := DB.WithContext(ctx)
	validTasks := db.Model(&AffinityDiagnosticTask{}).Select("id").Where("expires_at > ? AND status <> ?", time.Now().UnixMilli(), "deleting")
	err := db.Where("id = ? AND task_id = ?", sampleID, taskID).Where("task_id IN (?)", validTasks).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		err = ErrDiagnosticUnavailable
	}
	return &row, err
}

// Clean one bounded batch per task. Capacity stays reserved until every sample
// has physically gone; the final transaction removes the tombstone as well.
func CleanupAffinityDiagnostics(ctx context.Context) error {
	var tasks []AffinityDiagnosticTask
	if err := DB.WithContext(ctx).Where("expires_at <= ? OR status = ?", time.Now().UnixMilli(), "deleting").Order("id").Limit(20).Find(&tasks).Error; err != nil {
		return err
	}
	for _, task := range tasks {
		if task.Status != "deleting" {
			if err := StopAffinityDiagnostic(ctx, task.ID, "expired", true, false); err != nil {
				return err
			}
		}
		err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Model(&AffinityDiagnosticControl{}).Where("id = ?", 1).UpdateColumn("revision", gorm.Expr("revision + 1")).Error; err != nil {
				return err
			}
			var current AffinityDiagnosticTask
			if err := lockForUpdate(tx).First(&current, task.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			var ids []int64
			if err := tx.Model(&AffinityDiagnosticSample{}).Where("task_id = ?", task.ID).Order("id").Limit(100).Pluck("id", &ids).Error; err != nil {
				return err
			}
			if len(ids) > 0 {
				if err := tx.Where("id IN ?", ids).Delete(&AffinityDiagnosticSample{}).Error; err != nil {
					return err
				}
			}
			var remaining int64
			if err := tx.Model(&AffinityDiagnosticSample{}).Where("task_id = ?", task.ID).Count(&remaining).Error; err != nil {
				return err
			}
			if remaining != 0 {
				return nil
			}
			if err := tx.Delete(&current).Error; err != nil {
				return err
			}
			return tx.Model(&AffinityDiagnosticControl{}).Where("id = ?", 1).UpdateColumn("reserved_bytes", gorm.Expr("reserved_bytes - ?", current.ReservedBytes)).Error
		})
		if err != nil {
			DB.WithContext(ctx).Model(&AffinityDiagnosticTask{}).Where("id = ?", task.ID).Update("cleanup_error", true)
			return err
		}
	}
	return nil
}
