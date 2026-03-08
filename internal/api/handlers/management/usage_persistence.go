package management

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	log "github.com/sirupsen/logrus"
)

const (
	usageStatisticsStateFileName   = ".usage-statistics-cache.json"
	usageStatisticsPersistInterval = 30 * time.Second
)

type usageStatisticsPersistentState struct {
	Version   int                      `json:"version"`
	UpdatedAt time.Time                `json:"updated_at"`
	Usage     usage.StatisticsSnapshot `json:"usage"`
}

func (h *Handler) startUsagePersistence() {
	if h == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(usageStatisticsPersistInterval)
		defer ticker.Stop()
		for range ticker.C {
			h.persistUsageStatisticsState()
		}
	}()
}

func (h *Handler) usageStatisticsStateFilePath() string {
	if h == nil {
		return ""
	}
	baseDir := ""
	if cfgPath := strings.TrimSpace(h.configFilePath); cfgPath != "" {
		baseDir = filepath.Dir(cfgPath)
	} else if h.cfg != nil {
		baseDir = strings.TrimSpace(h.cfg.AuthDir)
	}
	if baseDir == "" {
		return ""
	}
	return filepath.Join(baseDir, usageStatisticsStateFileName)
}

func (h *Handler) loadUsageStatisticsState() {
	if h == nil || h.usageStats == nil {
		return
	}
	path := h.usageStatisticsStateFilePath()
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	var state usageStatisticsPersistentState
	if err := json.Unmarshal(data, &state); err != nil {
		log.WithError(err).Debugf("failed to load usage statistics cache from %s", path)
		return
	}
	if state.Version != 0 && state.Version != 1 {
		log.Debugf("skip usage statistics cache %s: unsupported version %d", path, state.Version)
		return
	}
	result := h.usageStats.MergeSnapshot(state.Usage)
	if result.Added > 0 {
		log.Debugf("loaded usage statistics cache from %s (added=%d skipped=%d)", path, result.Added, result.Skipped)
	}
	snapshot := h.usageStats.Snapshot()
	runtime := h.usagePersistenceStateRef()
	if runtime == nil {
		return
	}
	runtime.usagePersistMu.Lock()
	runtime.usagePersistRequest = snapshot.TotalRequests
	runtime.usagePersistFailure = snapshot.FailureCount
	runtime.usagePersistTokens = snapshot.TotalTokens
	runtime.usagePersisted = true
	runtime.usagePersistMu.Unlock()
}

func (h *Handler) persistUsageStatisticsState() {
	if h == nil || h.usageStats == nil {
		return
	}
	path := h.usageStatisticsStateFilePath()
	if path == "" {
		return
	}
	snapshot := h.usageStats.Snapshot()
	runtime := h.usagePersistenceStateRef()
	if runtime == nil {
		return
	}

	runtime.usagePersistMu.Lock()
	defer runtime.usagePersistMu.Unlock()
	if runtime.usagePersisted &&
		runtime.usagePersistRequest == snapshot.TotalRequests &&
		runtime.usagePersistFailure == snapshot.FailureCount &&
		runtime.usagePersistTokens == snapshot.TotalTokens {
		return
	}

	state := usageStatisticsPersistentState{
		Version:   1,
		UpdatedAt: time.Now().UTC(),
		Usage:     snapshot,
	}
	encoded, err := json.Marshal(state)
	if err != nil {
		log.WithError(err).Debug("failed to marshal usage statistics cache")
		return
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.WithError(err).Debugf("failed to create usage statistics cache directory %s", dir)
		return
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		log.WithError(err).Debugf("failed to write usage statistics cache temp file %s", tmp)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		log.WithError(err).Debugf("failed to commit usage statistics cache file %s", path)
		_ = os.Remove(tmp)
		return
	}

	runtime.usagePersistRequest = snapshot.TotalRequests
	runtime.usagePersistFailure = snapshot.FailureCount
	runtime.usagePersistTokens = snapshot.TotalTokens
	runtime.usagePersisted = true
}
