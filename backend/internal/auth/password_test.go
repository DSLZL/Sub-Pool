package auth

import (
	"strings"
	"testing"
)

func TestPasswordPolicyAndVersionedHash(t *testing.T) {
	for _, value := range []string{"", "short123", "abcdefghijk", "12345678901", strings.Repeat("a", 128) + "1", "中文中文中文a1", string([]byte{0xff}) + "letters12345"} {
		if ValidPassword(value) {
			t.Fatal("accepted invalid password policy")
		}
	}
	password := "  Letters12345  "
	hash, err := hashPassword(password)
	if err != nil {
		t.Fatal(err)
	}
	other, err := hashPassword(password)
	if err != nil || other == hash {
		t.Fatal("passwords did not receive unique salts")
	}
	if !verifyPassword(password, hash) || verifyPassword(strings.TrimSpace(password), hash) {
		t.Fatal("password bytes were normalized")
	}
	for _, value := range []string{"", "pbkdf2-sha256-v1$999999999999999999$bad$bad", strings.Replace(hash, "$600000$", "$1$", 1), hash + "$extra", strings.Replace(hash, "pbkdf2-sha256-v1", "unknown", 1)} {
		if verifyPassword(password, value) {
			t.Fatal("accepted malformed password hash")
		}
	}
	if _, err := hashPassword("weak"); err == nil {
		t.Fatal("hashed weak new password")
	}
}

func TestUsernameAndTokenCanonicalization(t *testing.T) {
	name, err := NormalizeUsername("  Admin.One_2-3  ")
	if err != nil || name != "admin.one_2-3" {
		t.Fatal("username normalization changed")
	}
	for _, value := range []string{"ab", "_admin", "äDMIN", "adm in", strings.Repeat("a", 65), "ADMIN\x00"} {
		if _, err := NormalizeUsername(value); err == nil {
			t.Fatal("accepted invalid username")
		}
	}
	token, err := randomValue(32)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := tokenDigest(token)
	if err != nil || len(digest) != 32 {
		t.Fatal("invalid generated session")
	}
	for _, invalid := range []string{"", token + "=", token[:42] + "/", strings.Repeat("_", 43)} {
		if _, err := tokenDigest(invalid); err == nil {
			t.Fatal("accepted invalid token encoding")
		}
	}
}
