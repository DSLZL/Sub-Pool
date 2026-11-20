package telemetry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

const RequestIDHeader = "X-Request-ID"

type traceKey struct{}
type requestTrace struct {
	id    string
	actor atomic.Pointer[string]
}

func SetActor(ctx context.Context, actor string) {
	if trace, ok := ctx.Value(traceKey{}).(*requestTrace); ok && actor != "" {
		trace.actor.CompareAndSwap(nil, &actor)
	}
}

type responseStatus struct {
	http.ResponseWriter
	status int
}

func (w *responseStatus) WriteHeader(code int) {
	if w.status == 0 && code >= 200 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseStatus) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(body)
}

func (w *responseStatus) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func Requests(mux *http.ServeMux, routes map[string]struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			Error(r.Context(), "request_id", err)
			http.Error(w, "Request unavailable.", http.StatusServiceUnavailable)
			return
		}
		trace := &requestTrace{id: hex.EncodeToString(random[:])}
		ctx := context.WithValue(r.Context(), traceKey{}, trace)
		w.Header().Set(RequestIDHeader, trace.id)
		_, route := mux.Handler(r)
		// ServeMux can report a synthesized request path for a redirect.
		if _, registered := routes[route]; !registered {
			route = ""
		}
		method := "OTHER"
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut, http.MethodPatch,
			http.MethodDelete, http.MethodOptions, http.MethodConnect, http.MethodTrace:
			method = r.Method
		}
		response := &responseStatus{ResponseWriter: w}
		defer func() {
			if p := recover(); p != nil {
				if p != http.ErrAbortHandler {
					Log(ctx, slog.LevelError, "request panicked", slog.String("route", route))
				}
				if response.status != 0 || p == http.ErrAbortHandler {
					panic(http.ErrAbortHandler)
				}
				http.Error(response, "Request failed; outcome unknown.", http.StatusInternalServerError)
			}
			if response.status == 0 {
				response.status = http.StatusOK
			}
			Log(ctx, slog.LevelInfo, "http request", slog.String("method", method), slog.String("route", route),
				slog.Int("status", response.status), slog.Int64("duration_ms", time.Since(start).Milliseconds()))
		}()
		next.ServeHTTP(response, r.WithContext(ctx))
	})
}
