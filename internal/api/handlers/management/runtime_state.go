package management

import (
	"sync"
	"time"
)

type codexUsageState struct {
	codexUsageMu                   sync.RWMutex
	codexUsagePollMu               sync.Mutex
	codexUsageByAuth               map[string]codexAuthUsageStatus
	codexUsageCompat               codexUsagePayload
	codexUsageSummary              codexUsageSummaryResponse
	codexUsageHasData              bool
	codexUsageSelected             string
	codexObservedServiceTierAuthID string
	codexObservedServiceTier       string
	codexObservedServiceTierAt     time.Time
}

type handlerUsageRuntime struct {
	initOnce   sync.Once
	codexUsage *codexUsageState
}

var handlerUsageRuntimeRegistry sync.Map

func newCodexUsageState() *codexUsageState {
	return &codexUsageState{
		codexUsageByAuth: make(map[string]codexAuthUsageStatus),
		codexUsageCompat: defaultCodexUsagePayload(),
	}
}

func (h *Handler) usageRuntimeEntry() *handlerUsageRuntime {
	if h == nil {
		return nil
	}
	if existing, ok := handlerUsageRuntimeRegistry.Load(h); ok {
		if runtime, okRuntime := existing.(*handlerUsageRuntime); okRuntime && runtime != nil {
			return runtime
		}
	}
	runtime := &handlerUsageRuntime{codexUsage: newCodexUsageState()}
	actual, _ := handlerUsageRuntimeRegistry.LoadOrStore(h, runtime)
	if stored, ok := actual.(*handlerUsageRuntime); ok && stored != nil {
		return stored
	}
	return runtime
}

func (h *Handler) ensureUsageRuntimeInitialized() {
	runtime := h.usageRuntimeEntry()
	if runtime == nil {
		return
	}
	runtime.initOnce.Do(func() {
		h.loadCodexUsageState()
	})
}

func (h *Handler) codexUsageStateRef() *codexUsageState {
	runtime := h.usageRuntimeEntry()
	if runtime == nil {
		return nil
	}
	return runtime.codexUsage
}
