package orch

import (
	"encoding/binary"
	"testing"
)

func TestBuildCCREQServerInfo(t *testing.T) {
	req := buildCCREQServerInfo()
	if len(req) < 12 {
		t.Fatalf("buildCCREQServerInfo() produced short packet: %d", len(req))
	}

	control := binary.BigEndian.Uint32(req[:4])
	wantControl := netFlagCtl | uint32(len(req))
	if control != wantControl {
		t.Fatalf("control header = %#x, want %#x", control, wantControl)
	}
	if req[4] != ccreqServerInfo {
		t.Fatalf("request type = %#x, want %#x", req[4], ccreqServerInfo)
	}

	wantTail := []byte{'Q', 'U', 'A', 'K', 'E', 0, netProtocolVersion}
	for i := range wantTail {
		if req[5+i] != wantTail[i] {
			t.Fatalf("tail[%d] = %#x, want %#x", i, req[5+i], wantTail[i])
		}
	}
}

func TestParseCCREPServerInfo(t *testing.T) {
	packet := make([]byte, 0, 64)
	packet = append(packet, 0, 0, 0, 0) // header placeholder
	packet = append(packet, ccrepServerInfo)
	packet = appendCString(packet, "127.0.0.1:26000")
	packet = appendCString(packet, "fragfest")
	packet = appendCString(packet, "dm6")
	packet = append(packet, 12, 16, netProtocolVersion)
	binary.BigEndian.PutUint32(packet[:4], netFlagCtl|uint32(len(packet)))

	hostname, mapName, players, maxPlayers, proto, ok := parseCCREPServerInfo(packet)
	if !ok {
		t.Fatalf("parseCCREPServerInfo() returned ok=false for valid packet")
	}
	if hostname != "fragfest" || mapName != "dm6" {
		t.Fatalf("unexpected host/map: got %q/%q", hostname, mapName)
	}
	if players != 12 || maxPlayers != 16 || proto != netProtocolVersion {
		t.Fatalf("unexpected counters/proto: got players=%d max=%d proto=%d", players, maxPlayers, proto)
	}
}

func TestParseCCREPServerInfo_ProtocolMismatch(t *testing.T) {
	packet := make([]byte, 0, 64)
	packet = append(packet, 0, 0, 0, 0)
	packet = append(packet, ccrepServerInfo)
	packet = appendCString(packet, "127.0.0.1:26000")
	packet = appendCString(packet, "fragfest")
	packet = appendCString(packet, "dm6")
	packet = append(packet, 1, 16, netProtocolVersion+1)
	binary.BigEndian.PutUint32(packet[:4], netFlagCtl|uint32(len(packet)))

	_, _, _, _, _, ok := parseCCREPServerInfo(packet)
	if ok {
		t.Fatalf("parseCCREPServerInfo() returned ok=true for mismatched protocol")
	}
}

func TestSlistEntriesEncodesFields(t *testing.T) {
	servers := slistEntriesFrom([]serverListEntry{{
		ListenPort: 26000,
		Hostname:   "fragfest",
		MapName:    "dm6",
		GameDir:    "id1",
		Users:      873,
		MaxUsers:   10000,
		Instances:  100,
	}})
	if len(servers) != 1 {
		t.Fatalf("entry count = %d, want 1", len(servers))
	}
	s := servers[0]
	if s.Port != 26000 || s.Hostname != "fragfest" || s.Map != "dm6" || s.GameDir != "id1" {
		t.Fatalf("unexpected string fields: %+v", s)
	}
	if s.Users != 873 || s.MaxUsers != 10000 || s.Instances != 100 {
		t.Fatalf("unexpected counters: users=%d max=%d inst=%d", s.Users, s.MaxUsers, s.Instances)
	}
}

func TestSlistEntriesPreservesZeroInstances(t *testing.T) {
	servers := slistEntriesFrom([]serverListEntry{{
		ListenPort: 26000,
		Hostname:   "fragfest",
		MapName:    "dm6",
		GameDir:    "id1",
		Users:      1,
		MaxUsers:   16,
		Instances:  0,
	}})
	if len(servers) != 1 {
		t.Fatalf("entry count = %d, want 1", len(servers))
	}
	if servers[0].Instances != 0 {
		t.Fatalf("instances = %d, want 0", servers[0].Instances)
	}
}

func TestSlistEntriesNormalizesAndSkipsBadPort(t *testing.T) {
	servers := slistEntriesFrom([]serverListEntry{
		{ListenPort: 0, Hostname: "skipme"}, // invalid port → skipped
		{ListenPort: 26000},                 // empty fields → defaults
	})
	if len(servers) != 1 {
		t.Fatalf("entry count = %d, want 1 (bad port dropped)", len(servers))
	}
	s := servers[0]
	if s.Hostname != "UNNAMED" || s.Map != "?" || s.GameDir != "id1" {
		t.Fatalf("defaults not applied: %+v", s)
	}
}
