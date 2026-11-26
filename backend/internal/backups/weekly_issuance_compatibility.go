package backups

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
)

const weeklyIssuanceMigration = "migrations/039_weekly_wallet_issuance.sql"

var weeklyIssuanceTables = []string{"weekly_wallet_issuance_phases", "weekly_wallet_issuances"}

// The catalog uses PostgreSQL's pretty constraint definition from migration 029.
const nativeWalletResultCheck038 = `wallet_application_results_check:CHECK ((((snapshot ->> 'unit'::text) = 'simulated_wallet_usd'::text AND ((snapshot -> 'simulation'::text) ->> 'environment'::text) = 'simulated'::text OR (snapshot ->> 'unit'::text) = 'native_wallet'::text AND jsonb_typeof(snapshot #> '{details,input,native_delta}'::text[]) = 'string'::text AND jsonb_typeof(snapshot #> '{details,native_adjustment}'::text[]) = 'object'::text AND ((snapshot ->> 'state'::text) <> 'applied'::text OR (snapshot #>> '{details,native_adjustment,ledger_id}'::text[]) ~ '^[1-9][0-9]*$'::text AND jsonb_typeof(snapshot #> '{details,native_adjustment,balance_after}'::text[]) = 'string'::text)) AND ((snapshot ->> 'state'::text) = 'applied'::text AND (snapshot ->> 'dispatch_state'::text) = 'confirmed'::text AND resolved OR (snapshot ->> 'state'::text) = 'failed'::text AND (snapshot ->> 'dispatch_state'::text) = 'confirmed_failed'::text AND resolved OR (snapshot ->> 'state'::text) = 'rejected'::text AND (snapshot ->> 'dispatch_state'::text) = 'not_dispatched'::text AND resolved OR (snapshot ->> 'state'::text) = 'unknown'::text AND (snapshot ->> 'dispatch_state'::text) = 'unknown'::text AND NOT resolved)) IS TRUE)`

func schemaBeforeWeeklyIssuance(shape schema) (schema, error) {
	previous := shape
	previous.Migrations = maps.Clone(shape.Migrations)
	delete(previous.Migrations, weeklyIssuanceMigration)
	previous.Tables = maps.Clone(shape.Tables)
	for _, name := range weeklyIssuanceTables {
		delete(previous.Tables, name)
	}
	result := previous.Tables["wallet_application_results"]
	result.Constraints = slices.Clone(result.Constraints)
	for i, check := range result.Constraints {
		if strings.HasPrefix(check, "wallet_application_results_check:") {
			result.Constraints[i] = nativeWalletResultCheck038
		}
	}
	result.Indexes = slices.DeleteFunc(slices.Clone(result.Indexes), func(definition string) bool {
		return strings.HasPrefix(definition, "CREATE UNIQUE INDEX weekly_wallet_issuance_receipt ON ")
	})
	previous.Tables[result.Name] = result
	descriptor, err := json.Marshal(slices.SortedFunc(maps.Values(previous.Tables), func(a, b tableSchema) int {
		return strings.Compare(a.Name, b.Name)
	}))
	if err != nil {
		return schema{}, err
	}
	previous.Fingerprint = hash(descriptor)
	return previous, nil
}

func archiveBusinessTables(shape schema) []string {
	var tables []string
	for _, name := range businessTables {
		if _, exists := shape.Tables[name]; exists {
			tables = append(tables, name)
		}
	}
	return tables
}
