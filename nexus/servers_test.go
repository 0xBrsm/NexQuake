package main

import "testing"

func TestParsePortConsoleLine(t *testing.T) {
	port, ok := parsePortConsoleLine("\"port\" is \"26000\"")
	if !ok {
		t.Fatalf("expected parse success")
	}
	if port != 26000 {
		t.Fatalf("expected port 26000, got %d", port)
	}

	if _, ok := parsePortConsoleLine("UDP Initialized"); ok {
		t.Fatalf("unexpected parse success for unrelated line")
	}
	if _, ok := parsePortConsoleLine("\"port\" is \"abc\""); ok {
		t.Fatalf("unexpected parse success for non-numeric port")
	}
	if _, ok := parsePortConsoleLine("\"port\" is \"70000\""); ok {
		t.Fatalf("unexpected parse success for out-of-range port")
	}
}

func TestUpdateServerListenPort_RekeysRecord(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir())

	rec := &serverRecord{
		launch: serverLaunch{
			spec: serverSpec{
				ListenPort: 0,
			},
		},
		running: &managedServer{
			spec: serverSpec{
				ListenPort: 0,
			},
		},
	}
	m.serversByPort[0] = []*serverRecord{rec}

	m.updateServerListenPort(rec, 26001)

	if rec.launch.spec.ListenPort != 26001 {
		t.Fatalf("expected launch listen port updated to 26001, got %d", rec.launch.spec.ListenPort)
	}
	if rec.running.spec.ListenPort != 26001 {
		t.Fatalf("expected running listen port updated to 26001, got %d", rec.running.spec.ListenPort)
	}
	if _, ok := m.serversByPort[0]; ok {
		t.Fatalf("expected old bucket (0) removed after re-key")
	}
	recs := m.serversByPort[26001]
	if len(recs) != 1 || recs[0] != rec {
		t.Fatalf("expected record re-keyed to 26001, got %#v", recs)
	}
}
