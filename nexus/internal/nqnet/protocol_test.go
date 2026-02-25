package nqnet

import (
	"encoding/binary"
	"testing"
)

func TestBuildCCREPServerListEncodesU16Fields(t *testing.T) {
	packet, count := BuildCCREPServerList([]ServerListEntry{{
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

	control := binary.BigEndian.Uint32(packet[:4])
	if (control &^ netFlagLengthMask) != netFlagCtl {
		t.Fatalf("control flag mismatch: %#x", control)
	}
	if got := int(control & netFlagLengthMask); got != len(packet) {
		t.Fatalf("length header = %d, want %d", got, len(packet))
	}
	if packet[4] != ccrepServerInfo {
		t.Fatalf("message type = %#x, want %#x", packet[4], ccrepServerInfo)
	}
	if packet[5] != 1 {
		t.Fatalf("entry count byte = %d, want 1", packet[5])
	}

	i := 6
	portText, next, ok := readCString(packet, i)
	if !ok {
		t.Fatalf("missing port text")
	}
	if portText != "26000" {
		t.Fatalf("port text = %q, want 26000", portText)
	}
	i = next

	hostname, next, ok := readCString(packet, i)
	if !ok {
		t.Fatalf("missing hostname")
	}
	if hostname != "fragfest" {
		t.Fatalf("hostname = %q, want fragfest", hostname)
	}
	i = next

	mapName, next, ok := readCString(packet, i)
	if !ok {
		t.Fatalf("missing map")
	}
	if mapName != "dm6" {
		t.Fatalf("map = %q, want dm6", mapName)
	}
	i = next

	gameDir, next, ok := readCString(packet, i)
	if !ok {
		t.Fatalf("missing gamedir")
	}
	if gameDir != "id1" {
		t.Fatalf("gamedir = %q, want id1", gameDir)
	}
	i = next

	if i+7 > len(packet) {
		t.Fatalf("missing numeric tail")
	}
	users := uint16(packet[i]) | uint16(packet[i+1])<<8
	maxUsers := uint16(packet[i+2]) | uint16(packet[i+3])<<8
	instances := uint16(packet[i+4]) | uint16(packet[i+5])<<8
	protocol := packet[i+6]

	if users != 873 {
		t.Fatalf("users = %d, want 873", users)
	}
	if maxUsers != 10000 {
		t.Fatalf("maxUsers = %d, want 10000", maxUsers)
	}
	if instances != 100 {
		t.Fatalf("instances = %d, want 100", instances)
	}
	if protocol != netProtocolVersion {
		t.Fatalf("protocol = %d, want %d", protocol, netProtocolVersion)
	}
}

func TestBuildCCREPServerListDefaultsInstancesToOne(t *testing.T) {
	packet, count := BuildCCREPServerList([]ServerListEntry{{
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
