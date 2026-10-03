package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"url_shortener/shortcuts"
)

// Ключи событий рекламы.
const (
	adEventImpression = "impression"
	adEventClick      = "click"
)

// adKeyPattern ограничивает ключ баннера символами, безопасными для
// URL-сегмента и имени файла в static/ads/.
var adKeyPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// Ссылка рекламодателя попадает в заголовок Location. Файл ad_images.txt
// лежит в публично отдаваемом static/ads/, поэтому схему проверяем по
// белому списку: иначе в Location мог бы уйти javascript: или data:.
var adLinkSchemes = map[string]bool{"http": true, "https": true}

// validAdLink проверяет, что цель рекламодателя — обычный http(s)-URL.
func validAdLink(raw string) bool {
	if strings.TrimSpace(raw) == "" {
		return false
	}
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return false
	}
	return adLinkSchemes[strings.ToLower(u.Scheme)]
}

// loadAdEntries / loadAdTexts перечитывают файлы на каждый запрос, чтобы
// правки списка баннеров применялись без пересборки и рестарта.
func (s *Server) adEntries() []adEntry {
	return loadAdEntries(s.cfg)
}

func (s *Server) adTexts() []string {
	return loadAdTexts(s.cfg)
}

// findAdByKey ищет баннер по ключу (имени файла) среди актуальных записей.
func findAdByKey(entries []adEntry, key string) (adEntry, bool) {
	for _, entry := range entries {
		if entry.Key == key {
			return entry, true
		}
	}
	return adEntry{}, false
}

// pickAd выбирает баннер пропорционально весу из ad_images.txt.
// Вес задаётся третьим полем (`image|link|weight`), дефолт — 1.
// Равномерный rand.Intn не позволял управлять частотностью показа,
// поэтому при равных весах поведение совпадает со старым.
func pickAd(entries []adEntry) (adEntry, bool) {
	total := 0
	for _, entry := range entries {
		total += entry.Weight
	}
	if total <= 0 {
		return adEntry{}, false
	}
	roll := rand.Intn(total)
	for _, entry := range entries {
		roll -= entry.Weight
		if roll < 0 {
			return entry, true
		}
	}
	return entries[len(entries)-1], true
}

// ---------------------------------------------------------------------------
// Метрики рекламы
// ---------------------------------------------------------------------------

// adCounterKey — ключ агрегата в буфере. linkID = 0 означает сквозной
// счётчик по баннеру, linkID > 0 — счётчик конкретной ссылки.
type adCounterKey struct {
	adKey     string
	eventType string
	linkID    int64
}

// AdStatsRecorder копит показы и клики в памяти и пачками пишет их в ad_stats
// раз в FlushInterval. Писать в SQLite на каждый редирект нельзя: файл один
// на оба сервиса (docs/ARCHITECTURE.md, раздел 9), лишние транзакции в
// редиректе — лишний contention.
type AdStatsRecorder struct {
	db            *Database
	flushInterval time.Duration

	mu     sync.Mutex
	counts map[adCounterKey]int64
}

func NewAdStatsRecorder(db *Database, flushInterval time.Duration) *AdStatsRecorder {
	if flushInterval <= 0 {
		flushInterval = 5 * time.Second
	}
	return &AdStatsRecorder{
		db:            db,
		flushInterval: flushInterval,
		counts:        make(map[adCounterKey]int64),
	}
}

// Record увеличивает счётчик. Холостой и горячий пути редиректа не блокируются
// дольше времени на взятие мьютекса.
func (r *AdStatsRecorder) Record(adKey, eventType string, linkID int64) {
	if r == nil || adKey == "" {
		return
	}
	key := adCounterKey{adKey: adKey, eventType: eventType, linkID: linkID}
	r.mu.Lock()
	r.counts[key]++
	r.mu.Unlock()
}

// Pending возвращает текущее количество несохранённых событий.
func (r *AdStatsRecorder) Pending() int {
	if r == nil {
		return 0
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	total := int64(0)
	for _, count := range r.counts {
		total += count
	}
	return int(total)
}

// Flush записывает накопленные счётчики в ad_stats. События, попавшие в буфер
// во время записи, остаются на следующий flush — потеря счётчика на пакете
// допустима, потеря события допустима тем более.
func (r *AdStatsRecorder) Flush(ctx context.Context) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	batch := r.counts
	r.counts = make(map[adCounterKey]int64, len(batch))
	r.mu.Unlock()

	if len(batch) == 0 {
		return nil
	}

	for key, count := range batch {
		if _, err := r.db.Execute(ctx, `
			INSERT INTO ad_stats (ad_key, event_type, link_id, count, updated_at)
			VALUES (?, ?, ?, ?, datetime('now'))
			ON CONFLICT (ad_key, event_type, link_id) DO UPDATE SET
				count      = count + excluded.count,
				updated_at = excluded.updated_at`,
			key.adKey, key.eventType, key.linkID, count); err != nil {
			return err
		}
	}
	return nil
}

// Run периодически сбрасывает буфер до отмены контекста.
func (r *AdStatsRecorder) Run(ctx context.Context) {
	if r == nil {
		return
	}
	ticker := time.NewTicker(r.flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Финальный flush на остановке сервиса, чтобы не потерять хвост.
			flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			if err := r.Flush(flushCtx); err != nil {
				log.Printf("ad stats final flush: %v", err)
			}
			cancel()
			return
		case <-ticker.C:
			flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if err := r.Flush(flushCtx); err != nil {
				log.Printf("ad stats flush: %v", err)
			}
			cancel()
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP-обработчики рекламы
// ---------------------------------------------------------------------------

type adAggregate struct {
	AdKey       string  `json:"ad_key"`
	Impressions int64   `json:"impressions"`
	Clicks      int64   `json:"clicks"`
	CTR         float64 `json:"ctr"`
}

type adStatsResponse struct {
	Ads              []adAggregate `json:"ads"`
	TotalImpressions int64         `json:"total_impressions"`
	TotalClicks      int64         `json:"total_clicks"`
	CTR              float64       `json:"ctr"`
}

type linkAdStatsResponse struct {
	ShortCode   string  `json:"short_code"`
	Impressions int64   `json:"impressions"`
	Clicks      int64   `json:"clicks"`
	CTR         float64 `json:"ctr"`
}

func ctr(clicks, impressions int64) float64 {
	if impressions == 0 {
		return 0
	}
	return float64(clicks) / float64(impressions)
}

// getAdStats отдаёт агрегаты по всем баннерам. Перед чтением буфер сбрасывается,
// иначе ответ систематически отставал бы от реальности на flush-интервал.
func (s *Server) getAdStats(w http.ResponseWriter, r *http.Request) {
	if err := s.adStats.Flush(r.Context()); err != nil {
		log.Printf("ad stats flush before read: %v", err)
	}

	rows, err := s.db.FetchAll(r.Context(), `
		SELECT ad_key,
		       COALESCE(SUM(CASE WHEN event_type = ? THEN count END), 0) AS impressions,
		       COALESCE(SUM(CASE WHEN event_type = ? THEN count END), 0) AS clicks
		FROM ad_stats
		WHERE link_id = 0
		GROUP BY ad_key
		ORDER BY impressions DESC, ad_key`, adEventImpression, adEventClick)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "База данных недоступна")
		return
	}
	defer rows.Close()

	ads := []adAggregate{}
	for rows.Next() {
		var item adAggregate
		if err := rows.Scan(&item.AdKey, &item.Impressions, &item.Clicks); err != nil {
			continue
		}
		item.CTR = ctr(item.Clicks, item.Impressions)
		ads = append(ads, item)
	}

	resp := adStatsResponse{Ads: ads}
	for _, item := range ads {
		resp.TotalImpressions += item.Impressions
		resp.TotalClicks += item.Clicks
	}
	resp.CTR = ctr(resp.TotalClicks, resp.TotalImpressions)
	writeJSON(w, http.StatusOK, resp)
}

// getLinkAdStats отдаёт статистику рекламы по конкретной ссылке.
func (s *Server) getLinkAdStats(w http.ResponseWriter, r *http.Request) {
	code := r.PathValue("short_code")
	if !shortcuts.IsValidShortCode(code) {
		writeError(w, http.StatusNotFound, "Ссылка не найдена")
		return
	}

	var id int64
	if err := s.db.FetchOne(r.Context(),
		"SELECT id FROM links WHERE short_code = ?", code).Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			writeError(w, http.StatusNotFound, "Ссылка не найдена")
			return
		}
		writeError(w, http.StatusInternalServerError, "База данных недоступна")
		return
	}

	if err := s.adStats.Flush(r.Context()); err != nil {
		log.Printf("ad stats flush before read: %v", err)
	}

	var impressions, clicks int64
	if err := s.db.FetchOne(r.Context(), `
		SELECT COALESCE(SUM(CASE WHEN event_type = ? THEN count END), 0),
		       COALESCE(SUM(CASE WHEN event_type = ? THEN count END), 0)
		FROM ad_stats WHERE link_id = ?`,
		adEventImpression, adEventClick, id).Scan(&impressions, &clicks); err != nil {
		writeError(w, http.StatusInternalServerError, "База данных недоступна")
		return
	}

	writeJSON(w, http.StatusOK, linkAdStatsResponse{
		ShortCode:   code,
		Impressions: impressions,
		Clicks:      clicks,
		CTR:         ctr(clicks, impressions),
	})
}

// adClick — клик по баннеру. Раньше шаблон вёл напрямую на рекламодателя, и
// сервер о клике не узнавал вовсе, поэтому CTR не измерялся. Теперь переход
// идёт через сервис: счётчик клика плюс 302 на рекламодателя.
func (s *Server) adClick(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("ad_key")
	if !adKeyPattern.MatchString(key) {
		writeError(w, http.StatusNotFound, "Баннер не найден")
		return
	}

	entry, ok := findAdByKey(s.adEntries(), key)
	if !ok || !validAdLink(entry.Link) {
		writeError(w, http.StatusNotFound, "Баннер не найден")
		return
	}

	if linkID, err := strconv.ParseInt(r.URL.Query().Get("link"), 10, 64); err == nil && linkID > 0 {
		s.adStats.Record(key, adEventClick, linkID)
	}
	s.adStats.Record(key, adEventClick, 0)

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	http.Redirect(w, r, entry.Link, http.StatusFound)
}

// adClickURL собирает адрес счётчика клика для шаблона страницы перехода.
func adClickURL(adKey string, linkID int64) string {
	if adKey == "" {
		return ""
	}
	return "/ad-click/" + adKey + "?link=" + strconv.FormatInt(linkID, 10)
}

// parseAdWeight читает вес баннера; некорректное значение — дефолт 1,
// чтобы опечатка в ad_images.txt не выключала рекламу целиком.
func parseAdWeight(raw string) int {
	weight, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || weight < 1 {
		return 1
	}
	return weight
}
