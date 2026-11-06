package connection

import (
	"bytes"
	"encoding/json"
	"time"
)

type NativeUser struct {
	UpstreamID string    `json:"upstream_id"`
	Email      string    `json:"email"`
	Username   string    `json:"username"`
	Role       string    `json:"role"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
}

func projectNativeUser(body json.RawMessage, expected, secret string) (NativeUser, bool) {
	fields, ok := usageObject(body)
	if !ok {
		return NativeUser{}, false
	}
	id, ok := usageID(fields["id"], true)
	if !ok || expected != "" && *id != expected {
		return NativeUser{}, false
	}
	user := NativeUser{UpstreamID: *id}
	for name, dest := range map[string]*string{"email": &user.Email, "username": &user.Username, "role": &user.Role, "status": &user.Status} {
		if decode(fields[name], dest) != nil || *dest != "" && !safeText(*dest, 512, secret) {
			return NativeUser{}, false
		}
	}
	if user.Role != "admin" && user.Role != "user" || user.Status != "active" && user.Status != "disabled" {
		return NativeUser{}, false
	}
	created, ok := snapshotTime(fields["created_at"])
	if !ok || created == nil {
		return NativeUser{}, false
	}
	user.CreatedAt = *created
	if raw := fields["deleted_at"]; len(raw) != 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return NativeUser{}, false
	}
	return user, true
}
