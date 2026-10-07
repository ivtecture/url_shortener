package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
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
	AdFlushInterval    int
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
		AdFlushInterval:    getEnvInt("AD_FLUSH_INTERVAL_SECONDS", 5),
	}
}

// adEntry — баннер из static/ads/ad_images.txt.
// Формат строки: image|link|weight, где weight — относительная частота
// показа (по умолчанию 1). Key = имя файла, оно же идентификатор в метриках.
type adEntry struct {
	Image  string
	Link   string
	Key    string
	Weight int
}

func parseAdLine(line string) adEntry {
	fields := strings.SplitN(line, "|", 3)
	image := strings.TrimSpace(fields[0])
	entry := adEntry{Image: image, Key: image, Weight: 1}
	if len(fields) > 1 {
		entry.Link = strings.TrimSpace(fields[1])
	}
	if len(fields) > 2 {
		entry.Weight = parseAdWeight(fields[2])
	}
	return entry
}

// fallbackAdEntries строит список из ADS_FILES, когда файла с баннерами нет.
func fallbackAdEntries(files []string) []adEntry {
	out := make([]adEntry, 0, len(files))
	for _, f := range files {
		name := strings.TrimSpace(f)
		out = append(out, adEntry{Image: name, Key: name, Weight: 1})
	}
	return out
}

func loadAdEntries(cfg Config) []adEntry {
	if cfg.AdImagesFile == "" {
		return fallbackAdEntries(cfg.AdsFiles)
	}
	data, err := os.ReadFile(cfg.AdImagesFile)
	if err != nil {
		return fallbackAdEntries(cfg.AdsFiles)
	}
	var out []adEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, parseAdLine(line))
	}
	if len(out) > 0 {
		return out
	}
	return fallbackAdEntries(cfg.AdsFiles)
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

	adStats := NewAdStatsRecorder(db, time.Duration(cfg.AdFlushInterval)*time.Second)

	server := &Server{cfg: cfg, db: db, cache: cache, geo: geo, adStats: adStats}

	// Отмена контекста останавливает flush-цикл и заставляет его дописать
	// несохранённые счётчики рекламы перед выходом.
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go adStats.Run(runCtx)

	addr := ":" + cfg.Port
	httpServer := &http.Server{
		Addr:              addr,
		Handler:           server.routes(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("url-shortener listening on %s", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("http server: %v", err)
	case <-runCtx.Done():
		log.Print("shutdown signal received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}
	stop()
}
