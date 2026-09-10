package shortcuts

import (
	"crypto/rand"
	"strings"
)

// base58: без 0, O, I, l — чтобы избежать визуальных неоднозначностей.
const (
	alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"
	length   = 7
)

const MaxAttempts = 5

var _ shorter = (*base58)(nil)

type base58 struct {
	code string
}

func (b *base58) getShortCode() string {
	if b.code == "" {
		b.code = generate()
	}
	return b.code
}

func (b *base58) isValidCode() bool {
	return isValid(b.code)
}

func generate() string {
	code := make([]byte, length)
	buf := make([]byte, 1)
	for i := range code {
		for {
			if _, err := rand.Read(buf); err != nil {
				return ""
			}
			if int(buf[0]) < 256-(256%len(alphabet)) {
				code[i] = alphabet[int(buf[0])%len(alphabet)]
				break
			}
		}
	}
	return string(code)
}

func isValid(code string) bool {
	if len(code) != length {
		return false
	}
	for i := 0; i < len(code); i++ {
		if !strings.ContainsRune(alphabet, rune(code[i])) {
			return false
		}
	}
	return true
}

func GenerateShortCode() string {
	return (&base58{}).getShortCode()
}

func IsValidShortCode(code string) bool {
	return (&base58{code: code}).isValidCode()
}
