package management

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func TestUsageStatisticsPersistenceRoundTrip(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	configPath := filepath.Join(baseDir, "config.yaml")
	ts := time.Date(2026, 3, 6, 9, 0, 0, 0, time.UTC)

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: configPath,
		usageStats:     usage.NewRequestStatistics(),
	}
	snapshot := usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			"test-api": {
				Models: map[string]usage.ModelSnapshot{
					"gpt-5": {
						Details: []usage.RequestDetail{
							{
								Timestamp: ts,
								Source:    "unit-test",
								AuthIndex: "0",
								Tokens: usage.TokenStats{
									InputTokens:  2,
									OutputTokens: 3,
									TotalTokens:  5,
								},
								Failed: false,
							},
						},
					},
				},
			},
		},
	}
	merged := h.usageStats.MergeSnapshot(snapshot)
	if merged.Added != 1 {
		t.Fatalf("expected one imported usage detail, got added=%d skipped=%d", merged.Added, merged.Skipped)
	}
	h.persistUsageStatisticsState()

	restored := &Handler{
		cfg:            &config.Config{},
		configFilePath: configPath,
		usageStats:     usage.NewRequestStatistics(),
	}
	restored.loadUsageStatisticsState()
	got := restored.usageStats.Snapshot()

	if got.TotalRequests != 1 {
		t.Fatalf("expected total_requests=1, got %d", got.TotalRequests)
	}
	if got.TotalTokens != 5 {
		t.Fatalf("expected total_tokens=5, got %d", got.TotalTokens)
	}
	api, ok := got.APIs["test-api"]
	if !ok {
		t.Fatal("expected restored snapshot to include test-api")
	}
	model, ok := api.Models["gpt-5"]
	if !ok {
		t.Fatal("expected restored snapshot to include model gpt-5")
	}
	if len(model.Details) != 1 {
		t.Fatalf("expected exactly one detail record, got %d", len(model.Details))
	}
}
