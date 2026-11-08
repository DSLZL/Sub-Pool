package backups

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func cloneSnapshot(t *testing.T, source snapshot) snapshot {
	t.Helper()
	body, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var out snapshot
	if err = json.Unmarshal(body, &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func changeRow(t *testing.T, data *snapshot, table, key string, value json.RawMessage) {
	t.Helper()
	for i := range data.Tables {
		if data.Tables[i].Name == table {
			var row map[string]json.RawMessage
			if json.Unmarshal(data.Tables[i].Rows[0], &row) != nil {
				t.Fatal("fixture row unavailable")
			}
			row[key] = value
			data.Tables[i].Rows[0], _ = json.Marshal(row)
			return
		}
	}
	t.Fatal("fixture table unavailable", table)
}

func TestUploadedArchiveTrustBoundariesAndNativeConstraints(t *testing.T) {
	f := setup(t)
	savedConnection(t, f)
	file := export(t, f, true)
	source, err := f.s.unprotect(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		edit func(*snapshot)
		want error
	}{
		{"future format", func(d *snapshot) { d.Format = "future" }, ErrSchema},
		{"migration mismatch", func(d *snapshot) {
			for k := range d.Migrations {
				d.Migrations[k] = strings.Repeat("a", 64)
				break
			}
		}, ErrSchema},
		{"runtime inclusion", func(d *snapshot) { d.Excluded = d.Excluded[1:] }, ErrSchema},
		{"missing table", func(d *snapshot) { d.Tables = d.Tables[1:] }, ErrSchema},
		{"duplicate table", func(d *snapshot) { d.Tables[1] = d.Tables[0] }, ErrSchema},
		{"unknown column", func(d *snapshot) { d.Tables[0].Columns = append(d.Tables[0].Columns, "unexpected") }, ErrSchema},
		{"missing singleton", func(d *snapshot) {
			for i := range d.Tables {
				if d.Tables[i].Name == "monitoring_config" {
					d.Tables[i].Rows = []json.RawMessage{}
				}
			}
		}, ErrInvalid},
		{"missing row field", func(d *snapshot) {
			for i := range d.Tables {
				if d.Tables[i].Name == "auth_users" {
					d.Tables[i].Rows[0] = json.RawMessage(`{"id":"missing"}`)
				}
			}
		}, ErrInvalid},
		{"duplicate row field", func(d *snapshot) {
			for i := range d.Tables {
				if d.Tables[i].Name == "auth_users" {
					row := d.Tables[i].Rows[0]
					d.Tables[i].Rows[0] = append(json.RawMessage(`{"id":"duplicate",`), row[1:]...)
				}
			}
		}, ErrInvalid},
		{"native unique", func(d *snapshot) {
			for i := range d.Tables {
				if d.Tables[i].Name == "auth_users" {
					d.Tables[i].Rows = append(d.Tables[i].Rows, d.Tables[i].Rows[0])
				}
			}
		}, ErrInvalid},
		{"native check", func(d *snapshot) { changeRow(t, d, "auth_users", "role", json.RawMessage(`"owner"`)) }, ErrInvalid},
		{"native foreign key", func(d *snapshot) { changeRow(t, d, "auth_events", "user_id", json.RawMessage(`"`+id(t)+`"`)) }, ErrInvalid},
		{"no recoverable administrator", func(d *snapshot) { changeRow(t, d, "auth_users", "enabled", json.RawMessage(`false`)) }, ErrInvalid},
		{"sequence below imported identity", func(d *snapshot) {
			for i := range d.Sequences {
				if d.Sequences[i].Table == "auth_events" {
					d.Sequences[i].Next = "1"
				}
			}
		}, ErrInvalid},
		{"false secret policy", func(d *snapshot) { d.IncludeSecrets = false }, ErrInvalid},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			data := cloneSnapshot(t, source)
			test.edit(&data)
			content, e := f.s.protect(data)
			if e != nil {
				t.Fatal(e)
			}
			if _, e = f.s.Preflight(context.Background(), f.token, id(t), content); !errors.Is(e, test.want) {
				t.Fatal("unsafe archive was not rejected", e)
			}
		})
	}
	t.Run("damaged or incomplete ciphertext", func(t *testing.T) {
		changed := "A"
		if file.Content[len(fileHeader)] == 'A' {
			changed = "B"
		}
		bad := []string{"other\n" + file.Content, file.Content[:len(file.Content)-8], strings.Replace(file.Content, "\n", "\r\n", 1), file.Content + " ", file.Content[:len(fileHeader)] + changed + file.Content[len(fileHeader)+1:]}
		for _, content := range bad {
			if _, e := f.s.Preflight(context.Background(), f.token, id(t), content); !errors.Is(e, ErrInvalid) {
				t.Fatal("corrupt material accepted", e)
			}
		}
	})
	t.Run("wrong or missing key", func(t *testing.T) {
		wrong := *f.s
		wrong.aead, _ = newAEAD(bytes.Repeat([]byte{7}, 32))
		if _, e := wrong.Preflight(context.Background(), f.token, id(t), file.Content); !errors.Is(e, ErrInvalid) {
			t.Fatal("wrong key accepted", e)
		}
		wrong.aead = nil
		if _, e := wrong.Export(context.Background(), f.token, id(t), true); !errors.Is(e, ErrKey) {
			t.Fatal("unprotected export allowed", e)
		}
	})
	t.Run("bounded file and plaintext", func(t *testing.T) {
		if _, e := f.s.Preflight(context.Background(), f.token, id(t), strings.Repeat("x", maxFile+1)); !errors.Is(e, ErrLimit) {
			t.Fatal("oversize file accepted", e)
		}
		data := cloneSnapshot(t, source)
		data.Tables[0].Rows = []json.RawMessage{json.RawMessage(`{"oversize":"` + strings.Repeat("x", maxPlaintext) + `"}`)}
		if _, e := f.s.protect(data); !errors.Is(e, ErrLimit) {
			t.Fatal("snapshot was truncated or limit ignored", e)
		}
		data = cloneSnapshot(t, source)
		data.Tables[0].Rows = make([]json.RawMessage, maxTableRows+1)
		content, e := f.s.protect(data)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.s.Preflight(context.Background(), f.token, id(t), content); !errors.Is(e, ErrLimit) {
			t.Fatal("row limit ignored", e)
		}
	})
	var plans int
	if err = f.pool.QueryRow(context.Background(), `SELECT count(*) FROM restore_plans`).Scan(&plans); err != nil || plans != 0 {
		t.Fatal("invalid material created a restore intent", plans, err)
	}
	if _, err = f.identity.Current(context.Background(), f.token, false); err != nil {
		t.Fatal("invalid material changed current identities", err)
	}
}

func TestPhysicalSchemaAndSecretOmissionAreReal(t *testing.T) {
	f := setup(t)
	savedConnection(t, f)
	exec(t, f.pool, `UPDATE notification_config SET secret=decode('010203','hex')`)
	file := export(t, f, false)
	data, err := f.s.unprotect(file.Content)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range data.Tables {
		if table.Name == "connection_config" || table.Name == "notification_config" {
			var row map[string]json.RawMessage
			if json.Unmarshal(table.Rows[0], &row) != nil || string(row["secret"]) != "null" {
				t.Fatal("secret was included against policy")
			}
			if table.Name == "connection_config" && string(row["credential_kind"]) != "null" {
				t.Fatal("incomplete secret omission")
			}
		}
	}
	p := preflight(t, f, file)
	before, err := f.s.GetFile(context.Background(), f.token, *p.BeforeArchiveID)
	if err != nil {
		t.Fatal(err)
	}
	beforeData, err := f.s.unprotect(before.Content)
	if err != nil || !beforeData.IncludeSecrets {
		t.Fatal("before-image discarded current secrets", err)
	}
	exec(t, f.pool, `ALTER TABLE system_settings ADD COLUMN unexpected text`)
	if _, err = f.s.Preflight(context.Background(), f.token, id(t), file.Content); !errors.Is(err, ErrSchema) {
		t.Fatal("changed physical schema accepted", err)
	}
	exec(t, f.pool, `ALTER TABLE system_settings DROP COLUMN unexpected`)
	exec(t, f.pool, `CREATE TABLE unknown_business(id bigint PRIMARY KEY)`)
	if _, err = f.s.Export(context.Background(), f.token, id(t), false); !errors.Is(err, ErrSchema) {
		t.Fatal("unknown business table silently omitted", err)
	}
}
