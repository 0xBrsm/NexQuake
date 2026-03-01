package orch

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"time"
)

type poolBackendCandidate struct {
	listenPort int
	players    int
	maxPlayers int
}

// SetPoolMaxSize sets the per-pool instance cap used by autoscaling.
// Values below 1 are clamped to 1.
func (m *ServerManager) SetPoolMaxSize(size int) {
	if size < 1 {
		size = 1
	}
	m.mu.Lock()
	m.poolMaxSize = size
	m.mu.Unlock()
}

func (m *ServerManager) poolRoutableCandidatesLocked(pool *serverPool, allowDraining bool) []poolBackendCandidate {
	if pool == nil {
		return nil
	}

	out := make([]poolBackendCandidate, 0, len(pool.BackendServerIDs))
	for _, serverID := range pool.BackendServerIDs {
		rec := m.serversByID[serverID]
		if !m.serverRecordRunningLocked(rec) {
			continue
		}
		state := pool.backendState[serverID]
		if !backendAllowsPoolRouting(state, allowDraining) {
			continue
		}

		port := recordListenPort(rec)
		if port < 1 || port > 65535 {
			continue
		}

		out = append(out, poolBackendCandidate{
			listenPort: port,
			players:    int(rec.Players),
			maxPlayers: int(rec.MaxPlayers),
		})
	}

	return out
}

func (m *ServerManager) poolRoutableCandidateCountLocked(pool *serverPool, allowDraining bool) int {
	if pool == nil {
		return 0
	}

	count := 0
	for _, serverID := range pool.BackendServerIDs {
		rec := m.serversByID[serverID]
		if !m.serverRecordRunningLocked(rec) {
			continue
		}
		state := pool.backendState[serverID]
		if !backendAllowsPoolRouting(state, allowDraining) {
			continue
		}
		port := recordListenPort(rec)
		if port < 1 || port > 65535 {
			continue
		}
		count++
	}
	return count
}

func poolCandidatesWithFreeSlots(candidates []poolBackendCandidate) []poolBackendCandidate {
	if len(candidates) == 0 {
		return nil
	}
	out := make([]poolBackendCandidate, 0, len(candidates))
	for _, cand := range candidates {
		// maxplayers=0 can appear briefly before first poll; treat as usable.
		if cand.maxPlayers <= 0 || cand.players < cand.maxPlayers {
			out = append(out, cand)
		}
	}
	return out
}

func (m *ServerManager) pickPoolBackendLocked(pool *serverPool) (int, bool) {
	candidates := m.poolRoutableCandidatesLocked(pool, false)
	if free := poolCandidatesWithFreeSlots(candidates); len(free) > 0 {
		candidates = free
	} else {
		// If every non-draining backend is full (or there are none), also
		// consider draining backends — prefer those with free slots.
		all := m.poolRoutableCandidatesLocked(pool, true)
		if freeAll := poolCandidatesWithFreeSlots(all); len(freeAll) > 0 {
			candidates = freeAll
		} else if len(all) > 0 {
			candidates = all
		}
	}

	if len(candidates) == 0 {
		return 0, false
	}

	best := []poolBackendCandidate{candidates[0]}
	for _, cand := range candidates[1:] {
		bestRef := best[0]
		candMax := max(cand.maxPlayers, 1)
		bestMax := max(bestRef.maxPlayers, 1)
		left := cand.players * bestMax
		right := bestRef.players * candMax
		switch {
		case left < right || (left == right && cand.players < bestRef.players):
			best = best[:1]
			best[0] = cand
		case left == right && cand.players == bestRef.players:
			best = append(best, cand)
		}
	}

	idx := 0
	if len(best) > 1 {
		idx = pool.RoundRobinCursor % len(best)
	}
	selected := best[idx]
	pool.RoundRobinCursor++
	return selected.listenPort, true
}

func (m *ServerManager) poolRunningCountLocked(pool *serverPool) int {
	if pool == nil {
		return 0
	}
	count := 0
	for _, serverID := range pool.BackendServerIDs {
		if m.serverRecordRunningLocked(m.serversByID[serverID]) {
			count++
		}
	}
	return count
}

func prunePoolDemandLocked(pool *serverPool, now time.Time) {
	if pool == nil || len(pool.joinDemandAt) == 0 {
		return
	}
	cutoff := now.Add(-demandWindow)
	n := 0
	for _, ts := range pool.joinDemandAt {
		if ts.Before(cutoff) {
			continue
		}
		pool.joinDemandAt[n] = ts
		n++
	}
	pool.joinDemandAt = pool.joinDemandAt[:n]
}

func notePoolDemandLocked(pool *serverPool, now time.Time) {
	if pool == nil {
		return
	}
	prunePoolDemandLocked(pool, now)
	pool.joinDemandAt = append(pool.joinDemandAt, now)
}

func poolNeededHeadroomLocked(pool *serverPool, now time.Time) int {
	if pool == nil {
		return demandMinFreeSlots
	}
	prunePoolDemandLocked(pool, now)
	if demandWindow <= 0 {
		return demandMinFreeSlots
	}

	joinRPS := float64(len(pool.joinDemandAt)) / demandWindow.Seconds()
	dynamicHeadroom := int(math.Ceil(joinRPS * demandSpawnReady.Seconds() * demandSafetyFactor))
	return max(demandMinFreeSlots, dynamicHeadroom)
}

func (m *ServerManager) decidePoolActionsLocked(pool *serverPool, now time.Time) (scaleUpPoolID int, despawnServerID int) {
	scaleUpPoolID = -1
	despawnServerID = -1
	if pool == nil {
		return scaleUpPoolID, despawnServerID
	}
	if !pool.Autoscales {
		m.refreshPoolSnapshotLocked(pool)
		return scaleUpPoolID, despawnServerID
	}

	m.refreshPoolSnapshotLocked(pool)

	freeSlots := 0
	if pool.AggregateMaxUsers > 0 {
		freeSlots = int(pool.AggregateMaxUsers) - int(pool.AggregateUsers)
		if freeSlots < 0 {
			freeSlots = 0
		}
	}
	neededHeadroom := poolNeededHeadroomLocked(pool, now)
	runningCount := int(pool.AggregateInstances)
	activeRoutableCount := m.poolRoutableCandidateCountLocked(pool, false)

	for _, serverID := range pool.BackendServerIDs {
		rec := m.serversByID[serverID]
		if !m.serverRecordRunningLocked(rec) {
			continue
		}
		state := m.ensurePoolBackendStateLocked(pool, serverID)

		setLifecycle := func(next poolBackendLifecycle) {
			prev := state.Lifecycle
			if prev == next {
				return
			}
			transitionPoolBackendLifecycle(state, next)
			if prev == poolBackendLifecycleActive {
				activeRoutableCount--
			}
			if next == poolBackendLifecycleActive {
				activeRoutableCount++
			}
		}

		if state.Lifecycle == poolBackendLifecycleTerminating {
			continue
		}
		if rec.LastSeen.IsZero() {
			setLifecycle(poolBackendLifecycleWarming)
			state.ZeroPollStreak = 0
			continue
		}
		if rec.Players > 0 {
			setLifecycle(poolBackendLifecycleActive)
			state.ZeroPollStreak = 0
			continue
		}

		backendFreeSlots := int(rec.MaxPlayers) - int(rec.Players)
		if backendFreeSlots < 1 {
			// MaxPlayers can be unknown briefly right after startup.
			backendFreeSlots = 1
		}

		canDrain := activeRoutableCount > 1 && freeSlots-backendFreeSlots >= neededHeadroom
		if pool.ScaleUpInFlight && state.Lifecycle != poolBackendLifecycleDraining {
			canDrain = false
		}
		if !canDrain {
			setLifecycle(poolBackendLifecycleActive)
			state.ZeroPollStreak = 0
			continue
		}

		setLifecycle(poolBackendLifecycleDraining)
		state.ZeroPollStreak++
		if state.ZeroPollStreak >= despawnZeroPolls && despawnServerID < 0 {
			setLifecycle(poolBackendLifecycleTerminating)
			state.ZeroPollStreak = 0
			despawnServerID = serverID
		}
	}

	if pool.AggregateMaxUsers > 0 &&
		freeSlots < neededHeadroom &&
		!pool.ScaleUpInFlight &&
		runningCount < max(1, m.poolMaxSize) &&
		(pool.LastScaleUpAt.IsZero() || now.Sub(pool.LastScaleUpAt) >= scaleUpCooldown) {
		pool.ScaleUpInFlight = true
		scaleUpPoolID = pool.PoolID
	}

	return scaleUpPoolID, despawnServerID
}

func forEachUniqueInt(values []int, fn func(int)) {
	seen := make(map[int]struct{}, len(values))
	for _, value := range values {
		if _, dup := seen[value]; dup {
			continue
		}
		seen[value] = struct{}{}
		fn(value)
	}
}

func (m *ServerManager) reconcilePools(poolIDs []int) {
	if len(poolIDs) == 0 {
		return
	}

	now := time.Now()
	scaleUps := make([]int, 0, len(poolIDs))
	despawns := make([]int, 0, len(poolIDs))

	m.mu.Lock()
	forEachUniqueInt(poolIDs, func(poolID int) {
		pool := m.poolsByID[poolID]
		if pool == nil {
			return
		}
		scaleUpPoolID, despawnServerID := m.decidePoolActionsLocked(pool, now)
		if scaleUpPoolID >= 0 {
			scaleUps = append(scaleUps, scaleUpPoolID)
		}
		if despawnServerID >= 0 {
			despawns = append(despawns, despawnServerID)
		}
	})
	m.mu.Unlock()

	forEachUniqueInt(scaleUps, m.launchPoolReplica)
	forEachUniqueInt(despawns, m.despawnPoolBackend)
}

func (m *ServerManager) reconcileAllPools() {
	m.mu.RLock()
	if len(m.poolsByID) == 0 {
		m.mu.RUnlock()
		return
	}
	poolIDs := make([]int, 0, len(m.poolsByID))
	for poolID := range m.poolsByID {
		poolIDs = append(poolIDs, poolID)
	}
	m.mu.RUnlock()
	m.reconcilePools(poolIDs)
}

func (m *ServerManager) launchPoolReplica(poolID int) {
	rec, err := m.registerPoolReplicaRecord(poolID)
	if err != nil {
		m.warnf("Pool %d scale-up failed: %v", poolID, err)
		return
	}
	if rec == nil {
		return
	}

	err = m.startRecord(rec)

	m.mu.Lock()
	pool := m.poolsByID[poolID]
	if pool != nil {
		pool.ScaleUpInFlight = false
		if err == nil {
			pool.LastScaleUpAt = time.Now()
			m.refreshPoolSnapshotLocked(pool)
		} else {
			m.removeServerRecordLocked(rec.id)
		}
	}
	m.mu.Unlock()

	if err != nil {
		m.warnf("Pool %d scale-up failed: %v", poolID, err)
		return
	}
	line := -1
	m.mu.RLock()
	if pool := m.poolByServerID[rec.id]; pool != nil {
		line = pool.Line
	}
	m.mu.RUnlock()
	if line >= 0 {
		m.infof("Pool %d line %d launched replica", poolID, line+1)
		return
	}
	m.infof("Pool %d launched replica", poolID)
}

func (m *ServerManager) registerPoolReplicaRecord(poolID int) (*serverRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	pool := m.poolsByID[poolID]
	if pool == nil {
		return nil, fmt.Errorf("unknown pool")
	}
	if !pool.ScaleUpInFlight {
		return nil, nil
	}
	if !pool.Autoscales {
		pool.ScaleUpInFlight = false
		return nil, nil
	}

	replicaID := m.nextServerID
	launch := cloneServerLaunch(pool.TemplateLaunch)
	launch.Line = -1
	launch.LogDir = fmt.Sprintf("replica-%d-%s-%s", replicaID, filepath.Base(launch.Binary), time.Now().UTC().Format("20060102T150405Z"))
	launch.Args = forceLaunchPortZero(launch.Args)

	rec := m.appendPoolBackendRecordLocked(pool, launch, poolBackendLifecycleWarming)

	return rec, nil
}

func (m *ServerManager) despawnPoolBackend(serverID int) {
	m.mu.RLock()
	pool := m.poolByServerID[serverID]
	rec := m.serversByID[serverID]
	if pool == nil || rec == nil || !m.serverRecordRunningLocked(rec) {
		m.mu.RUnlock()
		m.mu.Lock()
		if pool := m.poolByServerID[serverID]; pool != nil {
			m.setPoolBackendLifecycleLocked(pool, serverID, poolBackendLifecycleWarming, false)
		}
		m.mu.Unlock()
		return
	}
	if m.poolRunningCountLocked(pool) <= 1 {
		m.mu.RUnlock()
		m.mu.Lock()
		if pool := m.poolByServerID[serverID]; pool != nil {
			m.setPoolBackendLifecycleLocked(pool, serverID, poolBackendLifecycleActive, false)
		}
		m.mu.Unlock()
		return
	}
	srv := rec.Running
	poolID := pool.PoolID
	m.mu.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	err := m.stopServer(ctx, rec, srv, 2*time.Second, true)
	cancel()
	if err != nil {
		m.warnf("Pool %d despawn failed: %v", poolID, err)
	}

	m.mu.Lock()
	if pool := m.poolByServerID[serverID]; pool != nil {
		if err == nil {
			m.removeServerRecordLocked(serverID)
		} else {
			m.setPoolBackendLifecycleLocked(pool, serverID, poolBackendLifecycleActive, false)
		}
	}
	m.mu.Unlock()
}
