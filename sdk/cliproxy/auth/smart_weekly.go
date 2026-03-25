package auth

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

const (
	defaultSmartWeeklyProtectionThresholdPercent = 5.0
	defaultSmartWeeklyWarmupDelay                = 5 * time.Minute
	defaultSmartWeeklyMaxAuthCount               = 5
)

// SmartWeeklySelector enables scheduler-backed weekly-aware routing.
// Legacy selector fallback still behaves like round-robin when the fast path is disabled.
type SmartWeeklySelector struct {
	fallback RoundRobinSelector
}

// Pick falls back to round-robin when the scheduler fast path is unavailable.
func (s *SmartWeeklySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return s.fallback.Pick(ctx, provider, model, opts, auths)
}

// SmartWeeklySettings configures the built-in smart weekly scheduler behavior.
type SmartWeeklySettings struct {
	ProtectionThresholdPercent float64
	WarmupDelay                time.Duration
	MaxAuthCount               int
}

// WeeklyQuotaSnapshot is the normalized weekly quota state for one auth.
type WeeklyQuotaSnapshot struct {
	AuthID         string
	RemainingRatio float64
	ResetAt        time.Time
	ObservedAt     time.Time
}

// WeeklyQuotaProvider supplies cached weekly quota snapshots for auth IDs.
type WeeklyQuotaProvider interface {
	WeeklyQuotaSnapshots(ctx context.Context, authIDs []string) map[string]WeeklyQuotaSnapshot
}

type smartWeeklyConfig struct {
	protectionThresholdRatio float64
	warmupDelay              time.Duration
	maxAuthCount             int
}

type weeklyWarmupState struct {
	lastSeenResetAt time.Time
	warmupResetAt   time.Time
	warmupDueAt     time.Time
	warmupConsumed  bool
}

type schedulerReadySource struct {
	providerKey string
	priority    int
	view        *readyView
}

type smartWeeklyCandidate struct {
	authID         string
	remainingRatio float64
	resetAt        time.Time
}

func defaultSmartWeeklyConfig() smartWeeklyConfig {
	return normalizeSmartWeeklySettings(SmartWeeklySettings{
		ProtectionThresholdPercent: defaultSmartWeeklyProtectionThresholdPercent,
		WarmupDelay:                defaultSmartWeeklyWarmupDelay,
		MaxAuthCount:               defaultSmartWeeklyMaxAuthCount,
	})
}

func normalizeSmartWeeklySettings(settings SmartWeeklySettings) smartWeeklyConfig {
	threshold := settings.ProtectionThresholdPercent
	switch {
	case math.IsNaN(threshold), math.IsInf(threshold, 0):
		threshold = defaultSmartWeeklyProtectionThresholdPercent
	case threshold < 0:
		threshold = 0
	case threshold > 100:
		threshold = 100
	}

	delay := settings.WarmupDelay
	if delay < 0 {
		delay = 0
	}

	maxAuthCount := settings.MaxAuthCount

	return smartWeeklyConfig{
		protectionThresholdRatio: threshold / 100,
		warmupDelay:              delay,
		maxAuthCount:             maxAuthCount,
	}
}

func (s *authScheduler) setWeeklyQuotaProvider(provider WeeklyQuotaProvider) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.weeklyQuotaProvider = provider
	s.mu.Unlock()
}

func (s *authScheduler) setSmartWeeklySettings(settings SmartWeeklySettings) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.smartWeekly = normalizeSmartWeeklySettings(settings)
	s.mu.Unlock()
}

func (m *modelScheduler) readySourceLocked(providerKey string, preferWebsocket bool, priority int) (schedulerReadySource, bool) {
	if m == nil {
		return schedulerReadySource{}, false
	}
	bucket := m.readyByPriority[priority]
	if bucket == nil {
		return schedulerReadySource{}, false
	}
	view := &bucket.all
	if preferWebsocket && len(bucket.ws.flat) > 0 {
		view = &bucket.ws
	}
	if view == nil || len(view.flat) == 0 {
		return schedulerReadySource{}, false
	}
	return schedulerReadySource{
		providerKey: providerKey,
		priority:    priority,
		view:        view,
	}, true
}

func (m *modelScheduler) readySourcesLocked(providerKey string, preferWebsocket bool) []schedulerReadySource {
	if m == nil {
		return nil
	}
	out := make([]schedulerReadySource, 0, len(m.priorityOrder))
	for _, priority := range m.priorityOrder {
		source, ok := m.readySourceLocked(providerKey, preferWebsocket, priority)
		if !ok {
			continue
		}
		out = append(out, source)
	}
	return out
}

func (s *authScheduler) pickSingleSmartWeeklyLocked(ctx context.Context, providerKey string, shard *modelScheduler, preferWebsocket bool, predicate func(*scheduledAuth) bool) *Auth {
	if shard == nil {
		return nil
	}

	sources := shard.readySourcesLocked(providerKey, preferWebsocket)
	if picked, _ := s.pickSmartWeeklyWarmupLocked(ctx, sources, "", predicate); picked != nil && picked.auth != nil {
		return picked.auth
	}

	priorityReady, okPriority := shard.highestReadyPriorityLocked(preferWebsocket, predicate)
	if !okPriority {
		return nil
	}

	source, okSource := shard.readySourceLocked(providerKey, preferWebsocket, priorityReady)
	if okSource {
		if picked, _, _ := s.pickSmartWeeklyRankedLocked(ctx, []schedulerReadySource{source}, "", predicate); picked != nil && picked.auth != nil {
			return picked.auth
		}
	}

	return shard.pickReadyAtPriorityLocked(preferWebsocket, priorityReady, schedulerStrategyRoundRobin, predicate)
}

func (s *authScheduler) pickMixedSmartWeeklyLocked(ctx context.Context, providers []string, candidateShards []*modelScheduler, modelKey string, predicate func(*scheduledAuth) bool) (*Auth, string) {
	if len(providers) == 0 {
		return nil, ""
	}

	allSources := make([]schedulerReadySource, 0, len(providers))
	bestSources := make([]schedulerReadySource, 0, len(providers))
	bestPriority := 0
	hasCandidate := false

	for providerIndex, providerKey := range providers {
		if providerIndex >= len(candidateShards) {
			break
		}
		shard := candidateShards[providerIndex]
		if shard == nil {
			continue
		}
		allSources = append(allSources, shard.readySourcesLocked(providerKey, false)...)

		priorityReady, okPriority := shard.highestReadyPriorityLocked(false, predicate)
		if !okPriority {
			continue
		}
		source, okSource := shard.readySourceLocked(providerKey, false, priorityReady)
		if !okSource {
			continue
		}
		switch {
		case !hasCandidate || priorityReady > bestPriority:
			bestPriority = priorityReady
			hasCandidate = true
			bestSources = bestSources[:0]
			bestSources = append(bestSources, source)
		case priorityReady == bestPriority:
			bestSources = append(bestSources, source)
		}
	}

	cursorKey := strings.Join(providers, ",") + ":" + canonicalModelKey(modelKey)
	if picked, providerKey := s.pickSmartWeeklyWarmupLocked(ctx, allSources, cursorKey, predicate); picked != nil && picked.auth != nil {
		return picked.auth, providerKey
	}

	if hasCandidate {
		if picked, providerKey, _ := s.pickSmartWeeklyRankedLocked(ctx, bestSources, cursorKey, predicate); picked != nil && picked.auth != nil {
			return picked.auth, providerKey
		}
	}

	return nil, ""
}

func (s *authScheduler) pickSmartWeeklyWarmupLocked(ctx context.Context, sources []schedulerReadySource, cursorKey string, predicate func(*scheduledAuth) bool) (*scheduledAuth, string) {
	snapshots := s.weeklySnapshotsForSourcesLocked(ctx, sources, predicate)
	if len(snapshots) == 0 {
		return nil, ""
	}

	now := time.Now().UTC()
	topSet := make(map[string]struct{})
	bestDueAt := time.Time{}
	bestResetAt := time.Time{}

	for _, source := range sources {
		if source.view == nil {
			continue
		}
		for _, entry := range source.view.flat {
			if predicate != nil && !predicate(entry) {
				continue
			}
			snapshot, ok := snapshots[entry.auth.ID]
			if !ok {
				continue
			}
			dueAt, okDue := s.pendingWarmupDueLocked(snapshot, now)
			if !okDue {
				continue
			}
			switch {
			case len(topSet) == 0 || dueAt.Before(bestDueAt) || (dueAt.Equal(bestDueAt) && snapshot.ResetAt.Before(bestResetAt)):
				bestDueAt = dueAt
				bestResetAt = snapshot.ResetAt
				clear(topSet)
				topSet[entry.auth.ID] = struct{}{}
			case dueAt.Equal(bestDueAt) && snapshot.ResetAt.Equal(bestResetAt):
				topSet[entry.auth.ID] = struct{}{}
			}
		}
	}

	if len(topSet) == 0 {
		return nil, ""
	}

	picked, providerKey := s.pickFromTopSetLocked(sources, topSet, cursorKey)
	if picked == nil || picked.auth == nil {
		return nil, ""
	}
	if snapshot, ok := snapshots[picked.auth.ID]; ok {
		s.consumeWarmupLocked(snapshot)
	}
	return picked, providerKey
}

func (s *authScheduler) pickSmartWeeklyRankedLocked(ctx context.Context, sources []schedulerReadySource, cursorKey string, predicate func(*scheduledAuth) bool) (*scheduledAuth, string, bool) {
	snapshots := s.weeklySnapshotsForSourcesLocked(ctx, sources, predicate)
	if len(snapshots) == 0 {
		return nil, "", false
	}

	candidates := make([]smartWeeklyCandidate, 0, len(snapshots))
	for _, source := range sources {
		if source.view == nil {
			continue
		}
		for _, entry := range source.view.flat {
			if predicate != nil && !predicate(entry) {
				continue
			}
			snapshot, ok := snapshots[entry.auth.ID]
			if !ok {
				continue
			}
			candidates = append(candidates, smartWeeklyCandidate{
				authID:         entry.auth.ID,
				remainingRatio: snapshot.RemainingRatio,
				resetAt:        snapshot.ResetAt,
			})
		}
	}
	if len(candidates) == 0 {
		return nil, "", false
	}

	threshold := s.smartWeekly.protectionThresholdRatio
	hasUnprotected := false
	for _, candidate := range candidates {
		if candidate.remainingRatio > threshold {
			hasUnprotected = true
			break
		}
	}

	topSet := s.buildSmartWeeklyTopSetLocked(candidates, hasUnprotected, threshold)
	if len(topSet) == 0 {
		return nil, "", true
	}

	picked, providerKey := s.pickFromTopSetLocked(sources, topSet, cursorKey)
	if picked == nil {
		return nil, "", true
	}
	return picked, providerKey, true
}

func (s *authScheduler) buildSmartWeeklyTopSetLocked(candidates []smartWeeklyCandidate, hasUnprotected bool, threshold float64) map[string]struct{} {
	if len(candidates) == 0 {
		return nil
	}

	filtered := make([]smartWeeklyCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if hasUnprotected && candidate.remainingRatio <= threshold {
			continue
		}
		filtered = append(filtered, candidate)
	}
	if len(filtered) == 0 {
		return nil
	}

	if s == nil || s.smartWeekly.maxAuthCount < 0 {
		return buildSmartWeeklyBestRankedSet(filtered)
	}

	if s.smartWeekly.maxAuthCount == 0 {
		topSet := make(map[string]struct{}, len(filtered))
		for _, candidate := range filtered {
			topSet[candidate.authID] = struct{}{}
		}
		return topSet
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		if !filtered[i].resetAt.Equal(filtered[j].resetAt) {
			return filtered[i].resetAt.Before(filtered[j].resetAt)
		}
		if filtered[i].remainingRatio != filtered[j].remainingRatio {
			return filtered[i].remainingRatio > filtered[j].remainingRatio
		}
		return filtered[i].authID < filtered[j].authID
	})

	topSet := make(map[string]struct{}, minInt(s.smartWeekly.maxAuthCount, len(filtered)))
	for _, candidate := range filtered {
		if _, ok := topSet[candidate.authID]; ok {
			continue
		}
		topSet[candidate.authID] = struct{}{}
		if len(topSet) >= s.smartWeekly.maxAuthCount {
			break
		}
	}
	return topSet
}

func buildSmartWeeklyBestRankedSet(candidates []smartWeeklyCandidate) map[string]struct{} {
	topSet := make(map[string]struct{})
	best := smartWeeklyCandidate{}
	found := false
	for _, candidate := range candidates {
		switch {
		case !found || candidate.resetAt.Before(best.resetAt) || (candidate.resetAt.Equal(best.resetAt) && candidate.remainingRatio > best.remainingRatio):
			best = candidate
			found = true
			clear(topSet)
			topSet[candidate.authID] = struct{}{}
		case candidate.resetAt.Equal(best.resetAt) && candidate.remainingRatio == best.remainingRatio:
			topSet[candidate.authID] = struct{}{}
		}
	}
	return topSet
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func (s *authScheduler) pickFromTopSetLocked(sources []schedulerReadySource, topSet map[string]struct{}, cursorKey string) (*scheduledAuth, string) {
	if len(topSet) == 0 || len(sources) == 0 {
		return nil, ""
	}

	match := func(entry *scheduledAuth) bool {
		if entry == nil || entry.auth == nil {
			return false
		}
		_, ok := topSet[entry.auth.ID]
		return ok
	}

	start := 0
	if cursorKey != "" && len(sources) > 0 {
		start = s.mixedCursors[cursorKey] % len(sources)
	}
	for offset := 0; offset < len(sources); offset++ {
		sourceIndex := (start + offset) % len(sources)
		source := sources[sourceIndex]
		if source.view == nil {
			continue
		}
		picked := source.view.pickRoundRobin(match)
		if picked == nil {
			continue
		}
		if cursorKey != "" {
			s.mixedCursors[cursorKey] = sourceIndex + 1
		}
		return picked, source.providerKey
	}

	return nil, ""
}

func (s *authScheduler) weeklySnapshotsForSourcesLocked(ctx context.Context, sources []schedulerReadySource, predicate func(*scheduledAuth) bool) map[string]WeeklyQuotaSnapshot {
	if s == nil || s.weeklyQuotaProvider == nil || len(sources) == 0 {
		return nil
	}

	authIDs := make([]string, 0)
	seen := make(map[string]struct{})
	for _, source := range sources {
		if source.view == nil {
			continue
		}
		for _, entry := range source.view.flat {
			if entry == nil || entry.auth == nil {
				continue
			}
			if predicate != nil && !predicate(entry) {
				continue
			}
			authID := strings.TrimSpace(entry.auth.ID)
			if authID == "" {
				continue
			}
			if _, ok := seen[authID]; ok {
				continue
			}
			seen[authID] = struct{}{}
			authIDs = append(authIDs, authID)
		}
	}
	if len(authIDs) == 0 {
		return nil
	}

	raw := s.weeklyQuotaProvider.WeeklyQuotaSnapshots(ctx, authIDs)
	if len(raw) == 0 {
		return nil
	}

	out := make(map[string]WeeklyQuotaSnapshot, len(raw))
	for authID, snapshot := range raw {
		if strings.TrimSpace(snapshot.AuthID) == "" {
			snapshot.AuthID = authID
		}
		normalized, ok := normalizeWeeklyQuotaSnapshot(snapshot)
		if !ok {
			continue
		}
		out[normalized.AuthID] = normalized
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeWeeklyQuotaSnapshot(snapshot WeeklyQuotaSnapshot) (WeeklyQuotaSnapshot, bool) {
	snapshot.AuthID = strings.TrimSpace(snapshot.AuthID)
	if snapshot.AuthID == "" {
		return WeeklyQuotaSnapshot{}, false
	}

	if math.IsNaN(snapshot.RemainingRatio) || math.IsInf(snapshot.RemainingRatio, 0) {
		return WeeklyQuotaSnapshot{}, false
	}
	switch {
	case snapshot.RemainingRatio < 0:
		snapshot.RemainingRatio = 0
	case snapshot.RemainingRatio > 1:
		snapshot.RemainingRatio = 1
	}

	if snapshot.ResetAt.IsZero() {
		return WeeklyQuotaSnapshot{}, false
	}
	snapshot.ResetAt = snapshot.ResetAt.UTC()
	snapshot.ObservedAt = snapshot.ObservedAt.UTC()
	return snapshot, true
}

func (s *authScheduler) pendingWarmupDueLocked(snapshot WeeklyQuotaSnapshot, now time.Time) (time.Time, bool) {
	state := s.observeWarmupLocked(snapshot, now)
	if state == nil || state.warmupConsumed {
		return time.Time{}, false
	}
	if state.warmupResetAt.IsZero() || !state.warmupResetAt.Equal(snapshot.ResetAt) {
		return time.Time{}, false
	}
	if state.warmupDueAt.IsZero() || now.Before(state.warmupDueAt) {
		return time.Time{}, false
	}
	return state.warmupDueAt, true
}

func (s *authScheduler) observeWarmupLocked(snapshot WeeklyQuotaSnapshot, now time.Time) *weeklyWarmupState {
	if s == nil {
		return nil
	}
	if s.weeklyWarmups == nil {
		s.weeklyWarmups = make(map[string]*weeklyWarmupState)
	}
	authID := strings.TrimSpace(snapshot.AuthID)
	if authID == "" || snapshot.ResetAt.IsZero() {
		return nil
	}
	state := s.weeklyWarmups[authID]
	if state == nil {
		state = &weeklyWarmupState{}
		s.weeklyWarmups[authID] = state
	}

	resetAt := snapshot.ResetAt.UTC()
	if state.lastSeenResetAt.IsZero() {
		state.lastSeenResetAt = resetAt
		return state
	}
	switch {
	case resetAt.After(state.lastSeenResetAt):
		observedAt := snapshot.ObservedAt
		if observedAt.IsZero() {
			observedAt = now
		}
		state.lastSeenResetAt = resetAt
		state.warmupResetAt = resetAt
		state.warmupDueAt = observedAt.Add(s.smartWeekly.warmupDelay)
		state.warmupConsumed = false
	case resetAt.Before(state.lastSeenResetAt):
		state.lastSeenResetAt = resetAt
		state.warmupResetAt = time.Time{}
		state.warmupDueAt = time.Time{}
		state.warmupConsumed = false
	}
	return state
}

func (s *authScheduler) consumeWarmupLocked(snapshot WeeklyQuotaSnapshot) {
	if s == nil {
		return
	}
	state := s.weeklyWarmups[strings.TrimSpace(snapshot.AuthID)]
	if state == nil {
		return
	}
	if !state.warmupResetAt.IsZero() && state.warmupResetAt.Equal(snapshot.ResetAt) {
		state.warmupConsumed = true
	}
}
