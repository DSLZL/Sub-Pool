package backups

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/database"
)

// Runtime identities and target-local recovery material never travel in a business archive.
var excludedTables = []string{"auth_sessions", "backup_archives", "monitoring_worker", "restore_plans"}
var singletonTables = []string{"connection_config", "monitoring_config", "notification_config", "security_config", "system_settings", "wallet_automation"}
var businessTables = []string{
	"account_events", "announcement_reads", "auth_api_keys", "auth_events", "auth_login_limits", "auth_users",
	"collection_jobs", "collection_runs", "connection_config", "connection_sources", "dispatch_events", "dispatch_pauses", "estimate_config", "estimate_results",
	"maintenance_plans", "managed_accounts", "management_events", "monitoring_config", "notification_config", "notifications",
	"observation_changes", "observation_management", "participant_bindings", "participant_events", "participants",
	"pool_contract_accounts", "pool_contract_participants", "pool_contracts", "pool_events", "pools", "pricing_changes", "pricing_events", "quota_observations",
	"security_config", "source_blocks", "system_events", "system_settings", "usage_attributions", "usage_capture_facts",
	"usage_captures", "usage_events", "wallet_application_requests", "wallet_application_results", "wallet_automation", "wallet_reads", "wallet_recommendations",
}

type column struct {
	Name      string              `json:"name"`
	Type      string              `json:"type"`
	NotNull   bool                `json:"not_null"`
	Identity  string              `json:"identity"`
	Generated string              `json:"generated"`
	Default   string              `json:"default"`
	Sequence  *sequenceDefinition `json:"sequence"`
}
type sequenceDefinition struct {
	Name      string `json:"name"`
	Start     string `json:"start"`
	Increment string `json:"increment"`
	Min       string `json:"min"`
	Max       string `json:"max"`
	Cache     string `json:"cache"`
	Cycle     bool   `json:"cycle"`
}
type tableSchema struct {
	Name        string   `json:"name"`
	Columns     []column `json:"columns"`
	Primary     []string `json:"primary"`
	Constraints []string `json:"constraints"`
	Indexes     []string `json:"indexes"`
	Triggers    []string `json:"triggers"`
}
type schema struct {
	Namespace   string
	Fingerprint string
	Migrations  map[string]string
	Tables      map[string]tableSchema
}

func ident(parts ...string) string        { return pgx.Identifier(parts).Sanitize() }
func (s schema) table(name string) string { return ident(s.Namespace, name) }
func tempTable(name string) string        { return ident("pg_temp", "sp_restore_"+name) }
func names(columns []column) []string {
	result := make([]string, len(columns))
	for i, c := range columns {
		result[i] = c.Name
	}
	return result
}
func columnSQL(columns []string) string {
	result := make([]string, len(columns))
	for i, c := range columns {
		result[i] = ident(c)
	}
	return strings.Join(result, ",")
}

func readSchema(ctx context.Context, tx pgx.Tx) (schema, error) {
	s := schema{Tables: map[string]tableSchema{}}
	if err := tx.QueryRow(ctx, `SELECT current_schema()`).Scan(&s.Namespace); err != nil {
		return s, err
	}
	expected, err := database.MigrationManifest()
	if err != nil {
		return s, err
	}
	rows, err := tx.Query(ctx, `SELECT name,digest FROM schema_migrations ORDER BY name`)
	if err != nil {
		return s, err
	}
	s.Migrations = map[string]string{}
	for rows.Next() {
		var name, digest string
		if err = rows.Scan(&name, &digest); err != nil {
			break
		}
		s.Migrations[name] = digest
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return s, err
	}
	if !reflect.DeepEqual(expected, s.Migrations) {
		return s, ErrSchema
	}
	rows, err = tx.Query(ctx, `SELECT relname FROM pg_class WHERE relnamespace=$1::regnamespace AND relkind IN ('r','p','v','m','f') ORDER BY relname`, s.Namespace)
	if err != nil {
		return s, err
	}
	var actual []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		actual = append(actual, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return s, err
	}
	want := append(append(append([]string{}, businessTables...), excludedTables...), "schema_migrations")
	sort.Strings(want)
	if !reflect.DeepEqual(actual, want) {
		return s, ErrSchema
	}
	normalize := func(value string) string {
		value = strings.ReplaceAll(value, ident(s.Namespace)+".", "")
		return strings.ReplaceAll(value, s.Namespace+".", "")
	}
	descriptors := make([]tableSchema, 0, len(want))
	for _, name := range want {
		t := tableSchema{Name: name, Columns: []column{}, Primary: []string{}, Constraints: []string{}, Indexes: []string{}, Triggers: []string{}}
		rows, err = tx.Query(ctx, `SELECT a.attname,format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attidentity::text,a.attgenerated::text,COALESCE(pg_get_expr(d.adbin,d.adrelid),'') FROM pg_attribute a LEFT JOIN pg_attrdef d ON d.adrelid=a.attrelid AND d.adnum=a.attnum WHERE a.attrelid=$1::regclass AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum`, s.table(name))
		if err != nil {
			return s, err
		}
		for rows.Next() {
			var c column
			if err = rows.Scan(&c.Name, &c.Type, &c.NotNull, &c.Identity, &c.Generated, &c.Default); err != nil {
				break
			}
			c.Default = normalize(c.Default)
			if c.Generated != "" {
				err = ErrSchema
				break
			}
			t.Columns = append(t.Columns, c)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return s, err
		}
		if err = tx.QueryRow(ctx, `SELECT ARRAY(SELECT a.attname FROM pg_constraint c CROSS JOIN LATERAL unnest(c.conkey) WITH ORDINALITY k(num,ord) JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.num WHERE c.conrelid=$1::regclass AND c.contype='p' ORDER BY k.ord)`, s.table(name)).Scan(&t.Primary); err != nil {
			return s, err
		}
		if len(t.Primary) == 0 {
			return s, ErrSchema
		}
		queries := []struct {
			sql string
			out *[]string
		}{
			{`SELECT conname||':'||pg_get_constraintdef(oid,true) FROM pg_constraint WHERE conrelid=$1::regclass ORDER BY conname`, &t.Constraints},
			{`SELECT pg_get_indexdef(indexrelid,0,true) FROM pg_index WHERE indrelid=$1::regclass ORDER BY indexrelid::regclass::text`, &t.Indexes},
			{`SELECT pg_get_triggerdef(oid,true) FROM pg_trigger WHERE tgrelid=$1::regclass AND NOT tgisinternal ORDER BY tgname`, &t.Triggers},
		}
		for _, q := range queries {
			rows, err = tx.Query(ctx, q.sql, s.table(name))
			if err != nil {
				return s, err
			}
			for rows.Next() {
				var value string
				if err = rows.Scan(&value); err != nil {
					break
				}
				*q.out = append(*q.out, normalize(value))
			}
			if err == nil {
				err = rows.Err()
			}
			rows.Close()
			if err != nil {
				return s, err
			}
		}
		// Product migrations install no custom triggers; imports must not execute an added hook.
		if len(t.Triggers) != 0 {
			return s, ErrSchema
		}
		for i, c := range t.Columns {
			if c.Identity == "" && !strings.HasPrefix(c.Default, "nextval(") {
				continue
			}
			var seq string
			if err = tx.QueryRow(ctx, `SELECT pg_get_serial_sequence($1,$2)`, s.table(name), c.Name).Scan(&seq); err != nil {
				return s, err
			}
			var d sequenceDefinition
			if err = tx.QueryRow(ctx, `SELECT c.relname,s.seqstart::text,s.seqincrement::text,s.seqmin::text,s.seqmax::text,s.seqcache::text,s.seqcycle FROM pg_sequence s JOIN pg_class c ON c.oid=s.seqrelid WHERE s.seqrelid=$1::regclass`, seq).Scan(&d.Name, &d.Start, &d.Increment, &d.Min, &d.Max, &d.Cache, &d.Cycle); err != nil {
				return s, err
			}
			if d.Increment != "1" || d.Cycle || d.Cache != "1" {
				return s, ErrSchema
			}
			t.Columns[i].Sequence = &d
		}
		s.Tables[name] = t
		descriptors = append(descriptors, t)
	}
	body, err := json.Marshal(descriptors)
	if err != nil {
		return s, err
	}
	s.Fingerprint = hash(body)
	return s, nil
}

func validateRows(ctx context.Context, tx pgx.Tx, shape schema, data snapshot) error {
	if data.Format != fileFormat || data.SchemaFingerprint != shape.Fingerprint || !reflect.DeepEqual(data.Migrations, shape.Migrations) || !reflect.DeepEqual(data.Excluded, excludedTables) || len(data.Tables) != len(businessTables) {
		return ErrSchema
	}
	for i, name := range businessTables {
		t := data.Tables[i]
		expected := shape.Tables[name]
		if t.Name != name || !reflect.DeepEqual(t.Columns, names(expected.Columns)) || t.Rows == nil || len(t.Rows) > maxTableRows {
			return ErrSchema
		}
		if contains(singletonTables, name) && len(t.Rows) != 1 {
			return ErrInvalid
		}
		for _, row := range t.Rows {
			if !rowFields(row, t.Columns) {
				return ErrInvalid
			}
		}
		if _, err := tx.Exec(ctx, `CREATE TEMP TABLE `+tempTable(name)+` (LIKE `+shape.table(name)+` INCLUDING ALL) ON COMMIT DROP`); err != nil {
			return err
		}
		body, err := json.Marshal(t.Rows)
		if err != nil {
			return ErrInvalid
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+tempTable(name)+` (`+columnSQL(t.Columns)+`) OVERRIDING SYSTEM VALUE SELECT `+columnSQL(t.Columns)+` FROM json_populate_recordset(NULL::`+shape.table(name)+`,$1::json)`, string(body)); err != nil {
			return ErrInvalid
		}
	}
	// LIKE copies checks, indexes and uniqueness; foreign keys must refer to the candidate tables.
	for _, name := range businessTables {
		rows, err := tx.Query(ctx, `SELECT c.conname,pg_get_constraintdef(c.oid,true),c.confrelid::regclass::text,r.relname FROM pg_constraint c JOIN pg_class r ON r.oid=c.confrelid WHERE c.conrelid=$1::regclass AND c.contype='f' ORDER BY c.conname`, shape.table(name))
		if err != nil {
			return err
		}
		type constraint struct{ name, definition, reference, target string }
		var all []constraint
		for rows.Next() {
			var c constraint
			if err = rows.Scan(&c.name, &c.definition, &c.reference, &c.target); err != nil {
				break
			}
			all = append(all, c)
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()
		if err != nil {
			return err
		}
		for _, c := range all {
			if _, ok := shape.Tables[c.target]; !ok || !contains(businessTables, c.target) {
				return ErrSchema
			}
			needle := "REFERENCES " + c.reference
			if !strings.Contains(c.definition, needle) {
				return errors.New("foreign key descriptor unavailable")
			}
			definition := strings.Replace(c.definition, needle, "REFERENCES "+tempTable(c.target), 1)
			if _, err = tx.Exec(ctx, `ALTER TABLE `+tempTable(name)+` ADD CONSTRAINT `+ident(c.name)+` `+definition); err != nil {
				return ErrInvalid
			}
		}
	}
	var admin bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+tempTable("auth_users")+` WHERE role='admin' AND enabled)`).Scan(&admin); err != nil {
		return err
	}
	if !admin {
		return ErrInvalid
	}
	if !data.IncludeSecrets {
		var clean bool
		if err := tx.QueryRow(ctx, `SELECT c.secret IS NULL AND c.credential_kind IS NULL AND n.secret IS NULL FROM `+tempTable("connection_config")+` c CROSS JOIN `+tempTable("notification_config")+` n`).Scan(&clean); err != nil {
			return ErrInvalid
		}
		if !clean {
			return ErrInvalid
		}
	}
	return validateSequences(ctx, tx, shape, data)
}

func contains(list []string, value string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}
