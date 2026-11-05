package auth

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

type Handler struct {
	service    *Service
	origin     string
	secure     bool
	cookieName string
}

func NewHandler(service *Service, origin string, allowLoopbackHTTP bool) (*Handler, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.String() != origin {
		return nil, errors.New("an exact public origin is required")
	}
	secure := u.Scheme == "https"
	ip, _ := netip.ParseAddr(u.Hostname())
	if !secure && !(allowLoopbackHTTP && u.Scheme == "http" && ip.IsLoopback()) {
		return nil, errors.New("HTTPS is required except explicit loopback development")
	}
	name := "__Host-sub_pool_session"
	if !secure {
		name = "sub_pool_dev_session"
	}
	return &Handler{service: service, origin: origin, secure: secure, cookieName: name}, nil
}

func reply(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func failure(w http.ResponseWriter, err error) {
	f := &Failure{Status: 500, Code: "INTERNAL_SERVER_ERROR"}
	var known *Failure
	if errors.As(err, &known) {
		f = known
	}
	if f.RetryAt != nil {
		seconds := max(1, int(time.Until(*f.RetryAt).Seconds())+1)
		w.Header().Set("Retry-After", strconv.Itoa(seconds))
	}
	reply(w, f.Status, map[string]any{
		"defined": true, "code": f.Code, "status": f.Status, "message": "Request could not be completed.",
		"data": map[string]any{"retry_at": f.RetryAt},
	})
}

func (h *Handler) cookie(w http.ResponseWriter, token string, expires time.Time) {
	c := &http.Cookie{Name: h.cookieName, Value: token, Path: "/", HttpOnly: true, Secure: h.secure, SameSite: http.SameSiteLaxMode, Expires: expires}
	if token == "" {
		c.MaxAge = -1
		c.Expires = time.Unix(1, 0).UTC()
	}
	http.SetCookie(w, c)
}

func (h *Handler) token(r *http.Request) string {
	var found string
	count := 0
	for _, line := range r.Header.Values("Cookie") {
		for _, part := range strings.Split(line, ";") {
			name, value, _ := strings.Cut(strings.TrimSpace(part), "=")
			if strings.TrimSpace(name) == h.cookieName {
				found = value
				count++
			}
		}
	}
	if count != 1 {
		return ""
	}
	return found
}

// Go replaces invalid Unicode while decoding JSON; passwords must never be normalized.
func validJSONUnicode(body []byte) bool {
	if !utf8.Valid(body) {
		return false
	}
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		i++
		if i >= len(body) {
			return false
		}
		if body[i] != 'u' {
			continue
		}
		if i+4 >= len(body) {
			return false
		}
		value, err := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if value >= 0xDC00 && value <= 0xDFFF {
			return false
		}
		if value >= 0xD800 && value <= 0xDBFF {
			if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
			if err != nil || low < 0xDC00 || low > 0xDFFF {
				return false
			}
			i += 6
		}
	}
	return true
}

func readInput(w http.ResponseWriter, r *http.Request, names ...string) (map[string]string, error) {
	if len(r.Header.Values("Content-Type")) != 1 || r.Header.Get("Content-Encoding") != "" {
		return nil, ErrInput
	}
	media, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || media != "application/json" || len(params) > 1 || len(params) == 1 && !strings.EqualFold(params["charset"], "utf-8") {
		return nil, ErrInput
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
	if err != nil || !validJSONUnicode(body) {
		return nil, ErrInput
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, ErrInput
	}
	values := make(map[string]string, len(names))
	for decoder.More() {
		key, err := decoder.Token()
		if err != nil {
			return nil, ErrInput
		}
		name, ok := key.(string)
		if !ok {
			return nil, ErrInput
		}
		allowed := false
		for _, candidate := range names {
			allowed = allowed || name == candidate
		}
		if _, duplicate := values[name]; duplicate || !allowed {
			return nil, ErrInput
		}
		value, err := decoder.Token()
		text, ok := value.(string)
		if err != nil || !ok {
			return nil, ErrInput
		}
		values[name] = text
	}
	token, err = decoder.Token()
	if err != nil || token != json.Delim('}') || len(values) != len(names) {
		return nil, ErrInput
	}
	if _, err = decoder.Token(); err != io.EOF {
		return nil, ErrInput
	}
	return values, nil
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.URL.RawQuery != "" || r.URL.ForceQuery {
		failure(w, ErrInput)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/auth/")
	if path == r.URL.Path || path != "login" && path != "session" && path != "renew" && path != "logout" && path != "password" {
		failure(w, &Failure{Status: 404, Code: "NOT_FOUND"})
		return
	}
	method := http.MethodPost
	if path == "session" {
		method = http.MethodGet
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		failure(w, &Failure{Status: 405, Code: "METHOD_NOT_SUPPORTED"})
		return
	}
	var input map[string]string
	if method == http.MethodPost {
		if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.origin {
			failure(w, ErrForbidden)
			return
		}
		var names []string
		switch path {
		case "login":
			names = []string{"username", "password"}
		case "password":
			names = []string{"current_password", "new_password"}
		}
		var err error
		input, err = readInput(w, r, names...)
		if err != nil {
			failure(w, err)
			return
		}
	} else if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		failure(w, ErrInput)
		return
	}
	var result any
	var err error
	token := h.token(r)
	switch path {
	case "login":
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		ip, _ := netip.ParseAddr(host)
		var session Session
		var issued string
		session, issued, err = h.service.Login(r.Context(), input["username"], input["password"], ip)
		if err == nil {
			h.cookie(w, issued, session.AbsoluteExpiresAt)
			result = session
		}
	case "session", "renew":
		result, err = h.service.Current(r.Context(), token, path == "renew")
	case "logout":
		err = h.service.Logout(r.Context(), token)
		if err == nil {
			h.cookie(w, "", time.Time{})
			result = map[string]bool{"logged_out": true}
		}
	case "password":
		err = h.service.ChangePassword(r.Context(), token, input["current_password"], input["new_password"])
		if err == nil {
			h.cookie(w, "", time.Time{})
			result = map[string]bool{"password_changed": true, "sessions_revoked": true}
		}
	}
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			h.cookie(w, "", time.Time{})
		}
		failure(w, err)
		return
	}
	reply(w, 200, result)
}
