package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabasePath       string
	RedisURL           string
	GeoAPIURL          string
	GeoTimeoutSeconds  int
	CacheTTLSeconds    int
	GeoCacheTTLSeconds int
	BaseURL            string
	Port               string
	AdsFiles           []string
	AdImagesFile       string
	AdTexts            []string
	AdTextsFile        string
	RoutingPageFile    string
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func splitCSV(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func loadConfig() Config {
	return Config{
		DatabasePath:       getEnv("DATABASE_PATH", "/data/urlshortener.db"),
		RedisURL:           getEnv("REDIS_URL", "redis://redis:6379/0"),
		GeoAPIURL:          getEnv("GEO_API_URL", "http://ip-api.com/json/{ip}?fields=status,countryCode"),
		GeoTimeoutSeconds:  getEnvInt("GEO_TIMEOUT_SECONDS", 2),
		CacheTTLSeconds:    getEnvInt("CACHE_TTL_SECONDS", 3600),
		GeoCacheTTLSeconds: getEnvInt("GEO_CACHE_TTL_SECONDS", 86400),
		BaseURL:            getEnv("BASE_URL", "http://localhost"),
		Port:               getEnv("PORT", "8000"),
		AdsFiles:           splitCSV(getEnv("ADS_FILES", "ad1.svg,ad2.svg,ad3.svg")),
		AdImagesFile:       getEnv("AD_IMAGES_FILE", "/static/ads/ad_images.txt"),
		AdTexts:            splitCSV(getEnv("AD_TEXTS", "")),
		AdTextsFile:        getEnv("AD_TEXTS_FILE", "/static/ads/ad_texts.txt"),
		RoutingPageFile:    getEnv("ROUTING_PAGE_FILE", "/templates/routing_page.html"),
	}
}

func loadAdFiles(cfg Config) []string {
	if cfg.AdImagesFile == "" {
		return cfg.AdsFiles
	}
	data, err := os.ReadFile(cfg.AdImagesFile)
	if err != nil {
		return cfg.AdsFiles
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if len(out) > 0 {
		return out
	}
	return cfg.AdsFiles
}

func loadAdTexts(cfg Config) []string {
	if cfg.AdTextsFile == "" {
		return cfg.AdTexts
	}
	data, err := os.ReadFile(cfg.AdTextsFile)
	if err != nil {
		return cfg.AdTexts
	}
	var out []string
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if len(out) > 0 {
		return out
	}
	return cfg.AdTexts
}

func main() {
	cfg := loadConfig()

	db, err := NewDatabase(cfg.DatabasePath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer db.Close()

	initCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := db.InitSchema(initCtx); err != nil {
		log.Fatalf("init schema: %v", err)
	}

	cache, err := NewCache(cfg.RedisURL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	defer cache.Close()

	geo, err := NewGeoResolver(cfg.GeoAPIURL, float64(cfg.GeoTimeoutSeconds), cache, cfg.GeoCacheTTLSeconds)
	if err != nil {
		log.Fatalf("geo: %v", err)
	}

	server := &Server{cfg: cfg, db: db, cache: cache, geo: geo}

	addr := ":" + cfg.Port
	log.Printf("url-shortener listening on %s", addr)
	if err := http.ListenAndServe(addr, server.routes()); err != nil {
		log.Fatal(err)
	}
}
