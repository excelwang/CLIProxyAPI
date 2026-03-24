package management

import (
	"os"
	"strings"

	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

type codexUsageConfigOverrides struct {
	CodexFreePlanWeight       float64   `yaml:"codex-free-plan-weight"`
	CodexProPlanWeight        float64   `yaml:"codex-pro-plan-weight"`
	CodexOAuthAvailableTotals []float64 `yaml:"codex-oauth-available-totals"`
}

func (h *Handler) codexUsageConfigOverrides() codexUsageConfigOverrides {
	if h == nil {
		return codexUsageConfigOverrides{}
	}
	path := strings.TrimSpace(h.configFilePath)
	if path == "" {
		return codexUsageConfigOverrides{}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return codexUsageConfigOverrides{}
	}
	var overrides codexUsageConfigOverrides
	if err := yaml.Unmarshal(data, &overrides); err != nil {
		log.WithError(err).Debugf("failed to parse codex usage overrides from %s", path)
		return codexUsageConfigOverrides{}
	}
	return overrides
}
