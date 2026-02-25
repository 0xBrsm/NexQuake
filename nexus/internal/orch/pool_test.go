package orch

import (
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
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

func TestPoolRouting_AffinityAndSourceRewrite(t *testing.T) {
	oldTTL := proxyAffinityTTL
	proxyAffinityTTL = 40 * time.Millisecond
	t.Cleanup(func() { proxyAffinityTTL = oldTTL })

	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 8, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 1, 16)
	proxyPort := attachPoolBackendForTest(t, m, seed.id, replica)

	router, _ := nqnet.NewTestRouter(false)
	t.Cleanup(router.Close)

	first, ok := m.ResolveDestinationPort(router, proxyPort)
	if !ok {
		t.Fatalf("expected routed destination")
	}
	if first != 26001 {
		t.Fatalf("first routed backend = %d, want 26001", first)
	}

	rewritten := m.RewriteSourcePort(router, 26001)
	if rewritten != proxyPort {
		t.Fatalf("rewritten source = %d, want proxy %d", rewritten, proxyPort)
	}
	if got := m.RewriteSourcePort(router, 30000); got != 30000 {
		t.Fatalf("accept-port rewrite should not trigger: got %d", got)
	}

	m.SetServerInfoForTest(seed, seed.Hostname, seed.MapName, 0, 16)
	m.SetServerInfoForTest(replica, replica.Hostname, replica.MapName, 12, 16)

	stillSticky, ok := m.ResolveDestinationPort(router, proxyPort)
	if !ok {
		t.Fatalf("expected sticky route")
	}
	if stillSticky != 26001 {
		t.Fatalf("sticky route backend = %d, want 26001", stillSticky)
	}

	time.Sleep(proxyAffinityTTL + 20*time.Millisecond)

	afterTTL, ok := m.ResolveDestinationPort(router, proxyPort)
	if !ok {
		t.Fatalf("expected routed destination after ttl")
	}
	if afterTTL != 26000 {
		t.Fatalf("post-ttl backend = %d, want 26000", afterTTL)
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
	proxyPort := attachPoolBackendForTest(t, m, seed.id, replica)

	m.mu.Lock()
	pool := m.poolByServerID[replica.id]
	state := pool.backendState[replica.id]
	if state == nil {
		state = newPoolBackendState(poolBackendLifecycleActive)
		pool.backendState[replica.id] = state
	}
	transitionPoolBackendLifecycle(state, poolBackendLifecycleDraining)
	replicaDraining := state != nil && state.Lifecycle == poolBackendLifecycleDraining
	m.mu.Unlock()
	if !replicaDraining {
		t.Fatalf("expected replica to be marked draining")
	}

	router, _ := nqnet.NewTestRouter(false)
	t.Cleanup(router.Close)

	routed, ok := m.ResolveDestinationPort(router, proxyPort)
	if !ok {
		t.Fatalf("expected routed destination")
	}
	if routed != 26001 {
		t.Fatalf("routed backend = %d, want 26001", routed)
	}
}

func TestPoolRouting_StickyAffinityPersistsForDrainingBackend(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 16, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	proxyPort := attachPoolBackendForTest(t, m, seed.id, replica)

	m.mu.Lock()
	pool := m.poolByServerID[replica.id]
	state := pool.backendState[replica.id]
	if state == nil {
		state = newPoolBackendState(poolBackendLifecycleActive)
		pool.backendState[replica.id] = state
	}
	transitionPoolBackendLifecycle(state, poolBackendLifecycleDraining)
	m.mu.Unlock()

	router, _ := nqnet.NewTestRouter(false)
	t.Cleanup(router.Close)

	first, ok := m.ResolveDestinationPort(router, proxyPort)
	if !ok {
		t.Fatalf("expected first routed destination")
	}
	if first != 26001 {
		t.Fatalf("first backend = %d, want 26001", first)
	}

	// Once a session is pinned to a backend, keep that affinity even if the
	// backend is marked draining so connect handshakes stay coherent.
	m.SetServerInfoForTest(seed, seed.Hostname, seed.MapName, 8, 16)

	stillSticky, ok := m.ResolveDestinationPort(router, proxyPort)
	if !ok {
		t.Fatalf("expected sticky route")
	}
	if stillSticky != 26001 {
		t.Fatalf("sticky backend = %d, want 26001", stillSticky)
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
	proxyPort := attachPoolBackendForTest(t, m, seed.id, replica)

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
	m.mu.Unlock()

	router, _ := nqnet.NewTestRouter(false)
	t.Cleanup(router.Close)

	routed, ok := m.ResolveDestinationPort(router, proxyPort)
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
	proxyPort := attachPoolBackendForTest(t, m, seed.id, replica)

	m.mu.Lock()
	pool := m.poolByServerID[replica.id]
	state := pool.backendState[replica.id]
	if state == nil {
		state = newPoolBackendState(poolBackendLifecycleWarming)
		pool.backendState[replica.id] = state
	}
	transitionPoolBackendLifecycle(state, poolBackendLifecycleWarming)
	m.mu.Unlock()

	router, _ := nqnet.NewTestRouter(false)
	t.Cleanup(router.Close)

	routed, ok := m.ResolveDestinationPort(router, proxyPort)
	if !ok {
		t.Fatalf("expected routed destination")
	}
	if routed != 26000 {
		t.Fatalf("routed backend = %d, want 26000 while replica is warming", routed)
	}
}

func TestPoolRouting_RecordsDemandOnFirstBackendReply(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 4, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	proxyPort := attachPoolBackendForTest(t, m, seed.id, seed)

	router, _ := nqnet.NewTestRouter(false)
	t.Cleanup(router.Close)

	if _, ok := m.ResolveDestinationPort(router, proxyPort); !ok {
		t.Fatalf("expected first route success")
	}

	m.mu.RLock()
	pool := m.poolByServerID[seed.id]
	firstDemand := len(pool.joinDemandAt)
	m.mu.RUnlock()
	if firstDemand != 0 {
		t.Fatalf("demand should not increment before backend reply, got %d", firstDemand)
	}

	if got := m.RewriteSourcePort(router, 26000); got != proxyPort {
		t.Fatalf("backend reply rewrite = %d, want proxy %d", got, proxyPort)
	}

	m.mu.RLock()
	secondDemand := len(pool.joinDemandAt)
	m.mu.RUnlock()
	if secondDemand != 1 {
		t.Fatalf("first backend reply demand count = %d, want 1", secondDemand)
	}

	if _, ok := m.ResolveDestinationPort(router, proxyPort); !ok {
		t.Fatalf("expected sticky route success")
	}
	if got := m.RewriteSourcePort(router, 26000); got != proxyPort {
		t.Fatalf("sticky backend reply rewrite = %d, want proxy %d", got, proxyPort)
	}

	m.mu.RLock()
	thirdDemand := len(pool.joinDemandAt)
	m.mu.RUnlock()
	if thirdDemand != 1 {
		t.Fatalf("sticky backend replies should not add demand, got %d", thirdDemand)
	}
}

func TestResolveRCONTargetPort_PoolProxyAmbiguity(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := newRunningPoolTestRecord(t, m, 0, 26000, []string{"-dedicated", "-port", "0"}, 0, 16)
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}
	replica := newRunningPoolTestRecord(t, m, 1, 26001, []string{"-dedicated", "-port", "0"}, 0, 16)
	proxyPort := attachPoolBackendForTest(t, m, seed.id, replica)

	if _, err := m.resolveRCONTargetPort(proxyPort); err == nil {
		t.Fatalf("expected ambiguous scaled target error")
	}
	if _, err := m.ExecServerCmd(proxyPort, "status", ""); err == nil || !strings.Contains(err.Error(), "ambiguous scaled target") {
		t.Fatalf("ExecServerCmd ambiguous error = %v, want ambiguous scaled target", err)
	}

	m.mu.Lock()
	replica.Running = nil
	m.refreshPoolForServerLocked(replica.id)
	m.mu.Unlock()

	port, err := m.resolveRCONTargetPort(proxyPort)
	if err != nil {
		t.Fatalf("resolve pooled target with one backend: %v", err)
	}
	if port != 26000 {
		t.Fatalf("resolved backend = %d, want 26000", port)
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

func TestReconcileAllPools_AttemptsScaleUpWithoutServerInfoEvents(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
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

func TestPoolRouting_FailedRouteDoesNotRecordDemand(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir(), nil, nil, nil, nil, nil, nil)
	t.Cleanup(m.closePoolRegistry)

	seed := m.registerServerLaunch(serverLaunch{Line: 0, Binary: "nqserver", Args: []string{"-dedicated", "-port", "0"}})
	if err := m.registerPoolSeed(seed); err != nil {
		t.Fatalf("register pool seed: %v", err)
	}

	m.mu.RLock()
	pool := m.poolByServerID[seed.id]
	proxyPort := pool.ListenPort
	m.mu.RUnlock()

	router, _ := nqnet.NewTestRouter(false)
	t.Cleanup(router.Close)

	if _, ok := m.ResolveDestinationPort(router, proxyPort); ok {
		t.Fatalf("expected route failure with no running backends")
	}

	m.mu.RLock()
	demandCount := len(pool.joinDemandAt)
	m.mu.RUnlock()
	if demandCount != 0 {
		t.Fatalf("failed route should not record demand, got %d", demandCount)
	}
}
