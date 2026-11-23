package setting

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
)

const UserResourcesOption = "UserResourceLimits"
const maxUserResourceLimit = 2147483647

type ResourceLimit struct {
	Enabled bool `json:"enabled"`
	Limit   int  `json:"limit"`
}
type UserResourceLimits struct {
	Concurrency ResourceLimit `json:"concurrency"`
	InputTPM    ResourceLimit `json:"input_tpm"`
	RPM         ResourceLimit `json:"rpm"`
}
type UserResourceOverrides struct {
	Concurrency *ResourceLimit `json:"concurrency,omitempty"`
	InputTPM    *ResourceLimit `json:"input_tpm,omitempty"`
	RPM         *ResourceLimit `json:"rpm,omitempty"`
}
type ModelResourceConfig struct {
	Defaults UserResourceLimits               `json:"defaults"`
	Users    map[string]UserResourceOverrides `json:"users"`
	Schedule *ModelResourceSchedule           `json:"schedule,omitempty"`
}
type ModelResourceSchedule struct {
	Timezone string                `json:"timezone"`
	Periods  []ModelResourcePeriod `json:"periods"`

	location *time.Location
	windows  []modelResourceWindow
}
type ModelResourcePeriod struct {
	Start      string  `json:"start"`
	End        string  `json:"end"`
	Multiplier float64 `json:"multiplier"`
}
type modelResourceWindow struct {
	start, end int
	multiplier decimal.Decimal
}

type userResourceClockKey struct{}

// WithUserResourceClock supplies the clock for evaluating model schedules.
// The resource store keeps its own clock for slot and window expiration.
func WithUserResourceClock(ctx context.Context, now func() time.Time) context.Context {
	return context.WithValue(ctx, userResourceClockKey{}, now)
}

func UserResourceTime(ctx context.Context) time.Time {
	if now, ok := ctx.Value(userResourceClockKey{}).(func() time.Time); ok {
		return now()
	}
	return time.Now()
}

type UserResourcesConfig struct {
	Models   map[string]ModelResourceConfig   `json:"models,omitempty"`
	Defaults UserResourceLimits               `json:"defaults"`
	Users    map[string]UserResourceOverrides `json:"users"`
}

func (l UserResourceLimits) Restricted() bool {
	return l.Concurrency.Enabled || l.InputTPM.Enabled || l.RPM.Enabled
}
func (c UserResourcesConfig) ForUser(id int) UserResourceLimits {
	return c.Users[strconv.Itoa(id)].Apply(c.Defaults)
}
func (o UserResourceOverrides) Apply(l UserResourceLimits) UserResourceLimits {
	if o.Concurrency != nil {
		l.Concurrency = *o.Concurrency
	}
	if o.InputTPM != nil {
		l.InputTPM = *o.InputTPM
	}
	if o.RPM != nil {
		l.RPM = *o.RPM
	}
	return l
}

// ForModel returns nil only when the exact request model is not configured.
func (c UserResourcesConfig) ForModel(id int, model string) *UserResourceLimits {
	return c.ForModelAt(id, model, time.Now())
}

// ForModelAt resolves the exact request model's limits at the supplied instant.
func (c UserResourcesConfig) ForModelAt(id int, model string, at time.Time) *UserResourceLimits {
	rule, ok := c.Models[model]
	if !ok {
		return nil
	}
	limits := rule.Users[strconv.Itoa(id)].Apply(rule.Defaults)
	if schedule := rule.Schedule; schedule != nil {
		local := at.In(schedule.location)
		minute := local.Hour()*60 + local.Minute()
		for _, window := range schedule.windows {
			if minute < window.start {
				break
			}
			if minute < window.end {
				limits.Concurrency = limits.Concurrency.scaled(window.multiplier)
				limits.InputTPM = limits.InputTPM.scaled(window.multiplier)
				limits.RPM = limits.RPM.scaled(window.multiplier)
				break
			}
		}
	}
	return &limits
}

func (l ResourceLimit) scaled(multiplier decimal.Decimal) ResourceLimit {
	if l.Enabled && l.Limit > 0 {
		l.Limit = max(1, int(decimal.NewFromInt(int64(l.Limit)).Mul(multiplier).Floor().IntPart()))
	}
	return l
}

func modelResourceMinute(value string) (int, error) {
	at, err := time.Parse("15:04", value)
	if err != nil || len(value) != len("00:00") {
		return 0, fmt.Errorf("expected a time in HH:mm format")
	}
	return at.Hour()*60 + at.Minute(), nil
}

func (s *ModelResourceSchedule) compile(maxLimit int) error {
	if s.Timezone == "" || s.Timezone == "Local" {
		return fmt.Errorf("a valid IANA timezone is required")
	}
	location, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return fmt.Errorf("a valid IANA timezone is required")
	}
	if len(s.Periods) == 0 {
		return fmt.Errorf("at least one period is required")
	}
	const minutesPerDay = 24 * 60
	windows := make([]modelResourceWindow, 0, len(s.Periods))
	for i, period := range s.Periods {
		start, startErr := modelResourceMinute(period.Start)
		end, endErr := modelResourceMinute(period.End)
		if startErr != nil || endErr != nil || start == end {
			return fmt.Errorf("period %d must have distinct start and end times in HH:mm format", i+1)
		}
		if period.Multiplier <= 0 || math.IsInf(period.Multiplier, 0) || math.IsNaN(period.Multiplier) {
			return fmt.Errorf("period %d multiplier must be a finite positive number", i+1)
		}
		multiplier := decimal.NewFromFloat(period.Multiplier)
		if decimal.NewFromInt(int64(maxLimit)).Mul(multiplier).GreaterThanOrEqual(decimal.NewFromInt(maxUserResourceLimit + 1)) {
			return fmt.Errorf("period %d multiplier exceeds the resource limit maximum of %d", i+1, maxUserResourceLimit)
		}
		if start < end {
			windows = append(windows, modelResourceWindow{start, end, multiplier})
		} else {
			windows = append(windows, modelResourceWindow{start, minutesPerDay, multiplier})
			if end > 0 {
				windows = append(windows, modelResourceWindow{0, end, multiplier})
			}
		}
	}
	slices.SortFunc(windows, func(a, b modelResourceWindow) int { return a.start - b.start })
	for i := 1; i < len(windows); i++ {
		if windows[i].start < windows[i-1].end {
			return fmt.Errorf("periods must not overlap")
		}
	}
	s.location = location
	s.windows = windows
	return nil
}
func ResourceSources(o UserResourceOverrides, defaultSource string) map[string]string {
	sources := map[string]string{"concurrency": defaultSource, "input_tpm": defaultSource, "rpm": defaultSource}
	for name, value := range map[string]*ResourceLimit{"concurrency": o.Concurrency, "input_tpm": o.InputTPM, "rpm": o.RPM} {
		if value != nil {
			sources[name] = "user_override"
		}
	}
	return sources
}
func ValidateResourceModel(model string) error {
	if model == "" || strings.TrimSpace(model) != model || !utf8.ValidString(model) || len(model) > 512 || strings.ContainsFunc(model, unicode.IsControl) {
		return fmt.Errorf("model must be a nonempty exact name of at most 512 bytes without surrounding whitespace or control characters")
	}
	return nil
}
func ParseUserResourcesConfig(value string) (UserResourcesConfig, error) {
	c := UserResourcesConfig{Users: map[string]UserResourceOverrides{}}
	if value != "" {
		if err := common.UnmarshalJsonStr(value, &c); err != nil {
			return c, err
		}
	}
	if c.Users == nil {
		c.Users = map[string]UserResourceOverrides{}
	}
	limits := []ResourceLimit{c.Defaults.Concurrency, c.Defaults.InputTPM, c.Defaults.RPM}
	groups := []map[string]UserResourceOverrides{c.Users}
	for name, rule := range c.Models {
		if rule.Users == nil {
			rule.Users = map[string]UserResourceOverrides{}
			c.Models[name] = rule
		}
		if err := ValidateResourceModel(name); err != nil {
			return c, err
		}
		limits = append(limits, rule.Defaults.Concurrency, rule.Defaults.InputTPM, rule.Defaults.RPM)
		groups = append(groups, rule.Users)
		if rule.Schedule != nil {
			maxLimit := 0
			for _, limit := range []ResourceLimit{rule.Defaults.Concurrency, rule.Defaults.InputTPM, rule.Defaults.RPM} {
				if limit.Enabled {
					maxLimit = max(maxLimit, limit.Limit)
				}
			}
			for _, overrides := range rule.Users {
				for _, limit := range []*ResourceLimit{overrides.Concurrency, overrides.InputTPM, overrides.RPM} {
					if limit != nil && limit.Enabled {
						maxLimit = max(maxLimit, limit.Limit)
					}
				}
			}
			if err := rule.Schedule.compile(maxLimit); err != nil {
				return c, fmt.Errorf("model %q schedule: %w", name, err)
			}
		}
	}
	for _, users := range groups {
		for id, o := range users {
			n, err := strconv.Atoi(id)
			if err != nil || n <= 0 || strconv.Itoa(n) != id {
				return c, fmt.Errorf("invalid user ID")
			}
			for _, l := range []*ResourceLimit{o.Concurrency, o.InputTPM, o.RPM} {
				if l != nil {
					limits = append(limits, *l)
				}
			}
		}
	}
	for _, l := range limits {
		if l.Limit < 0 || l.Limit > maxUserResourceLimit {
			return c, fmt.Errorf("resource limit must be between 0 and %d", maxUserResourceLimit)
		}
	}
	return c, nil
}

// Cached configurations are immutable. Callers must not mutate their maps.
var userResourcesConfigCache struct {
	sync.Mutex
	raw    string
	loaded bool
	config UserResourcesConfig
	err    error
}

func CurrentUserResourcesConfig() (UserResourcesConfig, error) {
	common.OptionMapRWMutex.RLock()
	value := common.OptionMap[UserResourcesOption]
	common.OptionMapRWMutex.RUnlock()
	userResourcesConfigCache.Lock()
	defer userResourcesConfigCache.Unlock()
	if !userResourcesConfigCache.loaded || userResourcesConfigCache.raw != value {
		userResourcesConfigCache.config, userResourcesConfigCache.err = ParseUserResourcesConfig(value)
		userResourcesConfigCache.raw, userResourcesConfigCache.loaded = value, true
	}
	return userResourcesConfigCache.config, userResourcesConfigCache.err
}

// A semantic content version avoids conflicts caused solely by JSON formatting.
func UserResourcesVersion(config UserResourcesConfig) string {
	encoded, _ := common.Marshal(config)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
