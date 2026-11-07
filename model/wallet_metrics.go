package model

import "context"

// UserWalletSnapshot contains only the public monitoring fields, in quota units.
type UserWalletSnapshot struct {
	Id        int
	Username  string
	Quota     int
	UsedQuota int
}

// ReadUserWalletSnapshot includes disabled, inactive and zero-balance users.
// Model keeps GORM's soft-delete filter even though the result is a projection.
func ReadUserWalletSnapshot(ctx context.Context) ([]UserWalletSnapshot, error) {
	var users []UserWalletSnapshot
	err := DB.WithContext(ctx).Model(&User{}).
		Select("id", "username", "quota", "used_quota").Find(&users).Error
	return users, err
}
