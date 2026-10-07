package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// newTestServer поднимает полноценный Server на временной SQLite и miniredis.
func newTestServer(t *testing.T) *Server {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	db, err := NewDatabase(dbPath)
	if err != nil {
		t.Fatalf("NewDatabase: %v", err)
	}
	if err := db.InitSchema(t.Context()); err != nil {
		t.Fatalf("InitSchema: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	redisServer := miniredis.RunT(t)
	cache, err := NewCache("redis://" + redisServer.Addr() + "/0")
	if err != nil {
		t.Fatalf("NewCache: %v", err)
	}
	t.Cleanup(func() { _ = cache.Close() })

	geo, err := NewGeoResolver("http://127.0.0.1:1/?ip={ip}", 1, cache, 60)
	if err != nil {
		t.Fatalf("NewGeoResolver: %v", err)
	}

	dir := t.TempDir()
	adsFile := filepath.Join(dir, "ad_images.txt")
	adsContent := "ad1.png|https://ads.example/one|1\nad2.jpg|https://ads.example/two|1\n"
	if err := os.WriteFile(adsFile, []byte(adsContent), 0o644); err != nil {
		t.Fatalf("write ads file: %v", err)
	}
	textsFile := filepath.Join(dir, "ad_texts.txt")
	if err := os.WriteFile(textsFile, []byte("подпись\n"), 0o644); err != nil {
		t.Fatalf("write texts file: %v", err)
	}

	srv := &Server{
		cfg: Config{
			BaseURL:         "http://localhost",
			AdImagesFile:    adsFile,
			AdTextsFile:     textsFile,
			RoutingPageFile: filepath.Join(dir, "missing.html"),
			CacheTTLSeconds: 3600,
		},
		db:    db,
		cache: cache,
		geo:   geo,
		// Длинный интервал: в тестах флаш вызывается вручную, чтобы
		// результат не зависел от таймеров.
		adStats: NewAdStatsRecorder(db, time.Hour),
	}
	t.Cleanup(func() { _ = srv.adStats.Flush(t.Context()) })
	return srv
}

func doJSON(t *testing.T, srv *Server, method, target string, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	payload := map[string]any{}
	if strings.HasPrefix(rec.Header().Get("Content-Type"), "application/json") {
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("%s %s: invalid json %q: %v", method, target, rec.Body.String(), err)
		}
	}
	return rec, payload
}

func createTestLink(t *testing.T, srv *Server, url string) string {
	t.Helper()
	rec, payload := doJSON(t, srv, "POST", "/api/v2/links", fmt.Sprintf(`{"original_url":%q}`, url))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create link: status %d, body %s", rec.Code, rec.Body.String())
	}
	code, _ := payload["short_code"].(string)
	if code == "" {
		t.Fatalf("create link: no short_code in %s", rec.Body.String())
	}
	return code
}

func TestCreateLink(t *testing.T) {
	srv := newTestServer(t)
	code := createTestLink(t, srv, "https://example.com/some/path?x=1")

	rec, payload := doJSON(t, srv, "GET", "/api/v2/links/"+code+"/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("stats: status %d", rec.Code)
	}
	if payload["short_code"] != code {
		t.Fatalf("stats returned wrong code: %v", payload["short_code"])
	}
	if clicks, _ := payload["clicks"].(float64); clicks != 0 {
		t.Fatalf("expected 0 clicks, got %v", payload["clicks"])
	}
}

func TestCreateLinkValidation(t *testing.T) {
	srv := newTestServer(t)
	cases := []struct {
		name string
		body string
		want int
	}{
		{"not json", `{`, http.StatusUnprocessableEntity},
		{"empty url", `{"original_url":""}`, http.StatusUnprocessableEntity},
		{"no scheme", `{"original_url":"example.com"}`, http.StatusUnprocessableEntity},
		{"bad scheme", `{"original_url":"ftp://example.com"}`, http.StatusBadRequest},
		{"too long", `{"original_url":"https://example.com/` + strings.Repeat("a", 2048) + `"}`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec, _ := doJSON(t, srv, "POST", "/api/v2/links", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d (body %s)", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

func TestGetStatsUnknownCode(t *testing.T) {
	srv := newTestServer(t)
	rec, _ := doJSON(t, srv, "GET", "/api/v2/links/zzzzzzz/stats", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestDeleteLink(t *testing.T) {
	srv := newTestServer(t)
	code := createTestLink(t, srv, "https://example.com/gone")

	rec, payload := doJSON(t, srv, "DELETE", "/api/v2/links/delete/"+code, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete: status %d", rec.Code)
	}
	if payload["deleted"] != true {
		t.Fatalf("delete: expected deleted=true, got %v", payload["deleted"])
	}
	if rec2, _ := doJSON(t, srv, "DELETE", "/api/v2/links/delete/"+code, ""); rec2.Code != http.StatusNotFound {
		t.Fatalf("second delete: status %d, want 404", rec2.Code)
	}
}

func TestRedirectUnknownCode(t *testing.T) {
	srv := newTestServer(t)
	rec, _ := doJSON(t, srv, "GET", "/zzzzzzz", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// Раньше редирект отдавал 302 + Location; теперь это 200 + HTML-страница
// рекламы с таймером (см. PR-ревью: семантика описана неверно в README).
func TestRedirectRendersAdPage(t *testing.T) {
	srv := newTestServer(t)
	code := createTestLink(t, srv, "https://example.com/target")

	req := httptest.NewRequest("GET", "/"+code, nil)
	req.RemoteAddr = "10.0.0.5:1234"
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Fatalf("content-type = %q, want text/html", ct)
	}
	if loc := rec.Header().Get("Location"); loc != "" {
		t.Fatalf("unexpected Location header %q", loc)
	}
	body := rec.Body.String()
	// Внутри <script> html/template экранирует строку как JS-литерал,
	// поэтому слэши становятся \/ — сравниваем через Unescape.
	unescaped := strings.NewReplacer(`\/`, "/", `\u003c`, "<", `\u003e`, ">").Replace(body)
	if !strings.Contains(unescaped, "https://example.com/target") {
		t.Fatal("routing page must contain target url")
	}
	// Клик по баннеру обязан идти через счётчик, иначе CTR не измеряется.
	if !strings.Contains(body, "/ad-click/") {
		t.Fatal("routing page must link the ad through /ad-click/")
	}
	if strings.Contains(body, "https://ads.example") {
		t.Fatal("routing page must not link the advertiser directly")
	}
	if srv.adStats.Pending() == 0 {
		t.Fatal("impression was not recorded")
	}
}

// Программный клиент не должен получать HTML с баннером — только 302.
func TestRedirectPlainRedirectForNonHTMLClients(t *testing.T) {
	srv := newTestServer(t)
	code := createTestLink(t, srv, "https://example.com/plain")

	cases := []struct {
		name   string
		accept string
		want   int
	}{
		{"browser", "text/html,application/xhtml+xml", http.StatusOK},
		{"explicit json", "application/json", http.StatusFound},
		{"curl default", "*/*", http.StatusFound},
		{"no accept header", "", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest("GET", "/"+code, nil)
			if tc.accept != "" {
				req.Header.Set("Accept", tc.accept)
			}
			rec := httptest.NewRecorder()
			srv.routes().ServeHTTP(rec, req)

			if rec.Code != tc.want {
				t.Fatalf("status %d, want %d", rec.Code, tc.want)
			}
			if tc.want == http.StatusFound {
				if loc := rec.Header().Get("Location"); loc != "https://example.com/plain" {
					t.Fatalf("location = %q", loc)
				}
			}
		})
	}
}

func TestPrefersJSON(t *testing.T) {
	cases := []struct {
		accept string
		want   bool
	}{
		{"", false},
		{"text/html", false},
		{"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", false},
		{"application/json", true},
		{"*/*", true},
		{"application/json, text/html", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest("GET", "/x", nil)
		if tc.accept != "" {
			req.Header.Set("Accept", tc.accept)
		}
		if got := prefersJSON(req); got != tc.want {
			t.Fatalf("prefersJSON(%q) = %v, want %v", tc.accept, got, tc.want)
		}
	}
}

func TestRedirectInvalidCode(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/short", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

func TestAdClickRedirectsAndCounts(t *testing.T) {
	srv := newTestServer(t)
	code := createTestLink(t, srv, "https://example.com/target")
	var linkID float64
	_, payload := doJSON(t, srv, "GET", "/api/v2/links/"+code+"/stats", "")
	linkID = payload["clicks"].(float64) // clicks 0; нужен id — берём из БД ниже

	if err := srv.db.FetchOne(t.Context(),
		"SELECT id FROM links WHERE short_code = ?", code).Scan(&linkID); err != nil {
		t.Fatalf("fetch link id: %v", err)
	}

	req := httptest.NewRequest("GET", fmt.Sprintf("/ad-click/ad1.png?link=%d", int64(linkID)), nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://ads.example/one" {
		t.Fatalf("location = %q", loc)
	}

	if err := srv.adStats.Flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	var clicks int64
	if err := srv.db.FetchOne(t.Context(),
		"SELECT count FROM ad_stats WHERE ad_key = ? AND event_type = ? AND link_id = ?",
		"ad1.png", adEventClick, int64(linkID)).Scan(&clicks); err != nil {
		t.Fatalf("query clicks: %v", err)
	}
	if clicks != 1 {
		t.Fatalf("clicks = %d, want 1", clicks)
	}
}

func TestAdClickUnknownKey(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/ad-click/nope.png", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// Ключ из URL не должен позволять выйти за пределы static/ads.
func TestAdClickRejectsPathTraversal(t *testing.T) {
	srv := newTestServer(t)
	req := httptest.NewRequest("GET", "/ad-click/..%2f..%2fetc%2fpasswd", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
}

// Баннер с недопустимой схемой не должен ни редиректить, ни отдавать 302:
// цель из ad_images.txt попадает в заголовок Location.
func TestAdClickRejectsNonHTTPSchemes(t *testing.T) {
	srv := newTestServer(t)

	adsFile := filepath.Join(t.TempDir(), "ad_images.txt")
	content := "evil.png|javascript:alert(document.cookie)|1\n" +
		"data.png|data:text/html,<script>alert(1)</script>|1\n" +
		"rel.png|//evil.example/x|1\n"
	if err := os.WriteFile(adsFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write ads file: %v", err)
	}
	srv.cfg.AdImagesFile = adsFile

	for _, key := range []string{"evil.png", "data.png", "rel.png"} {
		req := httptest.NewRequest("GET", "/ad-click/"+key, nil)
		rec := httptest.NewRecorder()
		srv.routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", key, rec.Code)
		}
		if loc := rec.Header().Get("Location"); loc != "" {
			t.Fatalf("%s: unexpected Location %q", key, loc)
		}
	}
}

func TestGetAdStats(t *testing.T) {
	srv := newTestServer(t)
	code := createTestLink(t, srv, "https://example.com/target")

	var linkID int64
	if err := srv.db.FetchOne(t.Context(),
		"SELECT id FROM links WHERE short_code = ?", code).Scan(&linkID); err != nil {
		t.Fatalf("fetch link id: %v", err)
	}

	// Два показа и один клик по первому баннеру.
	srv.adStats.Record("ad1.png", adEventImpression, 0)
	srv.adStats.Record("ad1.png", adEventImpression, 0)
	srv.adStats.Record("ad2.jpg", adEventImpression, 0)
	srv.adStats.Record("ad1.png", adEventClick, 0)

	rec, payload := doJSON(t, srv, "GET", "/api/v2/ads/stats", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if got, _ := payload["total_impressions"].(float64); got != 3 {
		t.Fatalf("total_impressions = %v, want 3", payload["total_impressions"])
	}
	if got, _ := payload["total_clicks"].(float64); got != 1 {
		t.Fatalf("total_clicks = %v, want 1", payload["total_clicks"])
	}
	if got, _ := payload["ctr"].(float64); got < 0.333 || got > 0.334 {
		t.Fatalf("ctr = %v, want ~0.3333", payload["ctr"])
	}

	ads, _ := payload["ads"].([]any)
	if len(ads) != 2 {
		t.Fatalf("expected 2 ads in stats, got %d", len(ads))
	}
	first, _ := ads[0].(map[string]any)
	if first["ad_key"] != "ad1.png" {
		t.Fatalf("expected ad1.png first (most impressions), got %v", first["ad_key"])
	}
	_ = linkID
}

func TestGetLinkAdStats(t *testing.T) {
	srv := newTestServer(t)
	code := createTestLink(t, srv, "https://example.com/target")

	var linkID int64
	if err := srv.db.FetchOne(t.Context(),
		"SELECT id FROM links WHERE short_code = ?", code).Scan(&linkID); err != nil {
		t.Fatalf("fetch link id: %v", err)
	}

	srv.adStats.Record("ad1.png", adEventImpression, linkID)
	srv.adStats.Record("ad1.png", adEventImpression, linkID)
	srv.adStats.Record("ad1.png", adEventClick, linkID)

	rec, payload := doJSON(t, srv, "GET", "/api/v2/links/"+code+"/ads", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if payload["short_code"] != code {
		t.Fatalf("wrong short_code: %v", payload["short_code"])
	}
	if got, _ := payload["impressions"].(float64); got != 2 {
		t.Fatalf("impressions = %v, want 2", payload["impressions"])
	}
	if got, _ := payload["clicks"].(float64); got != 1 {
		t.Fatalf("clicks = %v, want 1", payload["clicks"])
	}
}

func TestGetLinkAdStatsUnknownCode(t *testing.T) {
	srv := newTestServer(t)
	rec, _ := doJSON(t, srv, "GET", "/api/v2/links/zzzzzzz/ads", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	rec, _ = doJSON(t, srv, "GET", "/api/v2/links/short/ads", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("invalid code: status %d, want 404", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	srv := newTestServer(t)
	rec, payload := doJSON(t, srv, "GET", "/api/v2/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	if payload["status"] != "ok" {
		t.Fatalf("status = %v", payload["status"])
	}
	if payload["db"] != true {
		t.Fatalf("db = %v, want true", payload["db"])
	}
	if payload["redis"] != true {
		t.Fatalf("redis = %v, want true", payload["redis"])
	}
}

// Счётчики копятся в буфере и должны попадать в БД при Flush.
func TestAdStatsRecorderFlushAccumulates(t *testing.T) {
	srv := newTestServer(t)
	for i := 0; i < 5; i++ {
		srv.adStats.Record("ad1.png", adEventImpression, 0)
	}
	if srv.adStats.Pending() != 5 {
		t.Fatalf("pending = %d, want 5", srv.adStats.Pending())
	}
	if err := srv.adStats.Flush(t.Context()); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if srv.adStats.Pending() != 0 {
		t.Fatalf("pending after flush = %d, want 0", srv.adStats.Pending())
	}

	var count int64
	if err := srv.db.FetchOne(t.Context(),
		"SELECT count FROM ad_stats WHERE ad_key = ? AND event_type = ? AND link_id = 0",
		"ad1.png", adEventImpression).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 5 {
		t.Fatalf("count = %d, want 5", count)
	}

	// Второй flush должен суммироваться, а не перезаписывать.
	srv.adStats.Record("ad1.png", adEventImpression, 0)
	if err := srv.adStats.Flush(t.Context()); err != nil {
		t.Fatalf("second flush: %v", err)
	}
	if err := srv.db.FetchOne(t.Context(),
		"SELECT count FROM ad_stats WHERE ad_key = ? AND event_type = ? AND link_id = 0",
		"ad1.png", adEventImpression).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 6 {
		t.Fatalf("count after second flush = %d, want 6", count)
	}
}

func TestAdStatsRecorderIgnoresEmptyKey(t *testing.T) {
	srv := newTestServer(t)
	srv.adStats.Record("", adEventImpression, 0)
	if srv.adStats.Pending() != 0 {
		t.Fatal("empty ad key must not be recorded")
	}
}

// Run обязан дописать буфер при отмене контекста, иначе хвост показов
// теряется на каждом рестарте контейнера.
func TestAdStatsRecorderRunFlushesOnStop(t *testing.T) {
	srv := newTestServer(t)
	srv.adStats.Record("ad1.png", adEventImpression, 0)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		srv.adStats.Run(ctx)
		close(done)
	}()
	cancel()
	<-done

	var count int64
	if err := srv.db.FetchOne(t.Context(),
		"SELECT count FROM ad_stats WHERE ad_key = ? AND event_type = ? AND link_id = 0",
		"ad1.png", adEventImpression).Scan(&count); err != nil {
		t.Fatalf("query: %v", err)
	}
	if count != 1 {
		t.Fatalf("count = %d, want 1 (final flush missed)", count)
	}
}

func TestRenderRoutingPageEscapes(t *testing.T) {
	// TargetURL попадает в JS-строку шаблона, поэтому html/template
	// должен экранировать кавычки — иначе возможна инъекция.
	srv := newTestServer(t)
	pageFile := filepath.Join(t.TempDir(), "routing.html")
	content := `<a id="ad" href="{{.AdLink}}">{{.TargetURL}}</a><script>var T="{{.TargetURL}}";</script>`
	if err := os.WriteFile(pageFile, []byte(content), 0o644); err != nil {
		t.Fatalf("write template: %v", err)
	}
	srv.cfg.RoutingPageFile = pageFile

	rec := httptest.NewRecorder()
	srv.renderRoutingPage(rec, routingPageData{
		TargetURL: `https://example.com/";alert(1);//`,
		AdImage:   "/ads/x.png",
	})
	body := rec.Body.String()
	if strings.Contains(body, `";alert(1);//`) {
		t.Fatalf("template did not escape target url: %s", body)
	}
	if !strings.Contains(body, "&#34;") {
		t.Fatalf("expected escaped quotes in output: %s", body)
	}
}
