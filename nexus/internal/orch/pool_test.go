package orch

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
)

func newRunningPoolTestRecord(t *testing.T, m *ServerManager, line, port int, args []string, players, maxPlayers byte) *serverRecord {
	t.Helper()

	rec := m.registerServerLaunch(serverLaunch{
		Line:   line,
		Binary: "nqserver",
		Args:   append([]string(nil), args...),
	})
	m.updatePort(rec, port)
	m.updateSearchPath(rec, []string{"id1"})
	m.SetServerRunningForTest(rec, NewTestServer(port))
	m.SetServerInfoForTest(rec, "pool-"+strconv.Itoa(line), "dm6", players, maxPlayers)
	m.mu.Lock()
	rec.LastSeen = time.Now()
	m.mu.Unlock()
	return rec
}

func attachPoolBackendForTest(t *testing.T, m *ServerManager, seedID int, backend *serverRecord) int {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	pool := m.poolByServerID[seedID]
	if pool == nil {
		t.Fatalf("seed server %d has no pool", seedID)
	}
	if _, ok := m.poolByServerID[backend.id]; !ok {
		m.poolByServerID[backend.id] = pool
	}
	if !slices.Contains(pool.BackendServerIDs, backend.id) {
		pool.BackendServerIDs = append(pool.BackendServerIDs, backend.id)
	}
	if pool.backendState == nil {
		pool.backendState = make(map[int]*poolBackendState)
	}

	if seedState := pool.backendState[seedID]; seedState == nil {
		pool.backendState[seedID] = newPoolBackendState(poolBackendLifecycleActive)
	} else {
		transitionPoolBackendLifecycle(seedState, poolBackendLifecycleActive)
		seedState.ZeroPollStreak = 0
	}
	if backendState := pool.backendState[backend.id]; backendState == nil {
		pool.backendState[backend.id] = newPoolBackendState(poolBackendLifecycleActive)
	} else {
		transitionPoolBackendLifecycle(backendState, poolBackendLifecycleActive)
		backendState.ZeroPollStreak = 0
	}
	m.refreshPoolSnapshotLocked(pool)
	return pool.ListenPort
}

func TestRegisterPoolSeed_OnlyPortZeroLaunches(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	recDynamic := m.registerServerLaunch(serverLaunch{Line: 0, Binary: "nqserver", Args: []string{"-dedicated", "-port", "0"}})
	recStatic := m.registerServerLaunch(serverLaunch{Line: 1, Binary: "nqserver", Args: []string{"-dedicated", "-port", "26000"}})

	if err := m.registerPoolSeed(recDynamic); err != nil {
		t.Fatalf("register dynamic seed: %v", err)
	}
	if err := m.registerPoolSeed(recStatic); err != nil {
		t.Fatalf("register static seed: %v", err)
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(m.poolsByID) != 1 {
		t.Fatalf("pool count = %d, want 1", len(m.poolsByID))
	}
	if m.poolByServerID[recDynamic.id] == nil {
		t.Fatalf("expected dynamic server to be pooled")
	}
	if m.poolByServerID[recStatic.id] != nil {
		t.Fatalf("expected static server to stay unpooled")
	}
}

func TestPoolRouting_PicksLeastLoadedBackend(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	// seed has 8/16 players, replica has 1/16 — replica is less loaded.
	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 8, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 1, 16)
	attachPoolBackendForTest(t, m, seed.id, replica)

	m.mu.Lock()
	pool := m.poolByServerID[seed.id]
	port, ok := m.pickPoolBackendLocked(pool)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination")
	}
	if port != 26001 {
		t.Fatalf("routed backend = %d, want 26001 (less loaded)", port)
	}

	// After replica fills up, seed should be selected.
	m.SetServerInfoForTest(replica, replica.Hostname, replica.MapName, 16, 16)

	m.mu.Lock()
	port, ok = m.pickPoolBackendLocked(pool)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination after replica full")
	}
	if port != 26000 {
		t.Fatalf("routed backend = %d, want 26000 after replica full", port)
	}
}

func TestPoolRouting_UsesDrainingBackendWhenOnlyFreeSlotsRemain(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 16, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachPoolBackendForTest(t, m, seed.id, replica)

	m.mu.Lock()
	pool := m.poolByServerID[replica.id]
	state := pool.backendState[replica.id]
	if state == nil {
		state = newPoolBackendState(poolBackendLifecycleActive)
		pool.backendState[replica.id] = state
	}
	transitionPoolBackendLifecycle(state, poolBackendLifecycleDraining)
	replicaDraining := state != nil && state.Lifecycle == poolBackendLifecycleDraining
	routed, ok := m.pickPoolBackendLocked(pool)
	m.mu.Unlock()

	if !replicaDraining {
		t.Fatalf("expected replica to be marked draining")
	}
	if !ok {
		t.Fatalf("expected routed destination")
	}
	if routed != 26001 {
		t.Fatalf("routed backend = %d, want 26001", routed)
	}
}

func TestPoolRouting_PrefersActiveLowerLoadOverDraining(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	// seed active with 8/16, replica draining with 0/16.
	// Active backend with free slots should be preferred.
	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 8, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachPoolBackendForTest(t, m, seed.id, replica)

	m.mu.Lock()
	pool := m.poolByServerID[replica.id]
	state := pool.backendState[replica.id]
	if state == nil {
		state = newPoolBackendState(poolBackendLifecycleActive)
		pool.backendState[replica.id] = state
	}
	transitionPoolBackendLifecycle(state, poolBackendLifecycleDraining)
	port, ok := m.pickPoolBackendLocked(pool)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination")
	}
	if port != 26000 {
		t.Fatalf("routed backend = %d, want 26000 (active over draining)", port)
	}
}

func TestPoolRouting_AllDrainingBackendsRemainRoutable(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 0, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachPoolBackendForTest(t, m, seed.id, replica)

	m.mu.Lock()
	pool := m.poolByServerID[seed.id]
	for _, serverID := range pool.BackendServerIDs {
		state := pool.backendState[serverID]
		if state == nil {
			state = newPoolBackendState(poolBackendLifecycleDraining)
			pool.backendState[serverID] = state
		}
		transitionPoolBackendLifecycle(state, poolBackendLifecycleDraining)
		state.ZeroPollStreak = 0
	}
	routed, ok := m.pickPoolBackendLocked(pool)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination")
	}
	if routed != 26000 && routed != 26001 {
		t.Fatalf("routed backend = %d, want one of [26000 26001]", routed)
	}
}

func TestPoolRouting_SkipsWarmingBackendForNewSessions(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 4, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachPoolBackendForTest(t, m, seed.id, replica)

	m.mu.Lock()
	pool := m.poolByServerID[replica.id]
	state := pool.backendState[replica.id]
	if state == nil {
		state = newPoolBackendState(poolBackendLifecycleWarming)
		pool.backendState[replica.id] = state
	}
	transitionPoolBackendLifecycle(state, poolBackendLifecycleWarming)
	routed, ok := m.pickPoolBackendLocked(pool)
	m.mu.Unlock()

	if !ok {
		t.Fatalf("expected routed destination")
	}
	if routed != 26000 {
		t.Fatalf("routed backend = %d, want 26000 while replica is warming", routed)
	}
}

func TestPoolRouting_RecordsDemandOnSlist(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 4, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	attachPoolBackendForTest(t, m, seed.id, seed)

	// No slist yet — demand should be zero.
	m.mu.RLock()
	pool := m.poolByServerID[seed.id]
	beforeDemand := len(pool.joinDemandAt)
	m.mu.RUnlock()
	if beforeDemand != 0 {
		t.Fatalf("demand before slist = %d, want 0", beforeDemand)
	}

	// Slist records one demand event per successful backend pick.
	entries := snapshotForSlist(m)
	if len(entries) == 0 {
		t.Fatalf("expected at least one slist entry")
	}

	m.mu.RLock()
	afterDemand := len(pool.joinDemandAt)
	m.mu.RUnlock()
	if afterDemand != 1 {
		t.Fatalf("demand after slist = %d, want 1", afterDemand)
	}
}

func TestPoolRouting_FailedSlistDoesNotRecordDemand(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	// Register pool seed but don't set it running — no routable backend.
	seed := m.registerServerLaunch(serverLaunch{Line: 0, Binary: "nqserver", Args: []string{"-dedicated", "-port", "0"}})
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}

	entries := snapshotForSlist(m)
	if len(entries) != 0 {
		t.Fatalf("expected no slist entries with no running backends, got %d", len(entries))
	}

	m.mu.RLock()
	pool := m.poolByServerID[seed.id]
	demandCount := len(pool.joinDemandAt)
	m.mu.RUnlock()
	if demandCount != 0 {
		t.Fatalf("failed slist should not record demand, got %d", demandCount)
	}
}

func TestResolveRCONTargetPort_DirectBackendPort(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 0, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}

	// RCON to the actual backend port works directly — no proxy resolution.
	_, err := m.ExecServerCmd(26000, "status", "")
	// Server has no real console so we expect "server console unavailable", not "unknown server".
	if err == nil {
		t.Fatalf("expected error (no console), got nil")
	}
	if strings.Contains(err.Error(), "unknown server") {
		t.Fatalf("ExecServerCmd should find backend 26000, got: %v", err)
	}

	// Out-of-range port should return unknown server.
	if _, err := m.ExecServerCmd(0, "status", ""); err == nil || !strings.Contains(err.Error(), "unknown server") {
		t.Fatalf("ExecServerCmd invalid port = %v, want unknown server", err)
	}

	// Unknown valid port should return unknown server.
	if _, err := m.ExecServerCmd(9999, "status", ""); err == nil || !strings.Contains(err.Error(), "unknown server") {
		t.Fatalf("ExecServerCmd unknown port = %v, want unknown server", err)
	}

}

func setPoolDemandEventsForTest(t *testing.T, m *ServerManager, seedServerID int, count int, now time.Time) {
	t.Helper()

	m.mu.Lock()
	defer m.mu.Unlock()

	pool := m.poolByServerID[seedServerID]
	if pool == nil {
		t.Fatalf("seed server %d has no pool", seedServerID)
	}
	pool.joinDemandAt = pool.joinDemandAt[:0]
	for i := 0; i < count; i++ {
		pool.joinDemandAt = append(pool.joinDemandAt, now)
	}
}

func TestPoolScaleUp_UsesDemandHeadroom(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	m.SetPoolMaxSize(10)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 10, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	// Aggregate headroom is 22, demand is low, so no scale-up.
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	attachPoolBackendForTest(t, m, seed.id, replica)
	setPoolDemandEventsForTest(t, m, seed.id, 2, time.Now())

	m.mu.Lock()
	pool := m.poolByServerID[seed.id]
	scaleUpPoolID, _ := m.decidePoolActionsLocked(pool, time.Now())
	m.mu.Unlock()
	if scaleUpPoolID != -1 {
		t.Fatalf("scale-up pool id = %d, want no scale-up", scaleUpPoolID)
	}
}

func TestPoolScaleUp_TriggersOnDemandHeadroom(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	m.SetPoolMaxSize(10)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 14, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 13, 16)
	attachPoolBackendForTest(t, m, seed.id, replica)
	// 20 joins in a 30s window => needed headroom ~= 12 slots.
	setPoolDemandEventsForTest(t, m, seed.id, 20, time.Now())

	m.mu.Lock()
	pool := m.poolByServerID[seed.id]
	scaleUpPoolID, _ := m.decidePoolActionsLocked(pool, time.Now())
	scalingUp := pool.ScaleUpInFlight
	m.mu.Unlock()

	if scaleUpPoolID != pool.PoolID {
		t.Fatalf("scale-up pool id = %d, want %d", scaleUpPoolID, pool.PoolID)
	}
	if !scalingUp {
		t.Fatalf("expected pool to enter scaling-up lifecycle")
	}
}

func TestPoolScaleUp_HonorsPoolSizeCap(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	m.SetPoolMaxSize(1)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 13, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	setPoolDemandEventsForTest(t, m, seed.id, 10, time.Now())

	m.mu.Lock()
	pool := m.poolByServerID[seed.id]
	scaleUpPoolID, _ := m.decidePoolActionsLocked(pool, time.Now())
	m.mu.Unlock()

	if scaleUpPoolID != -1 {
		t.Fatalf("scale-up pool id = %d, want no scale-up with POOL_SIZE=1", scaleUpPoolID)
	}
}

func TestPoolScaleUp_NoScaleUpWhileWarmingNoCapacityInfo(t *testing.T) {
	// Seed is running but hasn't yet received server info (MaxPlayers==0, LastSeen
	// zero). Scale-up must not fire based on the apparent-but-spurious 0 free slots.
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	m.SetPoolMaxSize(10) // allow scale-up so only the AggregateMaxUsers guard blocks it
	t.Cleanup(m.closePoolRegistry)

	seed := m.registerServerLaunch(serverLaunch{
		Line: 0, Binary: "nqserver", Args: []string{"-dedicated", "-port", "0"},
	})
	m.updatePort(seed, 26000)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	m.SetServerRunningForTest(seed, NewTestServer(26000))
	// No SetServerInfoForTest / no LastSeen: AggregateMaxUsers remains 0.

	m.mu.Lock()
	pool := m.poolByServerID[seed.id]
	scaleUpPoolID, _ := m.decidePoolActionsLocked(pool, time.Now())
	m.mu.Unlock()

	if scaleUpPoolID != -1 {
		t.Fatalf("scale-up pool id = %d, want -1 (no scale-up while seed is warming)", scaleUpPoolID)
	}
}

func TestReconcileAllPools_AttemptsScaleUpWithoutServerInfoEvents(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	m.SetPoolMaxSize(10)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 16, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}

	m.mu.Lock()
	beforeNextServerID := m.nextServerID
	pool := m.poolByServerID[seed.id]
	beforeBackends := len(pool.BackendServerIDs)
	m.mu.Unlock()

	m.reconcileAllPools()

	m.mu.Lock()
	afterNextServerID := m.nextServerID
	pool = m.poolByServerID[seed.id]
	afterBackends := len(pool.BackendServerIDs)
	scaleUpInFlight := pool.ScaleUpInFlight
	m.mu.Unlock()

	if afterNextServerID != beforeNextServerID+1 {
		t.Fatalf("next server id = %d, want %d (one scale-up attempt)", afterNextServerID, beforeNextServerID+1)
	}
	if afterBackends != beforeBackends {
		t.Fatalf("backend count = %d, want %d after failed start cleanup", afterBackends, beforeBackends)
	}
	if scaleUpInFlight {
		t.Fatalf("scale-up flag should clear after reconcile attempt")
	}
}

func TestPoolDrain_DoesNotMarkEmptyBackendWhenHeadroomIsNeeded(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	m.SetPoolMaxSize(10)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 1, 1)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 1)
	attachPoolBackendForTest(t, m, seed.id, replica)

	m.mu.Lock()
	pool := m.poolByServerID[seed.id]
	scaleUpPoolID, despawnServerID := m.decidePoolActionsLocked(pool, time.Now())
	state := pool.backendState[replica.id]
	draining := state != nil && state.Lifecycle == poolBackendLifecycleDraining
	zeroStreak := 0
	if state != nil {
		zeroStreak = state.ZeroPollStreak
	}
	m.mu.Unlock()

	if draining {
		t.Fatalf("replica should remain joinable when pool headroom is below target")
	}
	if zeroStreak != 0 {
		t.Fatalf("zero-player streak = %d, want 0", zeroStreak)
	}
	if despawnServerID != -1 {
		t.Fatalf("despawn server id = %d, want no despawn", despawnServerID)
	}
	if scaleUpPoolID != pool.PoolID {
		t.Fatalf("scale-up pool id = %d, want %d", scaleUpPoolID, pool.PoolID)
	}
}

func TestPoolDemand_DecaysOutsideWindow(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 4, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	oldNow := time.Now().Add(-demandWindow - time.Second)
	setPoolDemandEventsForTest(t, m, seed.id, 10, oldNow)

	m.mu.Lock()
	pool := m.poolByServerID[seed.id]
	needed := poolNeededHeadroomLocked(pool, time.Now())
	remaining := len(pool.joinDemandAt)
	m.mu.Unlock()

	if needed != demandMinFreeSlots {
		t.Fatalf("needed headroom after decay = %d, want %d", needed, demandMinFreeSlots)
	}
	if remaining != 0 {
		t.Fatalf("expected all old demand events pruned, got %d", remaining)
	}
}

