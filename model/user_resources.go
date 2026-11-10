package model

import (
	"errors"

	"github.com/QuantumNous/new-api/setting"
	"gorm.io/gorm"
)

var ErrUserResourcesConflict = errors.New("Resource limits changed. Reload before saving.")

// UpdateUserResourcesConfig reads the previous persisted value under the same
// row lock as the write so audits do not use another replica's stale option cache.
func UpdateUserResourcesConfig(value, expectedVersion string) (setting.UserResourcesConfig, error) {
	var before setting.UserResourcesConfig
	if _, err := setting.ParseUserResourcesConfig(value); err != nil {
		return before, err
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		option := Option{Key: setting.UserResourcesOption}
		if err := tx.FirstOrCreate(&option, Option{Key: setting.UserResourcesOption}).Error; err != nil {
			return err
		}
		if err := lockForUpdate(tx).First(&option, Option{Key: setting.UserResourcesOption}).Error; err != nil {
			return err
		}
		var err error
		before, err = setting.ParseUserResourcesConfig(option.Value)
		if err != nil {
			return err
		}
		if expectedVersion == "" || setting.UserResourcesVersion(before) != expectedVersion {
			return ErrUserResourcesConflict
		}
		if option.Value == value {
			return nil
		}
		// Compare the persisted value as well as locking it. SQLite has no row locks.
		result := tx.Model(&option).Where("value = ?", option.Value).Update("value", value)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrUserResourcesConflict
		}
		return nil
	})
	if err != nil {
		return before, err
	}
	return before, updateOptionMap(setting.UserResourcesOption, value)
}
