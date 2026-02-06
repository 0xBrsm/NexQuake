package main

import (
	"net/http/httptest"
	"os"
	"testing"
)

func TestResolveClientSourceKeyUsesConfiguredHeader(t *testing.T) {
	const envKey = "AUTH_CLIENT_IP_HEADER"
	old := os.Getenv(envKey)
	t.Cleanup(func() { _ = os.Setenv(envKey, old) })
	if err := os.Setenv(envKey, "X-Client-IP"); err != nil {
		t.Fatalf("Setenv failed: %v", err)
	}

	req := httptest.NewRequest("GET", "http://example/ws", nil)
	req.RemoteAddr = "10.0.0.2:54321"
	req.Header.Set("X-Client-IP", "2001:db8::1234")

	got := resolveClientSourceKey(req)
	if got != "ip:2001:db8::1234" {
		t.Fatalf("resolveClientSourceKey() = %q, want %q", got, "ip:2001:db8::1234")
	}
}

func TestResolveClientSourceKeyFallsBackToRemoteAddr(t *testing.T) {
	const envKey = "AUTH_CLIENT_IP_HEADER"
	old := os.Getenv(envKey)
	t.Cleanup(func() { _ = os.Setenv(envKey, old) })
	if err := os.Setenv(envKey, ""); err != nil {
		t.Fatalf("Setenv failed: %v", err)
	}

	req := httptest.NewRequest("GET", "http://example/ws", nil)
	req.RemoteAddr = "198.51.100.42:60000"

	got := resolveClientSourceKey(req)
	if got != "ip:198.51.100.42" {
		t.Fatalf("resolveClientSourceKey() = %q, want %q", got, "ip:198.51.100.42")
	}
}
