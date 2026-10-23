package service

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/concurrencymetrics"
)

func CollectChannelConcurrency(ctx context.Context) concurrencymetrics.Snapshot {
	scope := "process"
	if common.RedisEnabled {
		scope = "redis"
	}
	return collectChannelConcurrency(ctx, scope, model.GetChannelConcurrencyConfigs, GetChannelConcurrencyCounts)
}

func collectChannelConcurrency(ctx context.Context, scope string, readConfigs func(context.Context) ([]model.ChannelConcurrencyConfig, error), readCounts func(context.Context, []int) (map[int]int, error)) concurrencymetrics.Snapshot {
	snapshot := concurrencymetrics.Snapshot{Scope: scope}
	if ctx.Err() != nil {
		return snapshot
	}
	dbContext, cancel := context.WithTimeout(ctx, 2*time.Second)
	channels, err := readConfigs(dbContext)
	dbErr := dbContext.Err()
	cancel()
	if err != nil || dbErr != nil {
		return snapshot
	}
	snapshot.ConfigSuccess = true
	ids := make([]int, 0, len(channels))
	for _, channel := range channels {
		if ctx.Err() != nil {
			snapshot.ConfigSuccess = false
			return snapshot
		}
		maximum := parseConcurrencyLimit(channel.Setting)
		if maximum == nil {
			snapshot.ConfigSuccess = false
		}
		snapshot.Channels = append(snapshot.Channels, concurrencymetrics.ChannelSample{ChannelID: channel.ID, Limit: maximum})
		ids = append(ids, channel.ID)
	}
	if len(ids) == 0 {
		snapshot.ConcurrencySuccess = true
		return snapshot
	}
	if ctx.Err() != nil {
		return snapshot
	}
	counts, err := readCounts(ctx, ids)
	if err != nil || ctx.Err() != nil {
		return snapshot
	}
	// Validate the entire batch before exposing any counts.
	for _, id := range ids {
		count, ok := counts[id]
		if !ok || count < 0 {
			return snapshot
		}
	}
	for i := range snapshot.Channels {
		count := counts[snapshot.Channels[i].ChannelID]
		snapshot.Channels[i].InFlight = &count
	}
	snapshot.ConcurrencySuccess = true
	return snapshot
}

func parseConcurrencyLimit(setting *string) *int {
	maximum := 0
	if setting == nil || strings.TrimSpace(*setting) == "" {
		return &maximum
	}
	if common.GetJsonType(json.RawMessage(*setting)) != "object" {
		return nil
	}
	var fields struct {
		Maximum json.RawMessage `json:"max_concurrency"`
	}
	if err := common.UnmarshalJsonStr(*setting, &fields); err != nil {
		return nil
	}
	if len(fields.Maximum) == 0 {
		return &maximum
	}
	if common.GetJsonType(fields.Maximum) != "number" {
		return nil
	}
	if err := common.Unmarshal(fields.Maximum, &maximum); err != nil || maximum < 0 {
		return nil
	}
	return &maximum
}
