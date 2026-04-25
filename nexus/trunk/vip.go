package trunk

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"net"
	"strconv"
	"strings"
	"sync"
)

const maxVirtualIPProbeAttempts = 4096

// VirtualIPAllocator deterministically maps client source keys to unique 127.x.x.x
// VirtualIP addresses. The mapping is stable for a given sourceKey within a process
// lifetime: the same key always hashes to the same candidate, with linear
// probing on collision. Safe for concurrent use.
type VirtualIPAllocator struct {
	serverIP net.IP

	mu            sync.RWMutex
	used          map[uint32]struct{}
	reserved      map[uint32]struct{}
	blockedSource map[string]struct{}
}

// NewVirtualIPAllocator creates an allocator. serverIP must be a 127.x.x.x address
// (typically net.ParseIP(DefaultServerIP)); that address is excluded from
// the allocation pool to avoid the relay colliding with the game server itself.
func NewVirtualIPAllocator(serverIP net.IP) *VirtualIPAllocator {
	return &VirtualIPAllocator{
		serverIP:      serverIP.To4(),
		used:          make(map[uint32]struct{}),
		reserved:      make(map[uint32]struct{}),
		blockedSource: make(map[string]struct{}),
	}
}

// alloc allocates a unique VirtualIP for the given source key.
func (a *VirtualIPAllocator) alloc(sourceKey string) ([4]byte, error) {
	sourceKey = normalizeSourceKey(sourceKey)
	if sourceKey == "" {
		sourceKey = "unknown"
	}

	a.mu.Lock()
	defer a.mu.Unlock()

	if _, blocked := a.blockedSource[sourceKey]; blocked {
		return [4]byte{}, fmt.Errorf("failed to allocate relay source ip for %q", sourceKey)
	}

	for probe := 0; probe < maxVirtualIPProbeAttempts; probe++ {
		seed := "NQ:client-ip:v1|" + sourceKey + "|" + strconv.Itoa(probe)
		sum := fnv64aSum(seed)

		candidate := [4]byte{127, byte(sum >> 56), byte(sum >> 48), byte(sum >> 40)}
		if bytes.Equal(a.serverIP, candidate[:]) {
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

// release returns an VirtualIP to the pool.
func (a *VirtualIPAllocator) release(ip4 [4]byte) {
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

// ReserveAndBlock permanently reserves ip4 so it is never re-allocated, and
// blocks sourceKey from receiving any future allocation. Intended for banning:
// call after closing a relay to ensure the banned VirtualIP is not recycled and
// the banned key cannot reconnect with a different VirtualIP.
func (a *VirtualIPAllocator) ReserveAndBlock(ip4 [4]byte, sourceKey string) {
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

// IsBlocked reports whether sourceKey has been permanently blocked via
// [VirtualIPAllocator.ReserveAndBlock]. Callers can use this to reject reconnects
// before attempting to construct a new relay.
func (a *VirtualIPAllocator) IsBlocked(sourceKey string) bool {
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
