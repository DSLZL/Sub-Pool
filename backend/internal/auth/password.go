package auth

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"regexp"
	"strings"
	"unicode/utf8"
)

const passwordIterations = 600_000

var usernamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,63}$`)

func NormalizeUsername(value string) (string, error) {
	if len(value) > 256 {
		return "", ErrInput
	}
	value = strings.TrimSpace(value)
	value = strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
	if !usernamePattern.MatchString(value) {
		return "", ErrInput
	}
	return value, nil
}

func ValidPassword(value string) bool {
	if !utf8.ValidString(value) || len(value) > 128 || utf8.RuneCountInString(value) < 10 {
		return false
	}
	letter, digit := false, false
	for _, r := range value {
		letter = letter || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		digit = digit || r >= '0' && r <= '9'
	}
	return letter && digit
}

func hashPassword(value string) (string, error) {
	if !ValidPassword(value) {
		return "", ErrInput
	}
	return derivePasswordHash(value)
}

func derivePasswordHash(value string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := pbkdf2.Key(sha256.New, value, salt, passwordIterations, 32)
	if err != nil {
		return "", err
	}
	return "pbkdf2-sha256-v1$600000$" + base64.RawStdEncoding.EncodeToString(salt) + "$" + base64.RawStdEncoding.EncodeToString(key), nil
}

func verifyPassword(value, stored string) bool {
	if len(stored) > 160 {
		stored = ""
	}
	parts := strings.SplitN(stored, "$", 5)
	salt := make([]byte, 16)
	expected := make([]byte, 32)
	valid := len(parts) == 4 && parts[0] == "pbkdf2-sha256-v1" && parts[1] == "600000"
	if valid {
		s, err1 := base64.RawStdEncoding.Strict().DecodeString(parts[2])
		k, err2 := base64.RawStdEncoding.Strict().DecodeString(parts[3])
		valid = err1 == nil && err2 == nil && len(s) == 16 && len(k) == 32
		if valid {
			copy(salt, s)
			copy(expected, k)
		}
	}
	// Missing, disabled and malformed stored credentials still perform the same KDF.
	key, err := pbkdf2.Key(sha256.New, value, salt, passwordIterations, 32)
	return err == nil && subtle.ConstantTimeCompare(key, expected) == 1 && valid
}

func randomValue(size int) (string, error) {
	value := make([]byte, size)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func tokenDigest(token string) ([]byte, error) {
	if len(token) != 43 {
		return nil, ErrUnauthorized
	}
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return nil, ErrUnauthorized
	}
	digest := sha256.Sum256([]byte(token))
	return digest[:], nil
}
