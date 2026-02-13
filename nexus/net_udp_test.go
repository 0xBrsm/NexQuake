package main

import (
	"net"
	"testing"
)

func TestRelayListenAddr(t *testing.T) {
	addr := relayListenAddr()
	if addr.Port != 0 {
		t.Fatalf("relayListenAddr() port=%d want 0", addr.Port)
	}
	if !addr.IP.Equal(net.IPv4zero) {
		t.Fatalf("relayListenAddr() ip=%v want %v", addr.IP, net.IPv4zero)
	}
}

func TestRelayListenAddrForClient(t *testing.T) {
	ip4 := [4]byte{127, 1, 2, 3}
	addr := relayListenAddrForClient(ip4)
	wantIP := net.IPv4(ip4[0], ip4[1], ip4[2], ip4[3]).To4()
	if addr.Port != 0 {
		t.Fatalf("relayListenAddrForClient() port=%d want 0", addr.Port)
	}
	if !addr.IP.Equal(wantIP) {
		t.Fatalf("relayListenAddrForClient() ip=%v want %v", addr.IP, wantIP)
	}
}

func TestServerSourcePortFromAddr(t *testing.T) {
	good := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 26000}
	if port, ok := serverSourcePortFromAddr(good); !ok || port != 26000 {
		t.Fatalf("serverSourcePortFromAddr(good)=%d,%v want 26000,true", port, ok)
	}

	zeroPort := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
	if _, ok := serverSourcePortFromAddr(zeroPort); ok {
		t.Fatalf("serverSourcePortFromAddr(zeroPort) ok=true want false")
	}

	if _, ok := serverSourcePortFromAddr(&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 26000}); ok {
		t.Fatalf("serverSourcePortFromAddr(tcp) ok=true want false")
	}
}

func TestServerUDPAddr(t *testing.T) {
	addr := serverUDPAddr(26000)
	wantIP := nqServerIP
	if addr.Port != 26000 || !addr.IP.Equal(wantIP) {
		t.Fatalf("serverUDPAddr()=%v want %v:%d", addr, wantIP, 26000)
	}
}

func TestParseNQServerIP(t *testing.T) {
	defaultIP := net.ParseIP(defaultNQServerIP).To4()
	if got := parseNQServerIP(""); !got.Equal(defaultIP) {
		t.Fatalf("parseNQServerIP(empty)=%v want %v", got, defaultIP)
	}

	explicit := net.IPv4(127, 0, 0, 1).To4()
	if got := parseNQServerIP("127.0.0.1"); !got.Equal(explicit) {
		t.Fatalf("parseNQServerIP(127.0.0.1)=%v want %v", got, explicit)
	}

	if got := parseNQServerIP("not-an-ip"); !got.Equal(defaultIP) {
		t.Fatalf("parseNQServerIP(invalid)=%v want %v", got, defaultIP)
	}
}
