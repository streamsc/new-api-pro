package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestChannelConcurrencyConfigProjection(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			var dialect gorm.Dialector
			switch driver {
			case "sqlite":
				dialect = sqlite.Open(filepath.Join(t.TempDir(), "metrics.db"))
			case "mysql":
				dsn := os.Getenv("CONCURRENCY_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("disposable MySQL database not configured")
				}
				dialect = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("CONCURRENCY_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("disposable PostgreSQL database not configured")
				}
				dialect = postgres.Open(dsn)
			}
			db, err := gorm.Open(dialect, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			defer sqlDB.Close()
			previous := DB
			DB = db
			defer func() { DB = previous }()
			// The fixture deliberately has no credential or full Channel columns.
			type row struct {
				ID      int
				Setting *string
				Status  int
			}
			require.False(t, db.Migrator().HasTable("channels"), "use an empty disposable database")
			require.NoError(t, db.Table("channels").AutoMigrate(&row{}))
			defer db.Migrator().DropTable("channels")
			rows, err := GetChannelConcurrencyConfigs(context.Background())
			require.NoError(t, err)
			assert.Empty(t, rows)
			broken := "{broken"
			require.NoError(t, db.Table("channels").Create(&[]row{{ID: 1, Setting: &broken, Status: 2}, {ID: 2, Status: 1}}).Error)
			rows, err = GetChannelConcurrencyConfigs(context.Background())
			require.NoError(t, err)
			assert.ElementsMatch(t, []ChannelConcurrencyConfig{{ID: 1, Setting: &broken}, {ID: 2}}, rows)
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, err = GetChannelConcurrencyConfigs(ctx)
			require.ErrorIs(t, err, context.Canceled)
			if driver != "sqlite" {
				// A dedicated connection holds the table lock while the projection
				// uses another connection, exercising cancellation inside the driver.
				connection, err := sqlDB.Conn(context.Background())
				require.NoError(t, err)
				if driver == "mysql" {
					_, err = connection.ExecContext(context.Background(), "LOCK TABLES channels WRITE")
				} else {
					_, err = connection.ExecContext(context.Background(), "BEGIN")
					require.NoError(t, err)
					_, err = connection.ExecContext(context.Background(), "LOCK TABLE channels IN ACCESS EXCLUSIVE MODE")
				}
				require.NoError(t, err)
				blocked, stop := context.WithTimeout(context.Background(), 50*time.Millisecond)
				_, queryErr := GetChannelConcurrencyConfigs(blocked)
				assert.Error(t, queryErr)
				assert.ErrorIs(t, blocked.Err(), context.DeadlineExceeded)
				stop()
				if driver == "mysql" {
					_, err = connection.ExecContext(context.Background(), "UNLOCK TABLES")
				} else {
					_, err = connection.ExecContext(context.Background(), "ROLLBACK")
				}
				require.NoError(t, err)
				require.NoError(t, connection.Close())
			}
			updated := `{"max_concurrency":0}`
			require.NoError(t, db.Table("channels").Where("id = ?", 1).Update("setting", updated).Error)
			require.NoError(t, db.Table("channels").Where("id = ?", 2).Delete(&row{}).Error)
			rows, err = GetChannelConcurrencyConfigs(context.Background())
			require.NoError(t, err)
			assert.Equal(t, []ChannelConcurrencyConfig{{ID: 1, Setting: &updated}}, rows)
		})
	}
}
