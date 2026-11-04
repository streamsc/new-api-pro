package model

import (
	"github.com/QuantumNous/new-api/setting"
	"gorm.io/gorm"
)

// UpdateUserResourcesConfig reads the previous persisted value under the same
// row lock as the write so audits do not use another replica's stale option cache.
func UpdateUserResourcesConfig(value string) (setting.UserResourcesConfig, error) {
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
		return tx.Model(&option).Update("value", value).Error
	})
	if err != nil {
		return before, err
	}
	return before, updateOptionMap(setting.UserResourcesOption, value)
}
