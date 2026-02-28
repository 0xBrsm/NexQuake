package nqrelay

import (
	"context"
	"encoding/binary"
	"testing"
)

func TestBuildCCREQServerInfo(t *testing.T) {
	req := BuildCCREQServerInfo()
	if len(req) < 12 {
		t.Fatalf("BuildCCREQServerInfo() produced short packet: %d", len(req))
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

	hostname, mapName, players, maxPlayers, proto, ok := ParseCCREPServerInfo(packet)
	if !ok {
		t.Fatalf("ParseCCREPServerInfo() returned ok=false for valid packet")
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

	_, _, _, _, _, ok := ParseCCREPServerInfo(packet)
	if ok {
		t.Fatalf("ParseCCREPServerInfo() returned ok=true for mismatched protocol")
	}
}

func TestBuildWSFrame(t *testing.T) {
	frame := buildWSFrame(26000, []byte{1, 2, 3})
	if len(frame) != 5 {
		t.Fatalf("buildWSFrame() len = %d, want 5", len(frame))
	}
	if frame[0] != byte(26000>>8) || frame[1] != byte(26000&0xff) {
		t.Fatalf("unexpected port header: [%d %d]", frame[0], frame[1])
	}
	if frame[2] != 1 || frame[3] != 2 || frame[4] != 3 {
		t.Fatalf("unexpected payload bytes: %v", frame[2:])
	}

	if frame := buildWSFrame(-1, nil); frame != nil {
		t.Fatalf("buildWSFrame(-1) should be nil")
	}
	if frame := buildWSFrame(70000, nil); frame != nil {
		t.Fatalf("buildWSFrame(70000) should be nil")
	}
}

func TestDecodeWSFrame(t *testing.T) {
	if _, _, ok := decodeWSFrame([]byte{1}); ok {
		t.Fatalf("decodeWSFrame() should reject short packets")
	}

	frame := buildWSFrame(26000, []byte{9, 8, 7})
	port, payload, ok := decodeWSFrame(frame)
	if !ok {
		t.Fatalf("decodeWSFrame() returned ok=false for valid frame")
	}
	if port != 26000 {
		t.Fatalf("decoded port = %d, want 26000", port)
	}
	if string(payload) != string([]byte{9, 8, 7}) {
		t.Fatalf("decoded payload = %v, want [9 8 7]", payload)
	}
}

func TestBuildWSClientIdentityFrame(t *testing.T) {
	if frame := buildWSClientIdentityFrame([4]byte{}); frame != nil {
		t.Fatalf("expected nil identity frame for zero client ip")
	}

	frame := buildWSClientIdentityFrame([4]byte{127, 100, 10, 1})
	if len(frame) != WSPortHeaderSize+len(wsClientIdentityMagic)+4 {
		t.Fatalf("identity frame len = %d", len(frame))
	}
	if frame[0] != byte(ControlPort>>8) || frame[1] != byte(ControlPort) {
		t.Fatalf("identity frame port header = [%d %d], want control port", frame[0], frame[1])
	}
	if string(frame[WSPortHeaderSize:WSPortHeaderSize+len(wsClientIdentityMagic)]) != wsClientIdentityMagic {
		t.Fatalf("identity frame magic mismatch")
	}
}

func TestRelayHandleWSFrame_ControlDispatch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	called := false
	r := &Relay{
		wsTx:   make(chan []byte, 1),
		ctx:    ctx,
		cancel: cancel,
		warnf:  noopLogf,
		debugf: noopLogf,
		dispatch: FrameDispatch{
			HandleControlFrame: func(relay *Relay, payload []byte) []byte {
				called = true
				if string(payload) != "ping" {
					t.Fatalf("control payload = %q, want %q", string(payload), "ping")
				}
				return []byte("pong")
			},
		},
	}

	r.handleWSFrame(buildWSFrame(ControlPort, []byte("ping")))
	if !called {
		t.Fatalf("HandleControlFrame callback was not called")
	}

	select {
	case frame := <-r.wsTx:
		if frame[0] != byte(ControlPort>>8) || frame[1] != byte(ControlPort) {
			t.Fatalf("response frame routed to non-zero port: [%d %d]", frame[0], frame[1])
		}
		if string(frame[WSPortHeaderSize:]) != "pong" {
			t.Fatalf("response payload = %q, want %q", string(frame[WSPortHeaderSize:]), "pong")
		}
	default:
		t.Fatalf("expected control response frame")
	}
}

func TestRelayHandleWSFrame_DropsDisallowedPort(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	r := &Relay{
		ctx:    ctx,
		cancel: cancel,
		warnf:  noopLogf,
		debugf: noopLogf,
		dispatch: FrameDispatch{
			IsAllowedPort: func(port int) bool { return false },
		},
	}

	r.handleWSFrame(buildWSFrame(26000, []byte{1}))
	if got := r.activeServerPort(); got != 0 {
		t.Fatalf("activeServerPort = %d, want 0 after disallowed frame", got)
	}
}
