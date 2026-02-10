package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var fixedStartedAt = time.Date(2026, time.January, 8, 14, 5, 6, 0, time.UTC)

func loadServersINI(t *testing.T, content string) ([]serverLaunch, bool, error) {
	t.Helper()

	dir := t.TempDir()
	path := filepath.Join(dir, "servers.ini")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write servers.ini: %v", err)
	}

	return loadServerLaunchesFromINIAt(path, fixedStartedAt)
}

func TestLoadServerLaunchesFromINI_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.ini")

	entries, found, err := loadServerLaunchesFromINIAt(path, fixedStartedAt)
	if err != nil {
		t.Fatalf("loadServerLaunchesFromINI() error = %v", err)
	}
	if found {
		t.Fatalf("expected found=false for missing file")
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty entries for missing file, got entries=%d", len(entries))
	}
}

func TestLoadServerLaunchesFromINI_EmptyFileIsError(t *testing.T) {
	_, found, err := loadServersINI(t, " # comments only\n\n// and blanks\n")
	if !found {
		t.Fatalf("expected found=true when servers.ini exists")
	}
	if err == nil {
		t.Fatalf("expected error for empty servers.ini")
	}
}

func TestLoadServerLaunchesFromINI_ParsesRawArgsUnchanged(t *testing.T) {
	entries, found, err := loadServersINI(t, "\n# launch list\n// also a comment\nnqserver -dedicated 8 -game ctf\n")
	if err != nil {
		t.Fatalf("loadServerLaunchesFromINI() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}

	e := entries[0]
	if e.binary != "nqserver" {
		t.Fatalf("expected binary nqserver, got %q", e.binary)
	}
	if e.spec.Slot != 0 {
		t.Fatalf("expected slot 0, got %d", e.spec.Slot)
	}
	if e.spec.ModName != "ctf" {
		t.Fatalf("expected mod ctf, got %q", e.spec.ModName)
	}
	if e.spec.ListenPort != 0 {
		t.Fatalf("expected unknown listen port without explicit flag, got %d", e.spec.ListenPort)
	}
	if e.spec.LogDir != "ctf-20260108T140506Z-0" {
		t.Fatalf("expected log dir game-starttime-id, got %q", e.spec.LogDir)
	}
	want := []string{"-dedicated", "8", "-game", "ctf"}
	if len(e.args) != len(want) {
		t.Fatalf("expected raw args %v, got %v", want, e.args)
	}
	for i := range want {
		if e.args[i] != want[i] {
			t.Fatalf("expected raw args %v, got %v", want, e.args)
		}
	}
}

func TestLoadServerLaunchesFromINI_ExplicitPortTokenPassesThrough(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -dedicated 8 -port 26076 +hostname alpha\n")
	if err != nil {
		t.Fatalf("loadServerLaunchesFromINI() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].binary != "nqserver" {
		t.Fatalf("expected binary nqserver, got %q", entries[0].binary)
	}
	if entries[0].spec.Slot != 0 {
		t.Fatalf("expected slot 0 (entry order), got %d", entries[0].spec.Slot)
	}
	if entries[0].spec.ListenPort != 0 {
		t.Fatalf("expected unknown listen port (servers.ini is not parsed for port), got %d", entries[0].spec.ListenPort)
	}
	if entries[0].spec.LogDir != "id1-20260108T140506Z-0" {
		t.Fatalf("expected id1 fallback log dir with entry-order id, got %q", entries[0].spec.LogDir)
	}
	if len(entries[0].args) == 0 || entries[0].args[0] != "-dedicated" {
		t.Fatalf("expected first token after binary to be first arg, got %v", entries[0].args)
	}
}

func TestLoadServerLaunchesFromINI_IPXPortTokenPassesThrough(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -dedicated 8 -ipxport 26031\n")
	if err != nil {
		t.Fatalf("loadServerLaunchesFromINI() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].spec.Slot != 0 || entries[0].spec.ListenPort != 0 {
		t.Fatalf("expected slot=0 and unknown listen port, got %d/%d", entries[0].spec.Slot, entries[0].spec.ListenPort)
	}
}

func TestLoadServerLaunchesFromINI_RespectsQuotes(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -dedicated +hostname \"my server\" -game ctf\n")
	if err != nil {
		t.Fatalf("loadServerLaunchesFromINI() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].binary != "nqserver" {
		t.Fatalf("expected binary nqserver, got %q", entries[0].binary)
	}
	want := []string{"-dedicated", "+hostname", "my server", "-game", "ctf"}
	if len(entries[0].args) != len(want) {
		t.Fatalf("expected args %v, got %v", want, entries[0].args)
	}
	for i := range want {
		if entries[0].args[i] != want[i] {
			t.Fatalf("expected args %v, got %v", want, entries[0].args)
		}
	}
	if entries[0].spec.ModName != "ctf" {
		t.Fatalf("expected mod ctf, got %q", entries[0].spec.ModName)
	}
}

func TestLoadServerLaunchesFromINI_AllowsMissingPortValuePassThrough(t *testing.T) {
	entries, found, err := loadServersINI(t, "nqserver -dedicated 8 -port\n")
	if err != nil {
		t.Fatalf("loadServerLaunchesFromINI() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].spec.Slot != 0 {
		t.Fatalf("expected default slot 0, got %d", entries[0].spec.Slot)
	}
	if entries[0].spec.ListenPort != 0 {
		t.Fatalf("expected unknown listen port when -port value missing, got %d", entries[0].spec.ListenPort)
	}
	if !strings.Contains(strings.Join(entries[0].args, " "), "-port") {
		t.Fatalf("expected malformed -port to pass through untouched, got %v", entries[0].args)
	}
}

func TestLoadServerLaunchesFromINI_AllowsCustomBinary(t *testing.T) {
	entries, found, err := loadServersINI(t, "/opt/quake/customsv -dedicated 6 -game custommod\n")
	if err != nil {
		t.Fatalf("loadServerLaunchesFromINI() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].binary != "/opt/quake/customsv" {
		t.Fatalf("expected custom binary path, got %q", entries[0].binary)
	}
	if entries[0].spec.ListenPort != 0 {
		t.Fatalf("expected unknown listen port without explicit flag, got %d", entries[0].spec.ListenPort)
	}
	want := []string{"-dedicated", "6", "-game", "custommod"}
	if len(entries[0].args) != len(want) {
		t.Fatalf("expected unchanged args %v, got %v", want, entries[0].args)
	}
	for i := range want {
		if entries[0].args[i] != want[i] {
			t.Fatalf("expected unchanged args %v, got %v", want, entries[0].args)
		}
	}
}

func TestLoadServerLaunchesFromINI_LogDirIncludesServerIDForSameGame(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.ini")
	content := "nqserver -dedicated 8 -game ctf\nnqserver -dedicated 12 -game ctf\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write servers.ini: %v", err)
	}

	startedAt := time.Date(2026, time.January, 8, 14, 5, 6, 0, time.UTC)
	entries, found, err := loadServerLaunchesFromINIAt(path, startedAt)
	if err != nil {
		t.Fatalf("loadServerLaunchesFromINIAt() error = %v", err)
	}
	if !found {
		t.Fatalf("expected found=true")
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].spec.LogDir != "ctf-20260108T140506Z-0" {
		t.Fatalf("expected first log dir with id suffix, got %q", entries[0].spec.LogDir)
	}
	if entries[1].spec.LogDir != "ctf-20260108T140506Z-1" {
		t.Fatalf("expected second log dir with id suffix, got %q", entries[1].spec.LogDir)
	}
	if entries[0].spec.ListenPort != 0 || entries[1].spec.ListenPort != 0 {
		t.Fatalf("expected unknown listen ports without explicit flags, got %d,%d", entries[0].spec.ListenPort, entries[1].spec.ListenPort)
	}
	if strings.Contains(strings.Join(entries[0].args, " "), "-port") {
		t.Fatalf("expected first args to remain unchanged (no auto -port), got %v", entries[0].args)
	}
	if strings.Contains(strings.Join(entries[1].args, " "), "-port") {
		t.Fatalf("expected second args to remain unchanged (no auto -port), got %v", entries[1].args)
	}
}

func TestPlanLaunches_DefaultWhenServersINIMissing(t *testing.T) {
	m := NewServerManager(t.TempDir(), t.TempDir())

	launches, _, err := m.planLaunches()
	if err != nil {
		t.Fatalf("expected default launch when servers.ini missing, got %v", err)
	}
	if len(launches) != 1 {
		t.Fatalf("expected single default launch, got %d", len(launches))
	}
	if launches[0].spec.Slot != 0 {
		t.Fatalf("expected default slot 0, got %d", launches[0].spec.Slot)
	}
	if launches[0].spec.ListenPort != 0 {
		t.Fatalf("expected unknown default server port before runtime probe, got %d", launches[0].spec.ListenPort)
	}
	if launches[0].spec.ModName != "" {
		t.Fatalf("expected no forced -game for default launch, got mod %q", launches[0].spec.ModName)
	}
	if len(launches[0].args) != 1 || launches[0].args[0] != "-dedicated" {
		t.Fatalf("expected default launch to run dedicated with no -port/-game, got %v", launches[0].args)
	}
}

func TestPlanLaunches_LeavesBareNQServerUnchanged(t *testing.T) {
	dataDir := t.TempDir()
	path := filepath.Join(dataDir, "servers.ini")
	content := "nqserver -dedicated 8 -game ctf\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write servers.ini: %v", err)
	}

	m := NewServerManager(dataDir, t.TempDir())
	launches, _, err := m.planLaunches()
	if err != nil {
		t.Fatalf("planLaunches() error = %v", err)
	}
	if len(launches) != 1 {
		t.Fatalf("expected single launch entry, got %d", len(launches))
	}
	if launches[0].binary != "nqserver" {
		t.Fatalf("expected bare nqserver to remain unchanged, got %q", launches[0].binary)
	}
	if launches[0].spec.ListenPort != 0 {
		t.Fatalf("expected unknown listen port without explicit flag, got %d", launches[0].spec.ListenPort)
	}
	if strings.Contains(strings.Join(launches[0].args, " "), "-port") {
		t.Fatalf("expected args to remain unchanged (no auto -port), got %v", launches[0].args)
	}
}
