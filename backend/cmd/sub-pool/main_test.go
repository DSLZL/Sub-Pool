package main

import (
	"context"
	"io"
	"strings"
	"testing"
)

func TestCLIRejectsImplicitModeAndPasswordArguments(t *testing.T) {
	for _, args := range [][]string{nil, {"unknown"}, {"serve", "extra"}, {"migrate", "extra"}, {"bootstrap"},
		{"bootstrap", "admin", "SecretPassword123"}, {"reset-password"}, {"reset-password", "admin", "SecretPassword123"}} {
		if err := run(context.Background(), args, strings.NewReader(""), io.Discard); err == nil || strings.Contains(err.Error(), "SecretPassword123") {
			t.Fatal("CLI accepted implicit mode or exposed password argument")
		}
	}
}

func TestCLIRequiresExplicitDatabaseAndRedactsConfiguration(t *testing.T) {
	for _, dsn := range []string{"", "postgres://user:SECRET_SENTINEL@%zz/db"} {
		t.Setenv("SUB_POOL_DATABASE_URL", dsn)
		err := run(context.Background(), []string{"migrate"}, strings.NewReader(""), io.Discard)
		if err == nil || strings.Contains(err.Error(), "SECRET_SENTINEL") {
			t.Fatal("database defaults or credential-bearing error")
		}
	}
}

func TestCLIStdinIsBoundedAndDoesNotSilentlyAcceptBOM(t *testing.T) {
	for _, value := range []string{"  Password12345  ", "  Password12345  \n", "  Password12345  \r\n"} {
		password, err := readStdinPassword(strings.NewReader(value))
		if err != nil || password != "  Password12345  " {
			t.Fatal("stdin normalized significant password bytes")
		}
	}
	for _, value := range []string{"\ufeffPassword12345", strings.Repeat("x", 129) + "1", "weak", string([]byte{0xff}) + "Password12345"} {
		if _, err := readStdinPassword(strings.NewReader(value)); err == nil {
			t.Fatal("accepted invalid or BOM-bearing stdin password")
		}
	}
}
