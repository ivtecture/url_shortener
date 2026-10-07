package shortcuts_test

import (
	"regexp"
	"testing"

	"url_shortener/shortcuts"
)

func TestGenerateShortCodeUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 10_000)
	for i := 0; i < 10_000; i++ {
		code := shortcuts.GenerateShortCode()
		if len(code) != shortcuts.CodeLength {
			t.Fatalf("unexpected length: %d", len(code))
		}
		if _, ok := seen[code]; ok {
			t.Fatalf("duplicate code: %s", code)
		}
		seen[code] = struct{}{}
	}
}

func TestGenerateShortCodeAlphabet(t *testing.T) {
	re := regexp.MustCompile(`^[0-9A-Za-z]+$`)
	for i := 0; i < 1_000; i++ {
		code := shortcuts.GenerateShortCode()
		if !re.MatchString(code) {
			t.Fatalf("invalid alphabet in code: %s", code)
		}
	}
}

func TestIsValidShortCode(t *testing.T) {
	if !shortcuts.IsValidShortCode("aB3xK9z") {
		t.Fatal("valid code rejected")
	}
	if shortcuts.IsValidShortCode("") {
		t.Fatal("empty accepted")
	}
	if shortcuts.IsValidShortCode("short") {
		t.Fatal("wrong length accepted")
	}
	if shortcuts.IsValidShortCode("abc-def") {
		t.Fatal("invalid char accepted")
	}
}
