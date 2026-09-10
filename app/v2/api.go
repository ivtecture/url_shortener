package main

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"url_shortener/shortcuts"
)

const maxURLLength = 2048

//go:embed routing_page.html
var routingPageFS embed.FS

var routingPageTmpl = template.Must(template.ParseFS(routingPageFS, "routing_page.html"))

type routingPageData struct {
	TargetURL string
	AdImage   string
	AdLink    string
	AdText    string
}

type Server struct {
	cfg   Config
	db    *Database
	cache *Cache
	geo   *GeoResolver
}

func (s *Server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v2/links", s.createLink)
	mux.HandleFunc("GET /{short_code}", s.redirect)
	mux.HandleFunc("GET /api/v2/links/{short_code}/stats", s.getStats)
	mux.HandleFunc("DELETE /api/v2/links/delete/{short_code}", s.deleteLink)
	mux.HandleFunc("GET /api/v2/health", s.health)
	return mux
}

type linkCreateRequest struct {
	OriginalURL string `json:"original_url"`
}

type linkResponse struct {
	ShortCode   string `json:"short_code"`
	ShortURL    string `json:"short_url"`
	OriginalURL string `json:"original_url"`
	CreatedAt   string `json:"created_at"`
}

type countryClicks struct {
	Country string `json:"country"`
	Count   int64  `json:"count"`
}

type statsResponse struct {
	ShortCode   string          `json:"short_code"`
	OriginalURL string          `json:"original_url"`
	CreatedAt   string          `json:"created_at"`
	Clicks      int64           `json:"clicks"`
	Countries   []countryClicks `json:"countries"`
}

type deleteResponse struct {
	ShortCode string `json:"short_code"`
	Deleted   bool   `json:"deleted"`
}

type healthResponse struct {
	Status string `json:"status"`
	DB     bool   `json:"db"`
	Redis  bool   `json:"redis"`
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func writeError(w http.ResponseWriter, status int, detail string) {
	writeJSON(w, status, map[string]string{"detail": detail})
}

func buildShortURL(baseURL, shortCode string) string {
	return strings.TrimRight(baseURL, "/") + "/" + shortCode
}

// renderRoutingPage исполняет шаблон страницы перехода.
// Если определён ROUTING_PAGE_FILE и файл читается — шаблон берётся с диска
// на каждый запрос (правки применяются без рестарта сервиса);
// иначе используется встроенный в бинарник routingPageTmpl.
func (s *Server) renderRoutingPage(w http.ResponseWriter, data routingPageData) {
	if s.cfg.RoutingPageFile != "" {
		if tmpl, err := template.ParseFiles(s.cfg.RoutingPageFile); err == nil {
			if err := tmpl.Execute(w, data); err != nil {
				log.Printf("routing page: %v", err)
			}
			return
		}
	}
	if err := routingPageTmpl.Execute(w, data); err != nil {
		log.Printf("routing page: %v", err)
	}
}

// adEntries читается из static/ads/ad_images.txt на каждый запрос,
// чтобы правки списка баннеров и их ссылок применялись без рестарта.
func (s *Server) adEntries() []adEntry {
	return loadAdEntries(s.cfg)
}

// adTexts читается из файла на каждый запрос, чтобы правки
// static/ads/ad_texts.txt применялись без рестарта сервиса.
func (s *Server) adTexts() []string {
	return loadAdTexts(s.cfg)
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func isUniqueViolation(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unique constraint failed") || strings.Contains(msg, "constraint failed")
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	var req linkCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "Input should be a valid URL")
		return
	}
	originalURL := strings.TrimSpace(req.OriginalURL)
	parsed, err := url.Parse(originalURL)
	if err != nil || originalURL == "" || parsed.Scheme == "" || parsed.Host == "" {
		writeError(w, http.StatusUnprocessableEntity, "Input should be a valid URL")
		return
	}
	if strings.ToLower(parsed.Scheme) != "http" && strings.ToLower(parsed.Scheme) != "https" {
		writeError(w, http.StatusBadRequest, "Поддерживаются только схемы http и https")
		return
	}
	if len(originalURL) > maxURLLength {
		writeError(w, http.StatusBadRequest, "URL не может быть длиннее 2048 символов")
		return
	}

	for attempt := 0; attempt < shortcuts.MaxAttempts; attempt++ {
		code := shortcuts.GenerateShortCode()
		if _, err := s.db.Execute(r.Context(),
			"INSERT INTO links (short_code, original_url) VALUES (?, ?)", code, originalURL); err != nil {
			if isUniqueViolation(err) {
				continue
			}
			writeError(w, http.StatusServiceUnavailable, "База данных недоступна")
			return
		}

		var createdAt string
		if err := s.db.FetchOne(r.Context(),
			"SELECT created_at FROM links WHERE short_code = ?", code).Scan(&createdAt); err != nil {
			createdAt = ""
		}
		writeJSON(w, http.StatusCreated, linkResponse{
			ShortCode:   code,
			ShortURL:    buildShortURL(s.cfg.BaseURL, code),
			OriginalURL: originalURL,
			CreatedAt:   createdAt,
		})
		return
	}
	writeError(w, http.StatusServiceUnavailable, "Не удалось сгенерировать уникальный код, попробуйте ещё раз")
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("short_code")
	if !shortcuts.IsValidShortCode(code) {
		writeError(w, http.StatusNotFound, "Ссылка не найдена")
		return
	}

	var linkID int64
	var originalURL string

	cacheKey := "url:" + code
	if cached, ok := s.cache.Get(r.Context(), cacheKey); ok {
		if prefix, rest, found := strings.Cut(cached, ":"); found && isDigits(prefix) {
			if id, err := strconv.ParseInt(prefix, 10, 64); err == nil {
				linkID = id
				originalURL = rest
			}
		}
	}

	if originalURL == "" {
		err := s.db.FetchOne(r.Context(),
			"SELECT id, original_url FROM links WHERE short_code = ?", code).Scan(&linkID, &originalURL)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, "Ссылка не найдена")
				return
			}
			writeError(w, http.StatusInternalServerError, "База данных недоступна")
			return
		}
		s.cache.SetEx(r.Context(), cacheKey, s.cfg.CacheTTLSeconds, fmt.Sprintf("%d:%s", linkID, originalURL))
	}

	ip := s.clientIP(r)
	go s.recordClick(linkID, ip)

	ad := "/ads/ad1.svg"
	var adLink string
	if entries := s.adEntries(); len(entries) > 0 {
		entry := entries[rand.Intn(len(entries))]
		ad = "/ads/" + entry.Image
		adLink = entry.Link
	}
	adText := ""
	if texts := s.adTexts(); len(texts) > 0 {
		adText = texts[rand.Intn(len(texts))]
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	s.renderRoutingPage(w, routingPageData{
		TargetURL: originalURL,
		AdImage:   ad,
		AdLink:    adLink,
		AdText:    adText,
	})
}

func (s *Server) clientIP(r *http.Request) string {
	directIP := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		directIP = host
	}
	return extractClientIP(
		r.Header.Get("X-Forwarded-For"),
		r.Header.Get("X-Real-IP"),
		directIP,
	)
}

func (s *Server) recordClick(linkID int64, ip string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	country := s.geo.resolveCountry(ctx, ip)
	_, _ = s.db.Execute(ctx,
		"INSERT INTO analytics (link_id, ip_address, country) VALUES (?, ?, ?)",
		linkID, ip, country)
}

func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("short_code")

	var id int64
	var shortCode, originalURL, createdAt string
	err := s.db.FetchOne(r.Context(),
		"SELECT id, short_code, original_url, created_at FROM links WHERE short_code = ?", code).
		Scan(&id, &shortCode, &originalURL, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Ссылка не найдена")
			return
		}
		writeError(w, http.StatusInternalServerError, "База данных недоступна")
		return
	}

	var clicks int64
	_ = s.db.FetchOne(r.Context(),
		"SELECT COUNT(*) FROM analytics WHERE link_id = ?", id).Scan(&clicks)

	rows, err := s.db.FetchAll(r.Context(),
		`SELECT a.country, COUNT(*) AS cnt
		 FROM analytics a
		 WHERE a.link_id = ?
		 GROUP BY a.country
		 ORDER BY cnt DESC`, id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "База данных недоступна")
		return
	}
	defer rows.Close()

	countries := []countryClicks{}
	for rows.Next() {
		var country sql.NullString
		var count int64
		if err := rows.Scan(&country, &count); err != nil {
			continue
		}
		name := "unknown"
		if country.Valid && country.String != "" {
			name = country.String
		}
		countries = append(countries, countryClicks{Country: name, Count: count})
	}

	writeJSON(w, http.StatusOK, statsResponse{
		ShortCode:   shortCode,
		OriginalURL: originalURL,
		CreatedAt:   createdAt,
		Clicks:      clicks,
		Countries:   countries,
	})
}

func (s *Server) deleteLink(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("short_code")

	deleted, err := s.db.Execute(r.Context(),
		"DELETE FROM links WHERE short_code = ?", code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "База данных недоступна")
		return
	}
	if deleted == 0 {
		writeError(w, http.StatusNotFound, "Ссылка не найдена")
		return
	}
	s.cache.Delete(r.Context(), "url:"+code)
	writeJSON(w, http.StatusOK, deleteResponse{ShortCode: code, Deleted: true})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, healthResponse{
		Status: "ok",
		DB:     s.db.Ping(r.Context()),
		Redis:  s.cache.Ping(r.Context()),
	})
}
