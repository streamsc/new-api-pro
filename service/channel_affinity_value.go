package service

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

// AffinityValueInput keeps online routing and stored diagnostics on one evaluator.
// Availability is separate from value; empty values must not become "missing".
type AffinityValueInput interface {
	HeaderValue(string) (string, string)
	ContextValue(string) (any, string)
	BodyBytes() ([]byte, string)
}

type AffinityValueResult struct {
	Status string `json:"status"`
	Value  string `json:"value"`
}

func EvaluateAffinityValue(input AffinityValueInput, src operation_setting.ChannelAffinityKeySource) AffinityValueResult {
	result := AffinityValueResult{Status: "missing"}
	switch src.Type {
	case "context_int", "context_string":
		if src.Key == "" {
			return result
		}
		v, status := input.ContextValue(src.Key)
		result.Status = status
		if status != "present" {
			return result
		}
		if src.Type == "context_int" {
			n, ok := v.(int)
			if !ok {
				result.Status = "type_mismatch"
				return result
			}
			if n > 0 {
				result.Value = strconv.Itoa(n)
			}
		} else {
			s, ok := v.(string)
			if !ok {
				result.Status = "type_mismatch"
				return result
			}
			result.Value = strings.TrimSpace(s)
		}
	case "request_header":
		if src.Key == "" {
			return result
		}
		value, status := input.HeaderValue(src.Key)
		result.Status = status
		if status != "present" {
			return result
		}
		result.Value = strings.TrimSpace(value)
	case "gjson":
		if src.Path == "" {
			return result
		}
		body, status := input.BodyBytes()
		result.Status = status
		if status != "present" {
			return result
		}
		value := gjson.GetBytes(body, src.Path)
		if !value.Exists() {
			result.Status = "missing"
			return result
		}
		switch value.Type {
		case gjson.String, gjson.Number, gjson.True, gjson.False:
			result.Value = strings.TrimSpace(value.String())
		default:
			result.Value = strings.TrimSpace(value.Raw)
		}
	default:
		result.Status = "unsupported"
		return result
	}
	if result.Value == "" {
		result.Status = "empty"
	} else {
		result.Status = "extracted"
	}
	return result
}

type liveAffinityInput struct{ c *gin.Context }

func (in liveAffinityInput) HeaderValue(key string) (string, string) {
	if in.c == nil || in.c.Request == nil {
		return "", "missing"
	}
	if _, ok := in.c.Request.Header[http.CanonicalHeaderKey(key)]; !ok {
		return "", "missing"
	}
	return in.c.Request.Header.Get(key), "present"
}
func (in liveAffinityInput) ContextValue(key string) (any, string) {
	v, ok := in.c.Get(key)
	if !ok {
		return nil, "missing"
	}
	return v, "present"
}
func (in liveAffinityInput) BodyBytes() ([]byte, string) {
	s, err := common.GetBodyStorage(in.c)
	if err != nil {
		return nil, "read_failed"
	}
	b, err := s.Bytes()
	if err != nil {
		return nil, "read_failed"
	}
	return b, "present"
}
