package nqnet

import (
	"sort"
	"strings"
	"sync"
)

// BanTarget identifies a client to ban on a specific server port.
type BanTarget struct {
	Port      int
	VirtualIP string
}

// SessionSnapshot is a point-in-time view of a single client session.
type SessionSnapshot struct {
	VirtualIP        string
	SourceIP         string
	IsAdmin          bool
	ActiveServerPort int
}

// SessionRegistry tracks active Router→virtual-IP associations.
type SessionRegistry struct {
	mu          sync.RWMutex
	byVirtualIP map[string]map[*Router]struct{}
}

// NewSessionRegistry creates an empty session registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{byVirtualIP: make(map[string]map[*Router]struct{})}
}

// track registers a router in the registry.
func (r *SessionRegistry) track(router *Router) {
	virtualIP := router.VirtualClientIP()
	if virtualIP == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	set := r.byVirtualIP[virtualIP]
	if set == nil {
		set = make(map[*Router]struct{})
		r.byVirtualIP[virtualIP] = set
	}
	set[router] = struct{}{}
}

// untrack removes a router from the registry.
func (r *SessionRegistry) untrack(router *Router) {
	virtualIP := router.VirtualClientIP()
	if virtualIP == "" {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	set := r.byVirtualIP[virtualIP]
	if len(set) == 0 {
		return
	}
	delete(set, router)
	if len(set) == 0 {
		delete(r.byVirtualIP, virtualIP)
	}
}

// SnapshotByVirtualIP returns all routers and their ban targets for a virtual IP.
func (r *SessionRegistry) SnapshotByVirtualIP(virtualIP string) (routers []*Router, targets []BanTarget) {
	virtualIP = strings.TrimSpace(virtualIP)
	if virtualIP == "" {
		return nil, nil
	}

	r.mu.RLock()
	set := r.byVirtualIP[virtualIP]
	if len(set) == 0 {
		r.mu.RUnlock()
		return nil, nil
	}
	routers = make([]*Router, 0, len(set))
	targets = make([]BanTarget, 0, len(set))
	for router := range set {
		routers = append(routers, router)
		port := router.activeServerPort()
		if port < 1 || port > 65535 {
			continue
		}
		vip := router.VirtualClientIP()
		if vip == "" {
			continue
		}
		targets = append(targets, BanTarget{Port: port, VirtualIP: vip})
	}
	r.mu.RUnlock()

	return routers, sortedUniqueTargets(targets)
}

// SnapshotAll returns a snapshot of every tracked session.
func (r *SessionRegistry) SnapshotAll() []SessionSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.byVirtualIP) == 0 {
		return nil
	}

	out := make([]SessionSnapshot, 0, len(r.byVirtualIP))
	for virtualIP, routers := range r.byVirtualIP {
		for router := range routers {
			out = append(out, SessionSnapshot{
				VirtualIP:        virtualIP,
				SourceIP:         router.SourceIP(),
				IsAdmin:          router.IsAdmin(),
				ActiveServerPort: router.activeServerPort(),
			})
		}
	}
	return out
}

// sortedUniqueTargets deduplicates and sorts ban targets.
func sortedUniqueTargets(in []BanTarget) []BanTarget {
	if len(in) == 0 {
		return nil
	}
	type banKey struct {
		Port      int
		VirtualIP string
	}
	seen := make(map[banKey]BanTarget, len(in))
	for _, t := range in {
		if t.Port < 1 || t.Port > 65535 || t.VirtualIP == "" {
			continue
		}
		seen[banKey{Port: t.Port, VirtualIP: t.VirtualIP}] = t
	}
	out := make([]BanTarget, 0, len(seen))
	for _, target := range seen {
		out = append(out, target)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].VirtualIP < out[j].VirtualIP
	})
	return out
}
