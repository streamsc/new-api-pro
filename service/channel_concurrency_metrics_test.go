package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConcurrencyLimit(t *testing.T) {
	for _, tc := range []struct {
		name, setting string
		want          int
		valid         bool
	}{
		{"empty", "", 0, true}, {"whitespace", "  ", 0, true}, {"missing", "{}", 0, true},
		{"unlimited", `{"max_concurrency":0}`, 0, true}, {"limited", `{"max_concurrency":10}`, 10, true},
		{"unknown", `{"max_concurrency":3,"proxy":{},"extra":true}`, 3, true},
		{"null", "null", 0, false}, {"field null", `{"max_concurrency":null}`, 0, false},
		{"array", "[]", 0, false}, {"scalar", "3", 0, false}, {"broken", "{", 0, false},
		{"negative", `{"max_concurrency":-1}`, 0, false}, {"string", `{"max_concurrency":"1"}`, 0, false},
		{"fraction", `{"max_concurrency":1.5}`, 0, false}, {"decimal", `{"max_concurrency":1.0}`, 0, false},
		{"overflow", `{"max_concurrency":999999999999999999999}`, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := parseConcurrencyLimit(&tc.setting)
			if !tc.valid {
				require.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, tc.want, *got)
		})
	}
	require.NotNil(t, parseConcurrencyLimit(nil))
	assert.Zero(t, *parseConcurrencyLimit(nil))
}

func TestConcurrencySnapshotPartialFailures(t *testing.T) {
	good, bad := `{"max_concurrency":10}`, `{"max_concurrency":null}`
	for _, tc := range []struct {
		name    string
		counts  map[int]int
		failure error
		success bool
	}{
		{"complete", map[int]int{1: 0, 2: 3}, nil, true},
		{"missing", map[int]int{1: 0}, nil, false},
		{"negative", map[int]int{1: 0, 2: -1}, nil, false},
		{"store failure", map[int]int{1: 0, 2: 3}, errors.New("store failed"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := collectChannelConcurrency(context.Background(), "redis", func(ctx context.Context) ([]model.ChannelConcurrencyConfig, error) {
				deadline, ok := ctx.Deadline()
				require.True(t, ok)
				assert.LessOrEqual(t, time.Until(deadline), 2*time.Second)
				return []model.ChannelConcurrencyConfig{{ID: 1, Setting: &good}, {ID: 2, Setting: &bad}}, nil
			}, func(_ context.Context, ids []int) (map[int]int, error) {
				assert.Equal(t, []int{1, 2}, ids)
				return tc.counts, tc.failure
			})
			assert.False(t, result.ConfigSuccess)
			assert.Equal(t, tc.success, result.ConcurrencySuccess)
			require.Len(t, result.Channels, 2)
			require.NotNil(t, result.Channels[0].Limit)
			assert.Equal(t, 10, *result.Channels[0].Limit)
			assert.Nil(t, result.Channels[1].Limit)
			if tc.success {
				require.NotNil(t, result.Channels[0].InFlight)
				assert.Zero(t, *result.Channels[0].InFlight)
				require.NotNil(t, result.Channels[1].InFlight)
				assert.Equal(t, 3, *result.Channels[1].InFlight)
			} else {
				for _, sample := range result.Channels {
					assert.Nil(t, sample.InFlight)
				}
			}
		})
	}
}

func TestConcurrencySnapshotEmptyFailureAndCancellation(t *testing.T) {
	for _, mode := range []string{"empty", "db failure", "cancel before", "cancel during"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel before" {
				cancel()
			}
			called := false
			result := collectChannelConcurrency(ctx, "process", func(ctx context.Context) ([]model.ChannelConcurrencyConfig, error) {
				called = true
				if mode == "db failure" {
					return nil, errors.New("database unavailable")
				}
				if mode == "cancel during" {
					cancel()
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return nil, nil
			}, func(context.Context, []int) (map[int]int, error) { t.Fatal("unexpected count read"); return nil, nil })
			assert.Equal(t, mode != "cancel before", called)
			assert.Equal(t, mode == "empty", result.ConfigSuccess)
			assert.Equal(t, mode == "empty", result.ConcurrencySuccess)
			assert.Empty(t, result.Channels)
		})
	}
}
