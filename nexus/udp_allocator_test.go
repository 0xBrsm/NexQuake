package main

import (
	"strconv"
	"testing"
)

func TestHashedClientIPAllocatorSkipsReservedInfraSubnet(t *testing.T) {
	a := newHashedClientIPAllocator()

	for i := 0; i < 128; i++ {
		ip4, ok := a.alloc("ip:198.51.100." + strconv.Itoa(i))
		if !ok {
			t.Fatalf("alloc() failed at i=%d", i)
		}
		if isReservedClientIPv4(ip4) {
			t.Fatalf("alloc() returned reserved infra subnet address: %v", ip4)
		}
	}
}

func TestHashedClientIPAllocatorProbeOnActiveCollision(t *testing.T) {
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
		t.Fatalf("expected collision probe to produce distinct active allocation")
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

func TestInfraSubnetAllocatorReservesNexusAndServers(t *testing.T) {
	a := newInfraSubnetAllocator()
	a.configure(3)

	if !a.used[0] || !a.used[1] || !a.used[2] || !a.used[3] {
		t.Fatalf("expected .0 and .1..3 to be reserved")
	}

	o1, ok := a.allocAdmin()
	if !ok {
		t.Fatalf("allocAdmin() failed")
	}
	o2, ok := a.allocAdmin()
	if !ok {
		t.Fatalf("second allocAdmin() failed")
	}
	if o1 != 255 || o2 != 254 {
		t.Fatalf("expected descending admin allocation 255,254 got %d,%d", o1, o2)
	}

	a.releaseAdmin(1)
	if !a.used[1] {
		t.Fatalf("server slot .1 should remain reserved")
	}
}
