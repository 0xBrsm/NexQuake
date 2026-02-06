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

type hashedClientIPAllocator struct {
	mu   sync.Mutex
	used map[uint32]struct{}
}

func newHashedClientIPAllocator() *hashedClientIPAllocator {
	return &hashedClientIPAllocator{
		used: make(map[uint32]struct{}),
	}
}

func (a *hashedClientIPAllocator) alloc(sourceKey string) ([4]byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	base := strings.TrimSpace(sourceKey)
	if base == "" {
		base = "raw:unknown"
	}

	for probe := 0; probe < maxClientIPProbeAttempts; probe++ {
		seed := "NQ:ip:v1|" + base + "|" + strconv.Itoa(probe)
		sum := sha256.Sum256([]byte(seed))

		candidate := [4]byte{127, sum[0], sum[1], sum[2]}
		if isReservedClientIPv4(candidate) {
			continue
		}

		key := binary.BigEndian.Uint32(candidate[:])
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
	delete(a.used, binary.BigEndian.Uint32(ip4[:]))
	a.mu.Unlock()
}

func isReservedClientIPv4(ip4 [4]byte) bool {
	return ip4[0] == subnetAdminsA && ip4[1] == subnetAdminsB && ip4[2] == subnetAdminsC
}

type infraSubnetAllocator struct {
	mu          sync.Mutex
	used        [256]bool
	nextAdmin   int
	serverCount int
}

func newInfraSubnetAllocator() *infraSubnetAllocator {
	a := &infraSubnetAllocator{}
	a.configure(0)
	return a
}

func (a *infraSubnetAllocator) configure(serverCount int) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range a.used {
		a.used[i] = false
	}

	a.serverCount = 0
	if serverCount > 0 {
		if serverCount > 254 {
			serverCount = 254
		}
		a.serverCount = serverCount
	}

	// Nexus infra address.
	a.used[0] = true

	// Dedicated server addresses: .1..serverCount
	for i := 1; i <= a.serverCount; i++ {
		a.used[i] = true
	}

	a.nextAdmin = 255
}

func (a *infraSubnetAllocator) allocAdmin() (byte, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := 0; i < 256; i++ {
		if a.nextAdmin < 0 {
			a.nextAdmin = 255
		}
		oct := a.nextAdmin
		a.nextAdmin--

		if !a.used[oct] {
			a.used[oct] = true
			return byte(oct), true
		}
	}

	return 0, false
}

func (a *infraSubnetAllocator) releaseAdmin(oct byte) {
	if oct == 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	// Keep infra + server reservations pinned.
	if int(oct) <= a.serverCount {
		return
	}
	a.used[oct] = false
}

var globalClientIPs = newHashedClientIPAllocator()
var globalInfraIPs = newInfraSubnetAllocator()

func ConfigureInfraSubnet(serverCount int) {
	globalInfraIPs.configure(serverCount)
}

func allocateRelaySourceIPv4(client *ClientConnection, isAdmin bool) (ip4 [4]byte, adminOctet byte, err error) {
	if isAdmin {
		oct, ok := globalInfraIPs.allocAdmin()
		if !ok {
			return [4]byte{}, 0, fmt.Errorf("failed to allocate admin IP in %d.%d.%d.x", subnetAdminsA, subnetAdminsB, subnetAdminsC)
		}
		return [4]byte{subnetAdminsA, subnetAdminsB, subnetAdminsC, oct}, oct, nil
	}

	clientIP, ok := globalClientIPs.alloc(client.sourceKey)
	if !ok {
		return [4]byte{}, 0, fmt.Errorf("failed to allocate client IP from source %q", client.sourceKey)
	}
	return clientIP, 0, nil
}

func releaseRelaySourceIPv4(isAdmin bool, ip4 [4]byte, adminOctet byte) {
	if isAdmin {
		if adminOctet != 0 {
			globalInfraIPs.releaseAdmin(adminOctet)
		}
		return
	}
	if ip4[0] != 0 {
		globalClientIPs.release(ip4)
	}
}

func parseClientIP(raw string) (netip.Addr, bool) {
	val := strings.TrimSpace(raw)
	if val == "" {
		return netip.Addr{}, false
	}

	// Handle comma-separated forwarding headers.
	if i := strings.Index(val, ","); i >= 0 {
		val = strings.TrimSpace(val[:i])
	}

	// Handle simple key-value format like "for=1.2.3.4".
	if k, v, ok := strings.Cut(val, "="); ok && strings.EqualFold(strings.TrimSpace(k), "for") {
		val = strings.TrimSpace(v)
	}

	val = strings.Trim(val, "\"")
	if ip, err := netip.ParseAddr(val); err == nil {
		return ip.Unmap(), true
	}

	if host, _, err := net.SplitHostPort(val); err == nil {
		if ip, err := netip.ParseAddr(host); err == nil {
			return ip.Unmap(), true
		}
	}

	return netip.Addr{}, false
}

func resolveClientSourceKey(r *http.Request) string {
	if headerName := strings.TrimSpace(os.Getenv("AUTH_CLIENT_IP_HEADER")); headerName != "" {
		if ip, ok := parseClientIP(r.Header.Get(headerName)); ok {
			return "ip:" + ip.String()
		}
	}

	if ip, ok := parseClientIP(r.RemoteAddr); ok {
		return "ip:" + ip.String()
	}

	raw := strings.TrimSpace(r.RemoteAddr)
	if raw == "" {
		return "raw:unknown"
	}
	return "raw:" + raw
}

func relayListenAddr(ip4 [4]byte) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IPv4(ip4[0], ip4[1], ip4[2], ip4[3]).To4(),
		Port: 0,
	}
}

func serverRouteFromAddr(addr net.Addr) (serverID byte, srcPort int, ok bool) {
	udpAddr, isUDP := addr.(*net.UDPAddr)
	if !isUDP || udpAddr.IP == nil {
		return 0, 0, false
	}
	ip4 := udpAddr.IP.To4()
	if ip4 == nil {
		return 0, 0, false
	}
	if ip4[0] != subnetServersA || ip4[1] != subnetServersB || ip4[2] != subnetServersC {
		return 0, 0, false
	}
	if udpAddr.Port <= 0 || udpAddr.Port > 65535 {
		return 0, 0, false
	}
	return ip4[3], udpAddr.Port, true
}

func serverUDPAddr(serverID byte, port int) *net.UDPAddr {
	return &net.UDPAddr{
		IP:   net.IPv4(subnetServersA, subnetServersB, subnetServersC, serverID).To4(),
		Port: port,
	}
}
