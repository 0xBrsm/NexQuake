package nqrelay

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
)

const maxClientIPProbeAttempts = 4096

// IPAllocator deterministically maps client source keys to 127.x.x.x
// virtual IP addresses used for the Quake relay.
type IPAllocator struct {
	serverIP net.IP

	mu            sync.RWMutex
	used          map[uint32]struct{}
	reserved      map[uint32]struct{}
	blockedSource map[string]struct{}
}

// NewIPAllocator creates an allocator that avoids collisions with serverIP.
func NewIPAllocator(serverIP net.IP) *IPAllocator {
	return &IPAllocator{
		serverIP:      serverIP.To4(),
		used:          make(map[uint32]struct{}),
		reserved:      make(map[uint32]struct{}),
		blockedSource: make(map[string]struct{}),
	}
}

// alloc allocates a unique virtual IP for the given source key.
func (a *IPAllocator) alloc(sourceKey string) ([4]byte, error) {
	sourceKey = normalizeSourceKey(sourceKey)
	if sourceKey == "" {
		sourceKey = "unknown"
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, blocked := a.blockedSource[sourceKey]; blocked {
		return [4]byte{}, fmt.Errorf("failed to allocate relay source ip for %q", sourceKey)
	}

	for probe := 0; probe < maxClientIPProbeAttempts; probe++ {
		seed := "NQ:client-ip:v1|" + sourceKey + "|" + strconv.Itoa(probe)
		sum := fnv64aSum(seed)

		candidate := [4]byte{127, byte(sum >> 56), byte(sum >> 48), byte(sum >> 40)}
		if len(a.serverIP) == net.IPv4len &&
			candidate[0] == a.serverIP[0] &&
			candidate[1] == a.serverIP[1] &&
			candidate[2] == a.serverIP[2] &&
			candidate[3] == a.serverIP[3] {
			continue
		}

		key := binary.BigEndian.Uint32(candidate[:])
		if _, reserved := a.reserved[key]; reserved {
			continue
		}
		if _, used := a.used[key]; used {
			continue
		}

		a.used[key] = struct{}{}
		return candidate, nil
	}

	return [4]byte{}, fmt.Errorf("failed to allocate relay source ip for %q", sourceKey)
}

func fnv64aSum(text string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	return h.Sum64()
}

// release returns a virtual IP to the pool.
func (a *IPAllocator) release(ip4 [4]byte) {
	if ip4[0] != 127 {
		return
	}
	a.mu.Lock()
	key := binary.BigEndian.Uint32(ip4[:])
	if _, reserved := a.reserved[key]; !reserved {
		delete(a.used, key)
	}
	a.mu.Unlock()
}

// ReserveAndBlock permanently reserves ip4 and blocks sourceKey from future
// allocations. Used for banning.
func (a *IPAllocator) ReserveAndBlock(ip4 [4]byte, sourceKey string) {
	if ip4[0] != 127 {
		return
	}
	sourceKey = normalizeSourceKey(sourceKey)

	a.mu.Lock()
	key := binary.BigEndian.Uint32(ip4[:])
	a.reserved[key] = struct{}{}
	a.used[key] = struct{}{}
	if sourceKey != "" {
		a.blockedSource[sourceKey] = struct{}{}
	}
	a.mu.Unlock()
}

// IsBlocked reports whether sourceKey has been banned.
func (a *IPAllocator) IsBlocked(sourceKey string) bool {
	sourceKey = normalizeSourceKey(sourceKey)
	if sourceKey == "" {
		return false
	}
	a.mu.RLock()
	_, blocked := a.blockedSource[sourceKey]
	a.mu.RUnlock()
	return blocked
}

func normalizeSourceKey(sourceKey string) string {
	return strings.TrimSpace(sourceKey)
}

// ParseClientIP extracts an IP address from a raw header value or remote addr
// string, handling Forwarded/X-Forwarded-For formats and port stripping.
func ParseClientIP(raw string) (netip.Addr, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return netip.Addr{}, false
	}

	if comma := strings.IndexByte(value, ','); comma >= 0 {
		value = strings.TrimSpace(value[:comma])
	}
	if k, v, ok := strings.Cut(value, "="); ok && strings.EqualFold(strings.TrimSpace(k), "for") {
		value = strings.TrimSpace(v)
	}
	value = strings.Trim(strings.TrimSpace(value), "\"")
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}
	ip, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// ResolveClientSourceIP returns the external client IP for a request.
// Preference order:
// 1) AUTH_CLIENT_IP_HEADER (if configured and parseable)
// 2) Remote address IP
func ResolveClientSourceIP(r *http.Request) string {
	if r == nil {
		return ""
	}

	if headerName := strings.TrimSpace(os.Getenv("AUTH_CLIENT_IP_HEADER")); headerName != "" {
		if ip, ok := ParseClientIP(r.Header.Get(headerName)); ok {
			return ip.String()
		}
	}

	if ip, ok := ParseClientIP(r.RemoteAddr); ok {
		return ip.String()
	}

	return ""
}

// ResolveClientSourceKey derives a stable identity key from an HTTP request.
func ResolveClientSourceKey(r *http.Request) string {
	if r == nil {
		return ""
	}

	if sourceIP := ResolveClientSourceIP(r); sourceIP != "" {
		return "ip:" + sourceIP
	}
	return strings.TrimSpace(r.RemoteAddr)
}
