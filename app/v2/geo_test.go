package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alicebob/miniredis/v2"
)

func TestExtractClientIP(t *testing.T) {
	cases := []struct {
		name      string
		forwarded string
		realIP    string
		direct    string
		want      string
	}{
		{"forwarded wins", "203.0.113.5, 10.0.0.1", "10.0.0.2", "10.0.0.3:5555", "203.0.113.5"},
		{"forwarded with spaces", " 203.0.113.7 , 10.0.0.1", "", "10.0.0.3", "203.0.113.7"},
		{"fallback to real ip", "", "198.51.100.9", "10.0.0.3", "198.51.100.9"},
		// directIP приходит уже без порта: host:port разбирает clientIP.
		{"fallback to direct", "", "", "192.0.2.4", "192.0.2.4"},
		{"all empty", "", "", "", "0.0.0.0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractClientIP(tc.forwarded, tc.realIP, tc.direct)
			if got != tc.want {
				t.Fatalf("extractClientIP(%q, %q, %q) = %q, want %q",
					tc.forwarded, tc.realIP, tc.direct, got, tc.want)
			}
		})
	}
}

func TestIsPrivateIP(t *testing.T) {
	private := []string{"10.0.0.1", "172.16.5.4", "192.168.1.1", "127.0.0.1", "::1", "fd00::1", "169.254.1.1"}
	for _, ip := range private {
		if !isPrivateIP(ip) {
			t.Fatalf("expected %s to be private", ip)
		}
	}
	// Мусорный IP тоже считаем приватным: внешний запрос по нему бесполезен.
	if !isPrivateIP("not-an-ip") {
		t.Fatal("expected malformed ip to be treated as private")
	}
	public := []string{"8.8.8.8", "203.0.113.1", "2001:4860:4860::8888"}
	for _, ip := range public {
		if isPrivateIP(ip) {
			t.Fatalf("expected %s to be public", ip)
		}
	}
}

// resolveCountry для приватного IP не должен ходить во внешний API.
func TestResolveCountryPrivateIPSkipsUpstream(t *testing.T) {
	cache := newTestCache(t)
	upstreamCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","countryCode":"RU"}`))
	}))
	defer server.Close()

	geo, err := NewGeoResolver(server.URL+"/?ip={ip}", 1, cache, 60)
	if err != nil {
		t.Fatalf("NewGeoResolver: %v", err)
	}
	if got := geo.resolveCountry(context.Background(), "10.0.0.1"); got != "local" {
		t.Fatalf("resolveCountry = %q, want local", got)
	}
	if upstreamCalls != 0 {
		t.Fatalf("upstream called %d times for private ip", upstreamCalls)
	}
}

func TestResolveCountryUsesCacheAndUpstream(t *testing.T) {
	cache := newTestCache(t)
	upstreamCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"success","countryCode":"DE"}`))
	}))
	defer server.Close()

	geo, err := NewGeoResolver(server.URL+"/?ip={ip}", 1, cache, 60)
	if err != nil {
		t.Fatalf("NewGeoResolver: %v", err)
	}

	if got := geo.resolveCountry(context.Background(), "203.0.113.1"); got != "DE" {
		t.Fatalf("first resolveCountry = %q, want DE", got)
	}
	if got := geo.resolveCountry(context.Background(), "203.0.113.1"); got != "DE" {
		t.Fatalf("second resolveCountry = %q, want DE", got)
	}
	if upstreamCalls != 1 {
		t.Fatalf("upstream called %d times, want 1 (second call must hit cache)", upstreamCalls)
	}
}

func TestResolveCountryUpstreamFailure(t *testing.T) {
	cache := newTestCache(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"fail"}`))
	}))
	defer server.Close()

	geo, err := NewGeoResolver(server.URL+"/?ip={ip}", 1, cache, 60)
	if err != nil {
		t.Fatalf("NewGeoResolver: %v", err)
	}
	if got := geo.resolveCountry(context.Background(), "203.0.113.2"); got != "unknown" {
		t.Fatalf("resolveCountry = %q, want unknown", got)
	}
}

func TestNewGeoResolverRequiresPlaceholder(t *testing.T) {
	if _, err := NewGeoResolver("http://ip-api.com/json/", 1, newTestCache(t), 60); err == nil {
		t.Fatal("expected error when {ip} placeholder is missing")
	}
}

// newTestCache поднимает miniredis, чтобы тесты не зависели от контейнера.
func newTestCache(t *testing.T) *Cache {
	t.Helper()
	server := miniredis.RunT(t)
	cache, err := NewCache("redis://" + server.Addr() + "/0")
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })
	return cache
}
