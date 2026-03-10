package management

import (
	"sync"
	"sync/atomic"
	"time"
)

type usagePersistenceState struct {
	usagePersistMu      sync.Mutex
	usagePersisted      bool
	usagePersistRequest int64
	usagePersistFailure int64
	usagePersistTokens  int64
}

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
	codexUsageAsyncPoll            atomic.Bool
}

type handlerUsageRuntime struct {
	initOnce         sync.Once
	codexUsage       *codexUsageState
	usagePersistence *usagePersistenceState
}

var handlerUsageRuntimeRegistry sync.Map

func newUsagePersistenceState() *usagePersistenceState {
	return &usagePersistenceState{}
}

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
	runtime := &handlerUsageRuntime{
		codexUsage:       newCodexUsageState(),
		usagePersistence: newUsagePersistenceState(),
	}
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
		h.loadUsageStatisticsState()
		h.loadCodexUsageState()
		h.startUsagePersistence()
	})
}

func (h *Handler) usagePersistenceStateRef() *usagePersistenceState {
	runtime := h.usageRuntimeEntry()
	if runtime == nil {
		return nil
	}
	return runtime.usagePersistence
}

func (h *Handler) codexUsageStateRef() *codexUsageState {
	runtime := h.usageRuntimeEntry()
	if runtime == nil {
		return nil
	}
	return runtime.codexUsage
}
