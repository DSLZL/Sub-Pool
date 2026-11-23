package backups

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
)

const nativeFinancialMigration = "migrations/036_native_financial_notifications.sql"
const nativeFinancialSamples = "native_wallet_financial_samples"
const streamingMigration = "migrations/037_streaming_backups.sql"

// Match the complete historical catalog before adapting the candidate to the current schema.
func nativeFinancialCompatibleSnapshot(shape schema, data snapshot) (snapshot, error) {
	if data.SchemaFingerprint == shape.Fingerprint {
		return data, nil
	}
	migrations := maps.Clone(shape.Migrations)
	delete(migrations, streamingMigration)
	older := !maps.Equal(data.Migrations, migrations)
	if older {
		delete(migrations, nativeFinancialMigration)
	}
	if !maps.Equal(data.Migrations, migrations) || !slices.Equal(data.Excluded, legacyExcludedTables) {
		return snapshot{}, ErrSchema
	}
	config := shape.Tables["notification_config"]
	config.Columns = slices.DeleteFunc(slices.Clone(config.Columns), func(c column) bool { return c.Name == "native_source_id" })
	config.Constraints = slices.DeleteFunc(slices.Clone(config.Constraints), func(c string) bool {
		return strings.HasPrefix(c, "notification_config_native_source_id_fkey:") ||
			strings.HasSuffix(c, ":CHECK (financial_enabled OR native_source_id IS NULL)")
	})
	tables := make([]tableSchema, 0, len(shape.Tables)-1)
	for name, table := range shape.Tables {
		switch name {
		case "backup_blobs", "backup_blob_chunks":
			continue
		case "backup_archives", "restore_plans":
			table = legacyFileTable(table)
		case nativeFinancialSamples:
			if older {
				continue
			}
		case config.Name:
			if older {
				table = config
			}
		}
		tables = append(tables, table)
	}
	slices.SortFunc(tables, func(a, b tableSchema) int { return strings.Compare(a.Name, b.Name) })
	descriptor, err := json.Marshal(tables)
	if err != nil || hash(descriptor) != data.SchemaFingerprint {
		return snapshot{}, ErrSchema
	}

	upgraded := data
	upgraded.SchemaFingerprint, upgraded.Migrations = shape.Fingerprint, shape.Migrations
	upgraded.Excluded = excludedTables
	if !older {
		return upgraded, nil
	}
	if len(data.Tables) != len(businessTables)-1 {
		return snapshot{}, ErrSchema
	}
	upgraded.Tables = make([]tableData, 0, len(businessTables))
	next := 0
	for _, name := range businessTables {
		if name == nativeFinancialSamples {
			upgraded.Tables = append(upgraded.Tables, tableData{Name: name, Columns: names(shape.Tables[name].Columns), Rows: []json.RawMessage{}})
			continue
		}
		table := data.Tables[next]
		next++
		if table.Name != name {
			return snapshot{}, ErrSchema
		}
		if name == config.Name {
			if !reflect.DeepEqual(table.Columns, names(config.Columns)) || len(table.Rows) != 1 || !rowFields(table.Rows[0], table.Columns) {
				return snapshot{}, ErrSchema
			}
			var row map[string]json.RawMessage
			if err := json.Unmarshal(table.Rows[0], &row); err != nil {
				return snapshot{}, ErrInvalid
			}
			row["native_source_id"] = json.RawMessage("null")
			body, err := json.Marshal(row)
			if err != nil {
				return snapshot{}, ErrInvalid
			}
			table.Columns, table.Rows = names(shape.Tables[name].Columns), []json.RawMessage{body}
		}
		upgraded.Tables = append(upgraded.Tables, table)
	}
	sequence := shape.Tables[nativeFinancialSamples].Columns[0].Sequence
	if sequence == nil {
		return snapshot{}, ErrSchema
	}
	at := slices.Index(businessTables, nativeFinancialSamples)
	pos := len(data.Sequences)
	for i, row := range data.Sequences {
		if slices.Index(businessTables, row.Table) >= at {
			pos = i
			break
		}
	}
	upgraded.Sequences = slices.Insert(slices.Clone(data.Sequences), pos, sequenceNext{Table: nativeFinancialSamples, Column: "id", Next: sequence.Start})
	return upgraded, nil
}

func legacyFileTable(table tableSchema) tableSchema {
	table.Columns = slices.Clone(table.Columns)
	table.Constraints = slices.Clone(table.Constraints)
	for i, c := range table.Columns {
		if c.Name == "file" {
			table.Columns[i].NotNull = true
		}
	}
	table.Columns = slices.DeleteFunc(table.Columns, func(c column) bool {
		return contains([]string{"blob_id", "file_bytes", "snapshot_basis", "cleared_tables", "before_download_proof"}, c.Name)
	})
	table.Constraints = slices.DeleteFunc(table.Constraints, func(c string) bool {
		name, _, _ := strings.Cut(strings.TrimPrefix(c, table.Name+"_"), ":")
		return name == "payload" || strings.HasPrefix(name, "blob_id_") ||
			strings.HasPrefix(name, "cleared_tables_") || strings.HasPrefix(name, "before_download_proof_")
	})
	if slices.Contains(table.Constraints, table.Name+"_file_sha256_not_null:NOT NULL file_sha256") {
		table.Constraints = append(table.Constraints, table.Name+"_file_not_null:NOT NULL file")
		slices.SortFunc(table.Constraints, func(a, b string) int {
			a, _, _ = strings.Cut(a, ":")
			b, _, _ = strings.Cut(b, ":")
			return strings.Compare(a, b)
		})
	}
	return table
}
