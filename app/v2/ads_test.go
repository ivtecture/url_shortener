package main

import (
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestParseAdLine(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantImage string
		wantLink  string
		wantKey   string
		wantWeigh int
	}{
		{"image and link", "adDv.png|https://sibsiu.ru", "adDv.png", "https://sibsiu.ru", "adDv.png", 1},
		{"with weight", "openai.jpg|https://openai.com|5", "openai.jpg", "https://openai.com", "openai.jpg", 5},
		{"spaces trimmed", "  cpp.jpg | https://example.com | 3 ", "cpp.jpg", "https://example.com", "cpp.jpg", 3},
		{"image only", "adDv.png", "adDv.png", "", "adDv.png", 1},
		{"invalid weight falls back to 1", "cpp.jpg|https://example.com|zero", "cpp.jpg", "https://example.com", "cpp.jpg", 1},
		{"zero weight falls back to 1", "cpp.jpg|https://example.com|0", "cpp.jpg", "https://example.com", "cpp.jpg", 1},
		{"negative weight falls back to 1", "cpp.jpg|https://example.com|-2", "cpp.jpg", "https://example.com", "cpp.jpg", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseAdLine(tc.line)
			if got.Image != tc.wantImage || got.Link != tc.wantLink ||
				got.Key != tc.wantKey || got.Weight != tc.wantWeigh {
				t.Fatalf("parseAdLine(%q) = %+v", tc.line, got)
			}
		})
	}
}

func TestLoadAdEntriesSkipsComments(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "ad_images.txt")
	content := "# комментарий\n\nad1.png|https://a.example|2\nad2.jpg|https://b.example\n"
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	entries := loadAdEntries(Config{AdImagesFile: file})
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Key != "ad1.png" || entries[0].Weight != 2 {
		t.Fatalf("unexpected first entry: %+v", entries[0])
	}
	if entries[1].Key != "ad2.jpg" || entries[1].Weight != 1 {
		t.Fatalf("unexpected second entry: %+v", entries[1])
	}
}

func TestLoadAdEntriesFallbackOnMissingFile(t *testing.T) {
	cfg := Config{
		AdImagesFile: filepath.Join(t.TempDir(), "missing.txt"),
		AdsFiles:     []string{"fallback1.svg", "fallback2.svg"},
	}
	entries := loadAdEntries(cfg)
	if len(entries) != 2 {
		t.Fatalf("expected 2 fallback entries, got %d", len(entries))
	}
	if entries[0].Image != "fallback1.svg" || entries[0].Weight != 1 {
		t.Fatalf("unexpected fallback entry: %+v", entries[0])
	}
}

// pickAd выбирает пропорционально весу: баннер с весом 3 должен выигрывать
// у баннера с весом 1 примерно втрое, иначе частотностью не управлять.
func TestPickAdRespectsWeights(t *testing.T) {
	entries := []adEntry{
		{Image: "heavy.png", Key: "heavy.png", Weight: 3},
		{Image: "light.png", Key: "light.png", Weight: 1},
	}

	counts := map[string]int{}
	const rounds = 4000
	for i := 0; i < rounds; i++ {
		entry, ok := pickAd(entries)
		if !ok {
			t.Fatal("pickAd returned not ok")
		}
		counts[entry.Key]++
	}

	if counts["heavy.png"] <= counts["light.png"] {
		t.Fatalf("weighted pick broken: heavy=%d light=%d", counts["heavy.png"], counts["light.png"])
	}
	ratio := float64(counts["heavy.png"]) / float64(counts["light.png"])
	if math.Abs(ratio-3) > 0.4 {
		t.Fatalf("ratio heavy/light = %.2f, want ~3", ratio)
	}
}

func TestPickAdEqualWeights(t *testing.T) {
	entries := []adEntry{
		{Key: "a.png", Weight: 1},
		{Key: "b.png", Weight: 1},
	}
	counts := map[string]int{}
	for i := 0; i < 4000; i++ {
		entry, _ := pickAd(entries)
		counts[entry.Key]++
	}
	// При равных весах оба баннера должны быть представлены.
	if counts["a.png"] == 0 || counts["b.png"] == 0 {
		t.Fatalf("equal weights should use all banners: %v", counts)
	}
}

func TestPickAdEmpty(t *testing.T) {
	if _, ok := pickAd(nil); ok {
		t.Fatal("expected not ok for empty entries")
	}
	if _, ok := pickAd([]adEntry{{Key: "x", Weight: 1}, {Key: "y"}}); !ok {
		t.Fatal("expected ok when at least one entry has positive weight")
	}
}

func TestFindAdByKey(t *testing.T) {
	entries := []adEntry{
		{Key: "a.png", Link: "https://a.example"},
		{Key: "b.png", Link: ""},
	}
	if entry, ok := findAdByKey(entries, "a.png"); !ok || entry.Link != "https://a.example" {
		t.Fatalf("findAdByKey(a.png) = %+v, %v", entry, ok)
	}
	if _, ok := findAdByKey(entries, "missing.png"); ok {
		t.Fatal("expected not found for unknown key")
	}
	if entry, ok := findAdByKey(entries, "b.png"); !ok || entry.Link != "" {
		t.Fatalf("expected entry without link to be found: %+v", entry)
	}
}

func TestAdClickURL(t *testing.T) {
	if got := adClickURL("", 42); got != "" {
		t.Fatalf("empty key must produce empty url, got %q", got)
	}
	got := adClickURL("adDv.png", 42)
	want := "/ad-click/adDv.png?link=42"
	if got != want {
		t.Fatalf("adClickURL = %q, want %q", got, want)
	}
}

func TestValidAdLink(t *testing.T) {
	cases := []struct {
		name string
		link string
		want bool
	}{
		{"https", "https://sibsiu.ru", true},
		{"http", "http://sibsiu.ru/campaign", true},
		{"uppercase scheme", "HTTPS://sibsiu.ru", true},
		{"with spaces around", "  https://sibsiu.ru  ", true},
		{"empty", "", false},
		{"blank", "   ", false},
		{"javascript", "javascript:alert(1)", false},
		{"data", "data:text/html,<script>alert(1)</script>", false},
		{"scheme relative", "//evil.example", false},
		{"no scheme", "sibsiu.ru", false},
		{"no host", "https://", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validAdLink(tc.link); got != tc.want {
				t.Fatalf("validAdLink(%q) = %v, want %v", tc.link, got, tc.want)
			}
		})
	}
}

func TestCtr(t *testing.T) {
	cases := []struct {
		clicks, impressions int64
		want                float64
	}{
		{0, 0, 0},
		{0, 10, 0},
		{5, 10, 0.5},
		{1, 3, 1.0 / 3.0},
	}
	for _, tc := range cases {
		if got := ctr(tc.clicks, tc.impressions); math.Abs(got-tc.want) > 1e-9 {
			t.Fatalf("ctr(%d, %d) = %f, want %f", tc.clicks, tc.impressions, got, tc.want)
		}
	}
}
