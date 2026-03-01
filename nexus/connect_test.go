package main

import (
	"net/http/httptest"
	"testing"
)

func TestResolveClientSourceIP(t *testing.T) {
	t.Run("uses configured header when valid", func(t *testing.T) {
		t.Setenv("AUTH_CLIENT_IP_HEADER", "X-Real-IP")
		r := httptest.NewRequest("GET", "http://example/ws", nil)
		r.RemoteAddr = "10.0.0.1:3456"
		r.Header.Set("X-Real-IP", "203.0.113.50")

		if got := resolveClientSourceIP(r); got != "203.0.113.50" {
			t.Fatalf("resolveClientSourceIP() = %q, want %q", got, "203.0.113.50")
		}
	})

	t.Run("falls back to remote ip on invalid header", func(t *testing.T) {
		t.Setenv("AUTH_CLIENT_IP_HEADER", "X-Real-IP")
		r := httptest.NewRequest("GET", "http://example/ws", nil)
		r.RemoteAddr = "198.51.100.10:7890"
		r.Header.Set("X-Real-IP", "not-an-ip")

		if got := resolveClientSourceIP(r); got != "198.51.100.10" {
			t.Fatalf("resolveClientSourceIP() = %q, want %q", got, "198.51.100.10")
		}
	})
}

func TestResolveClientSourceKey(t *testing.T) {
	t.Setenv("AUTH_CLIENT_IP_HEADER", "X-Forwarded-For")
	r := httptest.NewRequest("GET", "http://example/ws", nil)
	r.RemoteAddr = "198.51.100.20:1234"
	r.Header.Set("X-Forwarded-For", "203.0.113.7")

	if got := resolveClientSourceKey(r); got != "ip:203.0.113.7" {
		t.Fatalf("resolveClientSourceKey() = %q, want %q", got, "ip:203.0.113.7")
	}
}
