package setting

import (
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
)

const UserResourcesOption = "UserResourceLimits"

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
	rule, ok := c.Models[model]
	if !ok {
		return nil
	}
	limits := rule.Users[strconv.Itoa(id)].Apply(rule.Defaults)
	return &limits
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
		if l.Limit < 0 || l.Limit > 2147483647 {
			return c, fmt.Errorf("resource limit must be between 0 and 2147483647")
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
