package main

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
)

const maxClientIPProbeAttempts = 4096

const wsClientIdentityMagic = "NQIP"

type hashedClientIPAllocator struct {
	mu            sync.Mutex
	used          map[uint32]struct{}
	reserved      map[uint32]struct{}
	blockedSource map[string]struct{}
}

func newHashedClientIPAllocator() *hashedClientIPAllocator {
	return &hashedClientIPAllocator{
		used:          make(map[uint32]struct{}),
		reserved:      make(map[uint32]struct{}),
		blockedSource: make(map[string]struct{}),
	}
}

func (a *hashedClientIPAllocator) alloc(sourceKey string) ([4]byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	base := normalizeSourceKey(sourceKey)
	if base == "" {
		base = "unknown"
	}
	if _, blocked := a.blockedSource[base]; blocked {
		return [4]byte{}, false
	}

	serverIP := nqServerIP.To4()

	for probe := 0; probe < maxClientIPProbeAttempts; probe++ {
		seed := "NQ:client-ip:v1|" + base + "|" + strconv.Itoa(probe)
		sum := sha256.Sum256([]byte(seed))

		candidate := [4]byte{127, sum[0], sum[1], sum[2]}
		if serverIP != nil &&
			candidate[0] == serverIP[0] &&
			candidate[1] == serverIP[1] &&
			candidate[2] == serverIP[2] &&
			candidate[3] == serverIP[3] {
			continue
		}

		key := binary.BigEndian.Uint32(candidate[:])
		if _, reserved := a.reserved[key]; reserved {
			continue
		}
		if _, exists := a.used[key]; exists {
			continue
		}

		a.used[key] = struct{}{}
		return candidate, true
	}

	return [4]byte{}, false
}

func (a *hashedClientIPAllocator) release(ip4 [4]byte) {
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

func (a *hashedClientIPAllocator) reserveAndBlock(ip4 [4]byte, sourceKey string) {
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

func (a *hashedClientIPAllocator) isBlockedSource(sourceKey string) bool {
	sourceKey = normalizeSourceKey(sourceKey)
	if sourceKey == "" {
		return false
	}
	a.mu.Lock()
	_, blocked := a.blockedSource[sourceKey]
	a.mu.Unlock()
	return blocked
}

func normalizeSourceKey(sourceKey string) string {
	return strings.TrimSpace(sourceKey)
}

var globalClientIPv4Allocator = newHashedClientIPAllocator()

func allocateRelaySourceIPv4(sourceKey string) ([4]byte, error) {
	ip4, ok := globalClientIPv4Allocator.alloc(sourceKey)
	if !ok {
		return [4]byte{}, fmt.Errorf("failed to allocate relay source ip for %q", sourceKey)
	}
	return ip4, nil
}

func releaseRelaySourceIPv4(ip4 [4]byte) {
	globalClientIPv4Allocator.release(ip4)
}

func reserveRelayClientIdentity(ip4 [4]byte, sourceKey string) {
	globalClientIPv4Allocator.reserveAndBlock(ip4, sourceKey)
}

func isBlockedRelaySource(sourceKey string) bool {
	return globalClientIPv4Allocator.isBlockedSource(sourceKey)
}

func buildWSClientIdentityFrame(clientIP [4]byte) []byte {
	if clientIP[0] == 0 {
		return nil
	}

	frame := make([]byte, wsPortHeaderSize+len(wsClientIdentityMagic)+len(clientIP))
	frame[0] = 0
	frame[1] = 0
	copy(frame[wsPortHeaderSize:], wsClientIdentityMagic)
	copy(frame[wsPortHeaderSize+len(wsClientIdentityMagic):], clientIP[:])
	return frame
}

func parseClientIP(raw string) (netip.Addr, bool) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return netip.Addr{}, false
	}

	if comma := strings.Index(value, ","); comma >= 0 {
		value = strings.TrimSpace(value[:comma])
	}

	if k, v, ok := strings.Cut(value, "="); ok && strings.EqualFold(strings.TrimSpace(k), "for") {
		value = strings.TrimSpace(v)
	}

	value = strings.Trim(value, "\"")
	if host, _, err := net.SplitHostPort(value); err == nil {
		value = host
	}

	ip, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

func resolveClientSourceKey(r *http.Request) string {
	if r == nil {
		return ""
	}

	if headerName := strings.TrimSpace(os.Getenv("AUTH_CLIENT_IP_HEADER")); headerName != "" {
		if ip, ok := parseClientIP(r.Header.Get(headerName)); ok {
			return "ip:" + ip.String()
		}
	}

	if ip, ok := parseClientIP(r.RemoteAddr); ok {
		return "ip:" + ip.String()
	}

	return strings.TrimSpace(r.RemoteAddr)
}
