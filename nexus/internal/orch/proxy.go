package orch

import (
	"fmt"
	"time"

	"github.com/0xBrsm/NexQuake/nexus/internal/nqnet"
)

type poolSessionAffinity struct {
	byProxy map[int]poolProxyAffinity
}

type poolProxyAffinity struct {
	BackendPort    int
	ExpiresAt      time.Time
	DemandRecorded bool
}

func (m *ServerManager) resolveRCONTargetPort(port int) (int, error) {
	if port < 1 || port > 65535 {
		return 0, fmt.Errorf("unknown server")
	}

	m.mu.RLock()
	pool := m.poolByListenPort[port]
	if pool == nil {
		m.mu.RUnlock()
		return port, nil
	}
	usesDynamic := pool.UsesDynamicPort
	candidates := m.poolRoutableCandidatesLocked(pool, true)
	m.mu.RUnlock()

	if len(candidates) == 0 {
		return 0, fmt.Errorf("unknown server")
	}
	if !usesDynamic {
		return candidates[0].listenPort, nil
	}

	backendPort := candidates[0].listenPort
	for _, cand := range candidates[1:] {
		if cand.listenPort != backendPort {
			return 0, fmt.Errorf("ambiguous scaled target")
		}
	}
	return backendPort, nil
}

func routerSessionID(router *nqnet.Router) (uint64, bool) {
	if router == nil {
		return 0, false
	}
	sessionID := router.SessionID()
	return sessionID, sessionID != 0
}

func (m *ServerManager) expireSessionAffinityLocked(sessionID uint64, now time.Time) {
	session := m.affinityBySession[sessionID]
	if session == nil {
		return
	}
	for proxyPort, entry := range session.byProxy {
		if entry.ExpiresAt.After(now) {
			continue
		}
		delete(session.byProxy, proxyPort)
	}
	if len(session.byProxy) == 0 {
		delete(m.affinityBySession, sessionID)
	}
}

func (m *ServerManager) setSessionAffinityLocked(sessionID uint64, proxyPort, backendPort int, expiresAt time.Time) {
	session := m.affinityBySession[sessionID]
	if session == nil {
		session = &poolSessionAffinity{
			byProxy: make(map[int]poolProxyAffinity),
		}
		m.affinityBySession[sessionID] = session
	}
	session.byProxy[proxyPort] = poolProxyAffinity{
		BackendPort:    backendPort,
		ExpiresAt:      expiresAt,
		DemandRecorded: false,
	}
}

func (m *ServerManager) ResolveDestinationPort(router *nqnet.Router, dstPort int) (int, bool) {
	if dstPort < 1 || dstPort > 65535 {
		return 0, false
	}

	m.mu.RLock()
	pool := m.poolByListenPort[dstPort]
	usesDynamic := pool != nil && pool.UsesDynamicPort
	m.mu.RUnlock()
	if !usesDynamic {
		return dstPort, true
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	pool = m.poolByListenPort[dstPort]
	if pool == nil {
		return dstPort, true
	}
	if !pool.UsesDynamicPort {
		return dstPort, true
	}

	now := time.Now()
	if sessionID, ok := routerSessionID(router); ok {
		m.expireSessionAffinityLocked(sessionID, now)
		if session := m.affinityBySession[sessionID]; session != nil {
			if entry, found := session.byProxy[dstPort]; found && m.poolHasBackendPortLocked(pool, entry.BackendPort, true) {
				return entry.BackendPort, true
			}
		}
	}

	backendPort, ok := m.pickPoolBackendLocked(pool)
	if !ok {
		return 0, false
	}

	if sessionID, ok := routerSessionID(router); ok {
		m.setSessionAffinityLocked(sessionID, dstPort, backendPort, now.Add(proxyAffinityTTL))
	}

	return backendPort, true
}

func (m *ServerManager) RewriteSourcePort(router *nqnet.Router, srcPort int) int {
	if srcPort < 1 || srcPort > 65535 {
		return srcPort
	}

	sessionID, ok := routerSessionID(router)
	if !ok {
		return srcPort
	}

	m.mu.RLock()
	_, haveSession := m.affinityBySession[sessionID]
	m.mu.RUnlock()
	if !haveSession {
		return srcPort
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.expireSessionAffinityLocked(sessionID, now)
	session := m.affinityBySession[sessionID]
	if session == nil {
		return srcPort
	}

	proxyPort := 0
	var entry poolProxyAffinity
	for candidateProxyPort, candidate := range session.byProxy {
		if candidate.BackendPort != srcPort {
			continue
		}
		proxyPort = candidateProxyPort
		entry = candidate
		break
	}
	if proxyPort == 0 {
		return srcPort
	}

	pool := m.poolByListenPort[proxyPort]
	if pool == nil || !pool.UsesDynamicPort || !m.poolHasBackendPortLocked(pool, srcPort, true) {
		delete(session.byProxy, proxyPort)
		if len(session.byProxy) == 0 {
			delete(m.affinityBySession, sessionID)
		}
		return srcPort
	}
	if !entry.DemandRecorded {
		notePoolDemandLocked(pool, now)
		entry.DemandRecorded = true
		session.byProxy[proxyPort] = entry
	}

	return proxyPort
}

func (m *ServerManager) ReleaseSessionAffinity(router *nqnet.Router) {
	sessionID, ok := routerSessionID(router)
	if !ok {
		return
	}

	m.mu.Lock()
	delete(m.affinityBySession, sessionID)
	m.mu.Unlock()
}
