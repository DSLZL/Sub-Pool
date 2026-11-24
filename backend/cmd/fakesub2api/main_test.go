package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

type startupURL chan string

func (out startupURL) Write(p []byte) (int, error) {
	out <- strings.TrimSpace(string(p))
	return len(p), nil
}

func TestRunListensOnLoopbackAndStops(t *testing.T) {
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	out := make(startupURL, 1)
	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, nil, out) }()
	var base string
	select {
	case base = <-out:
	case err := <-done:
		t.Fatalf("exited before listen: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("missing listener address")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	ip, err := netip.ParseAddr(u.Hostname())
	if err != nil || !ip.IsLoopback() || u.Port() == "" {
		t.Fatalf("expected a loopback URL with a port: %s", base)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	res, err := client.Get(base + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var health map[string]string
	if err := json.NewDecoder(res.Body).Decode(&health); err != nil || res.StatusCode != 200 || health["status"] != "ok" {
		t.Fatalf("unexpected health: %d %v %v", res.StatusCode, health, err)
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("listener did not stop")
	}
	if res, err := client.Get(base + "/health"); err == nil {
		res.Body.Close()
		t.Fatal("listener still accepted connections")
	}
}

func TestRunRejectsNonLoopback(t *testing.T) {
	if err := run(context.Background(), []string{"-listen", "0.0.0.0:0"}, nil, make(startupURL, 1)); err == nil {
		t.Fatal("non-loopback listener was accepted")
	}
}
