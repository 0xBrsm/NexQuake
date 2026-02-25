package nqnet

import (
	"encoding/binary"
	"net/netip"
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
	UserID           string
	IsAdmin          bool
	ActiveServerPort int
}

// SessionRegistry tracks active Router→virtual-IP associations.
type SessionRegistry struct {
	mu          sync.Mutex
	byVirtualIP map[uint32]map[*Router]struct{}
}

// NewSessionRegistry creates an empty session registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{byVirtualIP: make(map[uint32]map[*Router]struct{})}
}

// track registers a router in the registry.
func (r *SessionRegistry) track(router *Router) {
	virtualIP, ok := virtualIPKeyFromRouter(router)
	if !ok {
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
	virtualIP, ok := virtualIPKeyFromRouter(router)
	if !ok {
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
	virtualIPKey, ok := parseVirtualIPKey(virtualIP)
	if !ok {
		return nil, nil
	}

	r.mu.Lock()
	set := r.byVirtualIP[virtualIPKey]
	if len(set) == 0 {
		r.mu.Unlock()
		return nil, nil
	}
	routers = make([]*Router, 0, len(set))
	for router := range set {
		routers = append(routers, router)
	}
	r.mu.Unlock()

	vip := virtualIPStringFromKey(virtualIPKey)
	targets = make([]BanTarget, 0, len(routers))
	for _, router := range routers {
		port := router.activeServerPort()
		if port < 1 || port > 65535 {
			continue
		}
		targets = append(targets, BanTarget{Port: port, VirtualIP: vip})
	}

	return routers, sortedUniqueTargets(targets)
}

// SnapshotAll returns a snapshot of every tracked session.
func (r *SessionRegistry) SnapshotAll() []SessionSnapshot {
	type sessionGroup struct {
		virtualIPKey uint32
		routers      []*Router
	}

	r.mu.Lock()
	if len(r.byVirtualIP) == 0 {
		r.mu.Unlock()
		return nil
	}

	groups := make([]sessionGroup, 0, len(r.byVirtualIP))
	routerCount := 0
	for virtualIPKey, set := range r.byVirtualIP {
		routers := make([]*Router, 0, len(set))
		for router := range set {
			routers = append(routers, router)
		}
		routerCount += len(routers)
		groups = append(groups, sessionGroup{virtualIPKey: virtualIPKey, routers: routers})
	}
	r.mu.Unlock()

	out := make([]SessionSnapshot, 0, routerCount)
	for _, group := range groups {
		virtualIP := virtualIPStringFromKey(group.virtualIPKey)
		for _, router := range group.routers {
			out = append(out, SessionSnapshot{
				VirtualIP:        virtualIP,
				SourceIP:         router.SourceIP(),
				UserID:           router.UserID(),
				IsAdmin:          router.IsAdmin(),
				ActiveServerPort: router.activeServerPort(),
			})
		}
	}
	return out
}

func virtualIPKeyFromRouter(router *Router) (uint32, bool) {
	ip4 := router.ClientIP()
	if ip4[0] == 0 {
		return 0, false
	}
	return binary.BigEndian.Uint32(ip4[:]), true
}

func parseVirtualIPKey(virtualIP string) (uint32, bool) {
	virtualIP = strings.TrimSpace(virtualIP)
	if virtualIP == "" {
		return 0, false
	}
	addr, err := netip.ParseAddr(virtualIP)
	if err != nil {
		return 0, false
	}
	addr = addr.Unmap()
	if !addr.Is4() {
		return 0, false
	}
	ip4 := addr.As4()
	if ip4[0] == 0 {
		return 0, false
	}
	return binary.BigEndian.Uint32(ip4[:]), true
}

func virtualIPStringFromKey(virtualIPKey uint32) string {
	var ip4 [4]byte
	binary.BigEndian.PutUint32(ip4[:], virtualIPKey)
	return netip.AddrFrom4(ip4).String()
}

// sortedUniqueTargets deduplicates and sorts ban targets.
func sortedUniqueTargets(in []BanTarget) []BanTarget {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[BanTarget]struct{}, len(in))
	out := make([]BanTarget, 0, len(in))
	for _, t := range in {
		if _, dup := seen[t]; !dup {
			seen[t] = struct{}{}
			out = append(out, t)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Port != out[j].Port {
			return out[i].Port < out[j].Port
		}
		return out[i].VirtualIP < out[j].VirtualIP
	})
	return out
}
