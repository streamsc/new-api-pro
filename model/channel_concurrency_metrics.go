package model

import "context"

type ChannelConcurrencyConfig struct {
	ID      int `gorm:"column:id"`
	Setting *string
}

func GetChannelConcurrencyConfigs(ctx context.Context) ([]ChannelConcurrencyConfig, error) {
	var channels []ChannelConcurrencyConfig
	err := DB.WithContext(ctx).Model(&Channel{}).Select("id", "setting").Find(&channels).Error
	return channels, err
}
