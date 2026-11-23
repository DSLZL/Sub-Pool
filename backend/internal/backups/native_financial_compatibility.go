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

// Upgrade the immediately preceding schema after matching its full descriptor and migration digests.
func nativeFinancialCompatibleSnapshot(shape schema, data snapshot) (snapshot, error) {
	if data.SchemaFingerprint == shape.Fingerprint {
		return data, nil
	}
	migrations := maps.Clone(shape.Migrations)
	delete(migrations, nativeFinancialMigration)
	if !maps.Equal(data.Migrations, migrations) || len(data.Tables) != len(businessTables)-1 {
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
		case nativeFinancialSamples:
			continue
		case config.Name:
			table = config
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
