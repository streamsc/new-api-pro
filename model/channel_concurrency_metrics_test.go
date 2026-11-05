package model

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting"
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
				Name    string
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
			require.NoError(t, db.Table("channels").Create(&[]row{{ID: 1, Name: "same", Setting: &broken, Status: 2}, {ID: 2, Name: "same", Status: 1}}).Error)
			rows, err = GetChannelConcurrencyConfigs(context.Background())
			require.NoError(t, err)
			assert.ElementsMatch(t, []ChannelConcurrencyConfig{{ID: 1, Name: "same", Setting: &broken}, {ID: 2, Name: "same"}}, rows)
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
			require.NoError(t, db.Table("channels").Where("id = ?", 1).Updates(map[string]any{"setting": updated, "name": "renamed"}).Error)
			require.NoError(t, db.Table("channels").Where("id = ?", 2).Delete(&row{}).Error)
			rows, err = GetChannelConcurrencyConfigs(context.Background())
			require.NoError(t, err)
			assert.Equal(t, []ChannelConcurrencyConfig{{ID: 1, Name: "renamed", Setting: &updated}}, rows)
		})
	}
}

func TestUserResourceOptionsDatabaseMatrix(t *testing.T) {
	for _, driver := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(driver, func(t *testing.T) {
			var dialect gorm.Dialector
			switch driver {
			case "sqlite":
				dialect = sqlite.Open(filepath.Join(t.TempDir(), "resources.db"))
			case "mysql":
				dsn := os.Getenv("CONCURRENCY_TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("dedicated MySQL not configured")
				}
				dialect = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("CONCURRENCY_TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("dedicated PostgreSQL not configured")
				}
				dialect = postgres.Open(dsn)
			}
			db, err := gorm.Open(dialect, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			defer sqlDB.Close()
			previousDB := DB
			DB = db
			defer func() { DB = previousDB }()
			previousType := common.MainDatabaseType()
			common.SetMainDatabaseType(common.DatabaseType(driver))
			defer common.SetMainDatabaseType(previousType)
			common.OptionMapRWMutex.Lock()
			previousMap := common.OptionMap
			common.OptionMap = map[string]string{}
			common.OptionMapRWMutex.Unlock()
			defer func() {
				common.OptionMapRWMutex.Lock()
				common.OptionMap = previousMap
				common.OptionMapRWMutex.Unlock()
			}()
			require.False(t, db.Migrator().HasTable(&Option{}), "use a dedicated empty test database")
			require.NoError(t, db.AutoMigrate(&Option{}))
			defer db.Migrator().DropTable(&Option{})
			require.NoError(t, db.Create(&Option{Key: "Unrelated", Value: "preserve"}).Error)
			for range 2 {
				require.NoError(t, db.AutoMigrate(&Option{}))
			}
			before, err := UpdateUserResourcesConfig(`{"defaults":{"rpm":{"enabled":true,"limit":10}},"users":{}}`)
			require.NoError(t, err)
			assert.False(t, before.Defaults.RPM.Enabled)
			// Another node's local cache can be stale; audit must use the database value.
			common.OptionMapRWMutex.Lock()
			common.OptionMap[setting.UserResourcesOption] = ""
			common.OptionMapRWMutex.Unlock()
			before, err = UpdateUserResourcesConfig(`{"defaults":{"rpm":{"enabled":true,"limit":0}},"users":{"7":{"rpm":{"enabled":false,"limit":0}}}}`)
			require.NoError(t, err)
			assert.Equal(t, 10, before.Defaults.RPM.Limit)
			config, err := setting.CurrentUserResourcesConfig()
			require.NoError(t, err)
			assert.True(t, config.ForUser(8).RPM.Enabled)
			assert.False(t, config.ForUser(7).RPM.Enabled)
			_, err = UpdateUserResourcesConfig(`{"defaults":{"rpm":{"limit":-1}}}`)
			require.Error(t, err)
			var option Option
			require.NoError(t, db.First(&option, Option{Key: "Unrelated"}).Error)
			assert.Equal(t, "preserve", option.Value)
			var version string
			query := "SELECT version()"
			if driver == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
		})
	}
}
