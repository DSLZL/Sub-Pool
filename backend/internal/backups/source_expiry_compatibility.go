package backups

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
)

const sourceExpiryMigration = "migrations/038_source_block_expiry.sql"

// Derive the exact 037 catalog used to authenticate pre-expiry archive contents.
func schemaBeforeSourceExpiry(shape schema) (schema, error) {
	previous := shape
	previous.Migrations = maps.Clone(shape.Migrations)
	delete(previous.Migrations, sourceExpiryMigration)
	previous.Tables = maps.Clone(shape.Tables)
	rules := previous.Tables["source_blocks"]
	rules.Columns = slices.DeleteFunc(slices.Clone(rules.Columns), func(c column) bool {
		return c.Name == "reason" || c.Name == "expires_at"
	})
	rules.Constraints = slices.DeleteFunc(slices.Clone(rules.Constraints), func(definition string) bool {
		name, _, _ := strings.Cut(definition, ":")
		return name == "source_blocks_reason_check" || name == "source_blocks_expires_at_check"
	})
	rules.Indexes = slices.DeleteFunc(slices.Clone(rules.Indexes), func(definition string) bool {
		return strings.HasPrefix(definition, "CREATE INDEX source_blocks_expiry_idx ON ")
	})
	previous.Tables[rules.Name] = rules
	tables := slices.SortedFunc(maps.Values(previous.Tables), func(a, b tableSchema) int {
		return strings.Compare(a.Name, b.Name)
	})
	descriptor, err := json.Marshal(tables)
	if err != nil {
		return schema{}, err
	}
	previous.Fingerprint = hash(descriptor)
	return previous, nil
}

func compatibleSnapshot(shape schema, data snapshot) (snapshot, error) {
	if data.SchemaFingerprint == shape.Fingerprint {
		return data, nil
	}
	previous, err := schemaBeforeSourceExpiry(shape)
	if err != nil {
		return snapshot{}, err
	}
	upgraded, err := nativeFinancialCompatibleSnapshot(previous, data)
	if err != nil {
		return snapshot{}, err
	}
	at := slices.Index(businessTables, "source_blocks")
	if len(upgraded.Tables) != len(businessTables) {
		return snapshot{}, ErrSchema
	}
	rules := upgraded.Tables[at]
	if rules.Name != "source_blocks" || !reflect.DeepEqual(rules.Columns, names(previous.Tables[rules.Name].Columns)) || rules.Rows == nil {
		return snapshot{}, ErrSchema
	}
	rows := make([]json.RawMessage, len(rules.Rows))
	for i, body := range rules.Rows {
		if !rowFields(body, rules.Columns) {
			return snapshot{}, ErrInvalid
		}
		var row map[string]json.RawMessage
		if err = json.Unmarshal(body, &row); err != nil {
			return snapshot{}, ErrInvalid
		}
		row["reason"], row["expires_at"] = json.RawMessage("null"), json.RawMessage("null")
		if rows[i], err = json.Marshal(row); err != nil {
			return snapshot{}, ErrInvalid
		}
	}
	rules.Columns, rules.Rows = names(shape.Tables[rules.Name].Columns), rows
	upgraded.Tables = slices.Clone(upgraded.Tables)
	upgraded.Tables[at] = rules
	upgraded.SchemaFingerprint, upgraded.Migrations = shape.Fingerprint, shape.Migrations
	return upgraded, nil
}
