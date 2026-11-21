package main

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"
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

func TestDurationSetting(t *testing.T) {
	const setting = "SUB_POOL_UPSTREAM_READ_TIMEOUT"
	for _, tc := range []struct {
		value string
		want  time.Duration
	}{
		{"", 15 * time.Second},
		{"750ms", 750 * time.Millisecond},
		{"0s", 0},
		{"-1s", 0},
		{"SECRET_SENTINEL", 0},
		{"9223372036854775808s", 0},
	} {
		t.Setenv(setting, tc.value)
		got, err := durationSetting(setting, 15*time.Second)
		if got != tc.want || (err != nil) != (tc.want == 0) {
			t.Fatalf("unexpected duration or error: %v %v", got, err)
		}
		if err != nil && (!strings.Contains(err.Error(), setting) || strings.Contains(err.Error(), "SECRET_SENTINEL")) {
			t.Fatalf("unsafe timeout error: %v", err)
		}
	}
}

func TestServeRejectsInvalidUpstreamTimeouts(t *testing.T) {
	const read = "SUB_POOL_UPSTREAM_READ_TIMEOUT"
	const connect = "SUB_POOL_UPSTREAM_CONNECT_TIMEOUT"
	for _, setting := range []string{read, connect} {
		t.Setenv(read, "15s")
		t.Setenv(connect, "5s")
		t.Setenv(setting, "SECRET_SENTINEL")
		t.Setenv("SUB_POOL_DATABASE_URL", "")
		err := run(context.Background(), []string{"serve"}, strings.NewReader(""), io.Discard)
		if err == nil || !strings.Contains(err.Error(), setting) || strings.Contains(err.Error(), "SECRET_SENTINEL") {
			t.Fatalf("serve did not report the invalid timeout safely: %v", err)
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
