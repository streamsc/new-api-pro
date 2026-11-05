package model

import "context"

type ChannelConcurrencyConfig struct {
	ID      int `gorm:"column:id"`
	Name    string
	Setting *string
}

func GetChannelConcurrencyConfigs(ctx context.Context) ([]ChannelConcurrencyConfig, error) {
	var channels []ChannelConcurrencyConfig
	err := DB.WithContext(ctx).Model(&Channel{}).Select("id", "name", "setting").Find(&channels).Error
	return channels, err
}
