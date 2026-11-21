package connection

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"net/url"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUpstreamReadRetries(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		recover bool
		count   int
		reason  string
	}{
		{"502 then success", 502, true, 2, "VERIFIED_READ"},
		{"persistent 502", 502, false, 3, "UPSTREAM_ERROR"},
		{"unauthorized", 401, false, 1, "UNAUTHORIZED"},
		{"invalid envelope", 200, false, 1, "INVALID_RESPONSE"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var arrivals []time.Time
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				arrivals = append(arrivals, time.Now())
				attempt := len(arrivals)
				mu.Unlock()
				if tc.recover && attempt == 2 {
					respond(w, map[string]string{"version": "fixture"})
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, `{"message":"fixture failure"}`)
			}))
			defer server.Close()
			u := newUpstreamClient(DefaultUpstreamReadTimeout, DefaultUpstreamConnectTimeout)
			_, check := u.get(context.Background(), server.URL, "/version", "api_key", "fixture", "version.read", true)
			mu.Lock()
			defer mu.Unlock()
			if check.Reason != tc.reason || len(arrivals) != tc.count {
				t.Fatalf("read attempts=%d check=%+v", len(arrivals), check)
			}
			for i := 1; i < len(arrivals); i++ {
				want := time.Duration(i) * 250 * time.Millisecond
				if delay := arrivals[i].Sub(arrivals[i-1]); delay < want {
					t.Fatalf("retry %d waited %v, want at least %v", i, delay, want)
				}
			}
		})
	}
}

func closeWithoutResponse(t *testing.T, w http.ResponseWriter) {
	t.Helper()
	conn, _, err := w.(http.Hijacker).Hijack()
	if err != nil {
		t.Error(err)
		return
	}
	_ = conn.Close()
}

func TestUpstreamReadRecoversFromNetworkError(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			closeWithoutResponse(t, w)
			return
		}
		respond(w, map[string]string{"version": "fixture"})
	}))
	defer server.Close()
	u := newUpstreamClient(DefaultUpstreamReadTimeout, DefaultUpstreamConnectTimeout)
	_, check := u.get(context.Background(), server.URL, "/version", "api_key", "fixture", "version.read", true)
	if check.State != "available" || calls.Load() != 2 {
		t.Fatalf("read attempts=%d check=%+v", calls.Load(), check)
	}
}

func TestUpstreamRetryNetworkClassification(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{io.EOF, true},
		{&net.DNSError{IsTimeout: true}, true},
		{&net.DNSError{IsNotFound: true}, false},
		{&net.OpError{Op: "dial", Err: &net.AddrError{Err: "invalid address"}}, false},
		{&tls.CertificateVerificationError{Err: errors.New("invalid certificate")}, false},
		{context.Canceled, false},
		{errRedirect, false},
	} {
		err := &url.Error{Op: "Get", URL: "http://fixture.invalid", Err: tc.err}
		if got := temporaryUpstreamError(err); got != tc.want {
			t.Errorf("%T retry=%v, want %v", tc.err, got, tc.want)
		}
	}
}

func TestUpstreamReadHonorsRetryAfter(t *testing.T) {
	for _, tc := range []struct {
		name string
		date bool
	}{{"seconds", false}, {"HTTP-date", true}} {
		t.Run(tc.name, func(t *testing.T) {
			var calls atomic.Int32
			allowed, second := make(chan time.Time, 1), make(chan time.Time, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch calls.Add(1) {
				case 1:
					until, header := time.Now().Add(time.Second), "1"
					if tc.date {
						until = time.Now().UTC().Truncate(time.Second).Add(2 * time.Second)
						header = until.Format(http.TimeFormat)
					}
					allowed <- until
					w.Header().Set("Retry-After", header)
					w.WriteHeader(http.StatusTooManyRequests)
				case 2:
					second <- time.Now()
					respond(w, map[string]string{"version": "fixture"})
				default:
					t.Error("unexpected third request")
					w.WriteHeader(http.StatusForbidden)
				}
			}))
			defer server.Close()
			u := newUpstreamClient(DefaultUpstreamReadTimeout, DefaultUpstreamConnectTimeout)
			_, check := u.get(context.Background(), server.URL, "/version", "api_key", "fixture", "version.read", true)
			if check.State != "available" || calls.Load() != 2 {
				t.Fatalf("read attempts=%d check=%+v", calls.Load(), check)
			}
			if got, until := <-second, <-allowed; got.Before(until) {
				t.Errorf("retry sent at %v, before %v", got, until)
			}
		})
	}
}

func TestUpstreamReadRetainsRateLimitBeyondCallerDeadline(t *testing.T) {
	for _, tc := range []struct {
		header string
		delay  time.Duration
	}{
		{"7", 7 * time.Second},
		{"invalid", 5 * time.Minute},
		{"999999999999999999999999", 366 * 24 * time.Hour},
	} {
		t.Run(tc.header, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Retry-After", tc.header)
				w.WriteHeader(http.StatusTooManyRequests)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			u := newUpstreamClient(DefaultUpstreamReadTimeout, DefaultUpstreamConnectTimeout)
			start := time.Now()
			_, check := u.get(ctx, server.URL, "/version", "api_key", "fixture", "version.read", true)
			if calls.Load() != 1 || ctx.Err() != nil || check.Reason != "RATE_LIMITED" ||
				check.retryAfter == nil || check.retryAfter.Before(start.Add(tc.delay)) {
				t.Fatalf("read attempts=%d context=%v check=%+v", calls.Load(), ctx.Err(), check)
			}
		})
	}
}

func TestUpstreamReadBudgetIncludesBackoff(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	const connectTimeout = 450 * time.Millisecond
	s, err := NewWithUpstreamTimeouts(nil, nil, nil, true, 700*time.Millisecond, connectTimeout)
	if err != nil {
		t.Fatal(err)
	}
	if got := s.upstream.http.Transport.(*http.Transport).TLSHandshakeTimeout; got != connectTimeout {
		t.Fatalf("TLS handshake limit=%v, want %v", got, connectTimeout)
	}
	_, check := s.upstream.get(context.Background(), server.URL, "/version", "api_key", "fixture", "version.read", true)
	if calls.Load() != 2 || check.Reason != "UPSTREAM_ERROR" {
		t.Fatalf("read attempts=%d check=%+v", calls.Load(), check)
	}
}

func TestUpstreamReadWaitCancels(t *testing.T) {
	var calls atomic.Int32
	received := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "4")
		w.WriteHeader(http.StatusTooManyRequests)
		w.(http.Flusher).Flush()
		select {
		case received <- struct{}{}:
		default:
		}
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := newUpstreamClient(DefaultUpstreamReadTimeout, DefaultUpstreamConnectTimeout)
	done := make(chan Check, 1)
	go func() {
		_, check := u.get(ctx, server.URL, "/version", "api_key", "fixture", "version.read", true)
		done <- check
	}()
	select {
	case <-received:
	case <-time.After(3 * time.Second):
		t.Fatal("initial request did not reach the server")
	}
	timer := time.AfterFunc(250*time.Millisecond, cancel)
	defer timer.Stop()
	select {
	case check := <-done:
		if check.Reason != "RATE_LIMITED" || check.retryAfter == nil || ctx.Err() != context.Canceled || calls.Load() != 1 {
			t.Fatalf("read attempts=%d context=%v check=%+v", calls.Load(), ctx.Err(), check)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("read wait did not stop after cancellation")
	}
}

func TestUpstreamWritesStaySingleDispatch(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		status int
		key    string
		reason string
	}{
		{"502 write", http.MethodPost, 502, "", "UPSTREAM_ERROR"},
		{"429 write", http.MethodPut, 429, "", "RATE_LIMITED"},
		{"keyed write lost response", http.MethodPost, 0, "fixture-write", "NETWORK_ERROR"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const payload = `{"balance":1}`
			var writes atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodGet {
					respond(w, map[string]string{"status": "ok"})
					return
				}
				writes.Add(1)
				body, err := io.ReadAll(r.Body)
				if err != nil || string(body) != payload || r.Method != tc.method || r.Header.Get("Idempotency-Key") != tc.key {
					t.Error("unexpected write method, body or idempotency key")
				}
				if tc.status == 0 {
					closeWithoutResponse(t, w)
					return
				}
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(tc.status)
			}))
			defer server.Close()
			u := newUpstreamClient(DefaultUpstreamReadTimeout, DefaultUpstreamConnectTimeout)
			if _, check := u.get(context.Background(), server.URL, "/warm", "", "", "health", true); check.State != "available" {
				t.Fatalf("could not establish a reusable connection: %+v", check)
			}
			var reused atomic.Bool
			ctx := httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{
				GotConn: func(info httptrace.GotConnInfo) {
					if info.Reused {
						reused.Store(true)
					}
				},
			})
			_, check := u.requestWithKey(ctx, tc.method, server.URL, "/write", "api_key", "fixture", "wallet.write", []byte(payload), true, tc.key)
			if !reused.Load() || writes.Load() != 1 || check.Reason != tc.reason {
				t.Fatalf("reused=%v writes=%d check=%+v", reused.Load(), writes.Load(), check)
			}
		})
	}
}
