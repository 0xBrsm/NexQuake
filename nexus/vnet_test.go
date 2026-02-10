package main

import (
	"net"
	"net/http/httptest"
	"testing"
)

func TestHashedClientIPAllocatorProbeOnCollision(t *testing.T) {
	a := newHashedClientIPAllocator()

	first, ok := a.alloc("ip:203.0.113.10")
	if !ok {
		t.Fatalf("first alloc() failed")
	}
	second, ok := a.alloc("ip:203.0.113.10")
	if !ok {
		t.Fatalf("second alloc() failed")
	}
	if first == second {
		t.Fatalf("expected probe to produce distinct active allocation")
	}

	a.release(first)
	third, ok := a.alloc("ip:203.0.113.10")
	if !ok {
		t.Fatalf("third alloc() failed")
	}
	if third != first {
		t.Fatalf("expected released primary slot to be reused; got %v want %v", third, first)
	}
}

func TestHashedClientIPAllocatorSkipsNQServerIP(t *testing.T) {
	oldNQServerIP := nqServerIP
	t.Cleanup(func() { nqServerIP = oldNQServerIP })

	firstAllocator := newHashedClientIPAllocator()
	reservedCandidate, ok := firstAllocator.alloc("ip:203.0.113.9")
	if !ok {
		t.Fatalf("alloc() for reserved candidate failed")
	}

	nqServerIP = net.IPv4(reservedCandidate[0], reservedCandidate[1], reservedCandidate[2], reservedCandidate[3]).To4()

	secondAllocator := newHashedClientIPAllocator()
	got, ok := secondAllocator.alloc("ip:203.0.113.9")
	if !ok {
		t.Fatalf("alloc() with reserved server ip failed")
	}
	if got == reservedCandidate {
		t.Fatalf("expected allocator to skip nqServerIP %v", reservedCandidate)
	}
}

func TestResolveClientSourceKey(t *testing.T) {
	req := httptest.NewRequest("GET", "/ws", nil)
	req.RemoteAddr = "198.51.100.42:43210"
	if got := resolveClientSourceKey(req); got != "ip:198.51.100.42" {
		t.Fatalf("resolveClientSourceKey()=%q want %q", got, "ip:198.51.100.42")
	}

	if got := resolveClientSourceKey(nil); got != "" {
		t.Fatalf("resolveClientSourceKey(nil)=%q want empty", got)
	}
}

func TestResolveClientSourceKey_HeaderOverride(t *testing.T) {
	t.Setenv("AUTH_CLIENT_IP_HEADER", "X-Forwarded-For")

	req := httptest.NewRequest("GET", "/ws", nil)
	req.RemoteAddr = "10.0.0.9:34567"
	req.Header.Set("X-Forwarded-For", "203.0.113.7, 10.0.0.9")

	if got := resolveClientSourceKey(req); got != "ip:203.0.113.7" {
		t.Fatalf("resolveClientSourceKey()=%q want %q", got, "ip:203.0.113.7")
	}
}

func TestBuildWSClientIdentityFrame(t *testing.T) {
	ip4 := [4]byte{127, 1, 2, 3}
	frame := buildWSClientIdentityFrame(ip4)
	if len(frame) != wsPortHeaderSize+len(wsClientIdentityMagic)+len(ip4) {
		t.Fatalf("identity frame length=%d", len(frame))
	}
	if frame[0] != 0 || frame[1] != 0 {
		t.Fatalf("identity frame routing header=%v want [0 0]", frame[:2])
	}
	if got := string(frame[wsPortHeaderSize : wsPortHeaderSize+len(wsClientIdentityMagic)]); got != wsClientIdentityMagic {
		t.Fatalf("identity frame magic=%q want %q", got, wsClientIdentityMagic)
	}
	wantTail := []byte{127, 1, 2, 3}
	gotTail := frame[len(frame)-len(wantTail):]
	for i := range wantTail {
		if gotTail[i] != wantTail[i] {
			t.Fatalf("identity frame ip byte[%d]=%d want %d", i, gotTail[i], wantTail[i])
		}
	}
}

func TestBuildWSClientIdentityFrameZeroIP(t *testing.T) {
	if got := buildWSClientIdentityFrame([4]byte{}); got != nil {
		t.Fatalf("expected nil frame for zero IP")
	}
}
