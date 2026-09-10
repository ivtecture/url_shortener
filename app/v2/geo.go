package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"
)

type GeoResolver struct {
	baseURL string
	client  *http.Client
	cache   *Cache
	ttl     int
}

func NewGeoResolver(baseURL string, timeoutSeconds float64, cache *Cache, ttl int) (*GeoResolver, error) {
	if !strings.Contains(baseURL, "{ip}") {
		return nil, fmt.Errorf("geo api url must contain {ip} placeholder")
	}
	return &GeoResolver{
		baseURL: baseURL,
		client:  &http.Client{Timeout: time.Duration(timeoutSeconds * float64(time.Second))},
		cache:   cache,
		ttl:     ttl,
	}, nil
}

func extractClientIP(forwardedFor, realIP, directIP string) string {
	if forwardedFor != "" {
		if first := strings.TrimSpace(strings.Split(forwardedFor, ",")[0]); first != "" {
			return first
		}
	}
	if realIP != "" {
		return strings.TrimSpace(realIP)
	}
	if directIP != "" {
		return directIP
	}
	return "0.0.0.0"
}

func isPrivateIP(ip string) bool {
	addr := net.ParseIP(ip)
	if addr == nil {
		return true
	}
	return addr.IsPrivate() || addr.IsLoopback() || addr.IsLinkLocalUnicast() ||
		addr.IsLinkLocalMulticast() || addr.IsUnspecified() || addr.IsMulticast()
}

func (g *GeoResolver) resolveCountry(ctx context.Context, ip string) string {
	if isPrivateIP(ip) {
		return "local"
	}
	if cached, ok := g.cache.Get(ctx, "geo:"+ip); ok && cached != "" {
		return cached
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.ReplaceAll(g.baseURL, "{ip}", ip), nil)
	if err != nil {
		return "unknown"
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return "unknown"
	}
	defer resp.Body.Close()

	var data struct {
		Status      string `json:"status"`
		CountryCode string `json:"countryCode"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "unknown"
	}
	if data.Status == "success" && data.CountryCode != "" {
		g.cache.SetEx(ctx, "geo:"+ip, g.ttl, data.CountryCode)
		return data.CountryCode
	}
	return "unknown"
}
