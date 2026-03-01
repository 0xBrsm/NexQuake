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

func TestBuildCCREPServerListEncodesU16Fields(t *testing.T) {
	packet, count := buildCCREPServerList([]serverListEntry{{
		ListenPort: 26000,
		Hostname:   "fragfest",
		MapName:    "dm6",
		GameDir:    "id1",
		Users:      873,
		MaxUsers:   10000,
		Instances:  100,
	}})
	if count != 1 {
		t.Fatalf("entry count = %d, want 1", count)
	}
	if len(packet) < 8 {
		t.Fatalf("packet too small: %d", len(packet))
	}
	if packet[5] != 1 {
		t.Fatalf("entry count byte = %d, want 1", packet[5])
	}

	i := 6
	portText, next, ok := readCString(packet, i)
	if !ok || portText != "26000" {
		t.Fatalf("port text = %q, want 26000", portText)
	}
	i = next
	hostname, next, ok := readCString(packet, i)
	if !ok || hostname != "fragfest" {
		t.Fatalf("hostname = %q, want fragfest", hostname)
	}
	i = next
	mapName, next, ok := readCString(packet, i)
	if !ok || mapName != "dm6" {
		t.Fatalf("map = %q, want dm6", mapName)
	}
	i = next
	gameDir, next, ok := readCString(packet, i)
	if !ok || gameDir != "id1" {
		t.Fatalf("gamedir = %q, want id1", gameDir)
	}
	i = next

	if i+7 > len(packet) {
		t.Fatalf("missing numeric tail")
	}
	users := uint16(packet[i]) | uint16(packet[i+1])<<8
	maxUsers := uint16(packet[i+2]) | uint16(packet[i+3])<<8
	instances := uint16(packet[i+4]) | uint16(packet[i+5])<<8

	if users != 873 {
		t.Fatalf("users = %d, want 873", users)
	}
	if maxUsers != 10000 {
		t.Fatalf("maxUsers = %d, want 10000", maxUsers)
	}
	if instances != 100 {
		t.Fatalf("instances = %d, want 100", instances)
	}
}

func TestBuildCCREPServerListDefaultsInstancesToOne(t *testing.T) {
	packet, count := buildCCREPServerList([]serverListEntry{{
		ListenPort: 26000,
		Hostname:   "fragfest",
		MapName:    "dm6",
		GameDir:    "id1",
		Users:      1,
		MaxUsers:   16,
		Instances:  0,
	}})
	if count != 1 {
		t.Fatalf("entry count = %d, want 1", count)
	}
	i := 6
	for step := 0; step < 4; step++ {
		_, next, ok := readCString(packet, i)
		if !ok {
			t.Fatalf("missing cstring %d", step)
		}
		i = next
	}
	if i+7 > len(packet) {
		t.Fatalf("missing numeric fields")
	}
	instances := uint16(packet[i+4]) | uint16(packet[i+5])<<8
	if instances != 1 {
		t.Fatalf("instances = %d, want 1", instances)
	}
}
