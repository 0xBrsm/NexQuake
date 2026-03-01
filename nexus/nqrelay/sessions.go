package nqrelay

import (
	"encoding/binary"
	"net/netip"
	"sort"
	"strings"
	"sync"
)

// BanTarget pairs a virtual IP with the server port the client was last routed
// to, giving callers the information needed to issue a game-server ban command.
type BanTarget struct {
	Port      int    // UDP port of the game server the client was routing to.
	VirtualIP string // 127.x.x.x address the game server sees as the client.
}

// SessionSnapshot is a point-in-time view of a single active session.
type SessionSnapshot struct {
	VirtualIP        string // 127.x.x.x virtual IP allocated for this client.
	SourceIP         string // Best-effort real client address (from NewRelay).
	UserID           string // Application-layer user identifier (from NewRelay).
	IsAdmin          bool   // Whether the relay has admin privileges.
	ActiveServerPort int    // Last UDP server port the client sent a frame to.
}

// SessionRegistry tracks active relays indexed by their virtual IP.
// It is safe for concurrent use.
type SessionRegistry struct {
	mu          sync.RWMutex
	byVirtualIP map[uint32]map[*Relay]struct{}
}

// NewSessionRegistry creates an empty session registry.
func NewSessionRegistry() *SessionRegistry {
	return &SessionRegistry{byVirtualIP: make(map[uint32]map[*Relay]struct{})}
}

// track registers a relay in the registry.
func (r *SessionRegistry) track(relay *Relay) {
	virtualIP, ok := virtualIPKeyFromRelay(relay)
	if !ok {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	set := r.byVirtualIP[virtualIP]
	if set == nil {
		set = make(map[*Relay]struct{})
		r.byVirtualIP[virtualIP] = set
	}
	set[relay] = struct{}{}
}

// untrack removes a relay from the registry.
func (r *SessionRegistry) untrack(relay *Relay) {
	virtualIP, ok := virtualIPKeyFromRelay(relay)
	if !ok {
		return
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	set := r.byVirtualIP[virtualIP]
	if len(set) == 0 {
		return
	}
	delete(set, relay)
	if len(set) == 0 {
		delete(r.byVirtualIP, virtualIP)
	}
}

// SnapshotByVirtualIP returns all active relays for the given virtual IP and
// the deduplicated, sorted set of ban targets derived from their last-routed
// server ports. Returns (nil, nil) for an unknown or invalid IP.
// Callers typically close each returned relay and then issue game-server ban
// commands for each target.
func (r *SessionRegistry) SnapshotByVirtualIP(virtualIP string) (relays []*Relay, targets []BanTarget) {
	virtualIPKey, ok := parseVirtualIPKey(virtualIP)
	if !ok {
		return nil, nil
	}

	vip := virtualIPStringFromKey(virtualIPKey)
	r.mu.RLock()
	defer r.mu.RUnlock()

	set := r.byVirtualIP[virtualIPKey]
	if len(set) == 0 {
		return nil, nil
	}

	relays = make([]*Relay, 0, len(set))
	targets = make([]BanTarget, 0, len(set))
	for relay := range set {
		relays = append(relays, relay)
		port := relay.activeServerPort()
		if !isValidServerPort(port) {
			continue
		}
		targets = append(targets, BanTarget{Port: port, VirtualIP: vip})
	}

	return relays, sortedUniqueTargets(targets)
}

// SnapshotAll returns a point-in-time snapshot of every tracked session.
// Returns nil when there are no active sessions.
func (r *SessionRegistry) SnapshotAll() []SessionSnapshot {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.byVirtualIP) == 0 {
		return nil
	}

	total := 0
	for _, set := range r.byVirtualIP {
		total += len(set)
	}
	out := make([]SessionSnapshot, 0, total)
	for virtualIPKey, set := range r.byVirtualIP {
		virtualIP := virtualIPStringFromKey(virtualIPKey)
		for relay := range set {
			out = append(out, SessionSnapshot{
				VirtualIP:        virtualIP,
				SourceIP:         relay.SourceIP(),
				UserID:           relay.UserID(),
				IsAdmin:          relay.IsAdmin(),
				ActiveServerPort: relay.activeServerPort(),
			})
		}
	}
	return out
}

func virtualIPKeyFromRelay(relay *Relay) (uint32, bool) {
	ip4 := relay.ClientIP()
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
