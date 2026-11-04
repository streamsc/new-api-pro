package setting

import (
	"fmt"
	"strconv"

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
type UserResourcesConfig struct {
	Defaults UserResourceLimits               `json:"defaults"`
	Users    map[string]UserResourceOverrides `json:"users"`
}

func (l UserResourceLimits) Restricted() bool {
	return l.Concurrency.Enabled || l.InputTPM.Enabled || l.RPM.Enabled
}
func (c UserResourcesConfig) ForUser(id int) UserResourceLimits {
	l := c.Defaults
	o := c.Users[strconv.Itoa(id)]
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
	for id, o := range c.Users {
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
	for _, l := range limits {
		if l.Limit < 0 || l.Limit > 2147483647 {
			return c, fmt.Errorf("resource limit must be between 0 and 2147483647")
		}
	}
	return c, nil
}
func CurrentUserResourcesConfig() (UserResourcesConfig, error) {
	common.OptionMapRWMutex.RLock()
	value := common.OptionMap[UserResourcesOption]
	common.OptionMapRWMutex.RUnlock()
	return ParseUserResourcesConfig(value)
}
