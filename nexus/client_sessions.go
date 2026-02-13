package main

import (
	"strings"
	"sync"
)

type clientSessionRegistry struct {
	mu          sync.RWMutex
	byVirtualIP map[string]map[*Router]struct{}
}

type clientSessionSnapshot struct {
	VirtualIP        string
	IsAdmin          bool
	ActiveServerPort int
}

var globalClientSessions = newClientSessionRegistry()

func newClientSessionRegistry() *clientSessionRegistry {
	return &clientSessionRegistry{byVirtualIP: make(map[string]map[*Router]struct{})}
}

func (r *clientSessionRegistry) Track(router *Router) {
	if r == nil || router == nil {
		return
	}
	virtualIP := strings.TrimSpace(router.VirtualClientIP())
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

func (r *clientSessionRegistry) Untrack(router *Router) {
	if r == nil || router == nil {
		return
	}
	virtualIP := strings.TrimSpace(router.VirtualClientIP())
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

func (r *clientSessionRegistry) SnapshotByVirtualIP(virtualIP string) (routers []*Router, targets []banTarget) {
	if r == nil {
		return nil, nil
	}
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
	targets = make([]banTarget, 0, len(set))
	for router := range set {
		routers = append(routers, router)
		port := router.ActiveServerPort()
		if port < 1 || port > 65535 {
			continue
		}
		vip := router.VirtualClientIP()
		if vip == "" {
			continue
		}
		targets = append(targets, banTarget{Port: port, VirtualIP: vip})
	}
	r.mu.RUnlock()

	return routers, sortedUniqueTargets(targets)
}

func (r *clientSessionRegistry) SnapshotAll() []clientSessionSnapshot {
	if r == nil {
		return nil
	}

	r.mu.RLock()
	defer r.mu.RUnlock()

	if len(r.byVirtualIP) == 0 {
		return nil
	}

	out := make([]clientSessionSnapshot, 0, len(r.byVirtualIP))
	for virtualIP, routers := range r.byVirtualIP {
		for router := range routers {
			if router == nil {
				continue
			}
			out = append(out, clientSessionSnapshot{
				VirtualIP:        strings.TrimSpace(virtualIP),
				IsAdmin:          router.isAdmin,
				ActiveServerPort: router.ActiveServerPort(),
			})
		}
	}
	return out
}
