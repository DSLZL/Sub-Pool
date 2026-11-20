package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
)

func (h *Handler) serveUsers(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	action := strings.TrimPrefix(r.URL.Path, "/api/v1/users/")
	names := map[string][]string{
		"list":   {"after", "search", "limit"},
		"get":    {"id"},
		"create": {"username", "password", "enabled", "features", "account_ids", "participant_ids"},
		"update": {"id", "revision", "username", "enabled", "features", "account_ids", "participant_ids", "new_password"},
		"delete": {"id", "revision", "confirmation"},
	}
	method := http.MethodPost
	if r.URL.Path == "/api/v1/access" {
		action, method = "access", http.MethodGet
	} else if _, found := names[action]; !found {
		failure(ctx, w, ErrNotFound)
		return
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		failure(ctx, w, &Failure{Status: 405, Code: "METHOD_NOT_SUPPORTED"})
		return
	}
	var input struct {
		UserUpdate
		Password     string `json:"password"`
		After        string `json:"after"`
		Search       string `json:"search"`
		Limit        int    `json:"limit"`
		Confirmation string `json:"confirmation"`
	}
	if method == http.MethodPost {
		if len(r.Header.Values("Origin")) != 1 || r.Header.Get("Origin") != h.origin {
			failure(ctx, w, ErrForbidden)
			return
		}
		fields, err := readFields(w, r, 65536, names[action]...)
		if err != nil {
			failure(ctx, w, err)
			return
		}
		body, err := json.Marshal(fields)
		if err != nil || json.Unmarshal(body, &input) != nil {
			failure(ctx, w, ErrInput)
			return
		}
	} else if r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		failure(ctx, w, ErrInput)
		return
	}
	token := h.token(r)
	var result any
	var err error
	switch action {
	case "access":
		result, err = h.service.CurrentAccess(ctx, token)
	case "list":
		result, err = h.service.ListUsers(ctx, token, input.After, input.Search, input.Limit)
	case "get":
		result, err = h.service.GetUser(ctx, token, input.ID)
	case "create":
		result, err = h.service.CreateUser(ctx, token, UserCreate{Username: input.Username, Password: input.Password, Enabled: input.Enabled, Grants: input.Grants})
	case "update":
		result, err = h.service.UpdateUser(ctx, token, input.UserUpdate)
	case "delete":
		err = h.service.DeleteUser(ctx, token, input.ID, input.Revision, input.Confirmation)
		result = map[string]bool{"deleted": true}
	}
	if err != nil {
		if errors.Is(err, ErrUnauthorized) {
			h.cookie(w, "", time.Time{})
		}
		failure(ctx, w, err)
		return
	}
	reply(w, http.StatusOK, result)
}
