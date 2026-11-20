package connection

import (
	"context"
	"math/big"
	"slices"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/estimation"
)

type nativeWeeklyShare struct {
	SharePercent string             `json:"share_percent"`
	Capacity     *estimation.Amount `json:"capacity"`
	Remaining    *estimation.Amount `json:"remaining"`
}

type nativeWeeklyMember struct {
	ID   string `json:"participant_id"`
	Name string `json:"name"`
	nativeWeeklyShare
}

type nativeWeeklyAllocation struct {
	PoolID     string               `json:"pool_id"`
	PoolName   string               `json:"pool_name"`
	ContractID string               `json:"contract_id"`
	Members    []nativeWeeklyMember `json:"members"`
	Reserve    *nativeWeeklyShare   `json:"reserve"`
}

func projectedWeeklyShare(capacity, remaining *big.Rat, units int64) nativeWeeklyShare {
	share := big.NewRat(units, shareTotal)
	return nativeWeeklyShare{
		SharePercent: sharePercent(units),
		Capacity:     estimation.Exact(new(big.Rat).Mul(capacity, share)),
		Remaining:    estimation.Exact(new(big.Rat).Mul(remaining, share)),
	}
}

func readWeeklyAllocation(ctx context.Context, tx pgx.Tx, access auth.Access, a storedAccount, c storedConfig, weekly *NativeWeeklyCurrent) (*nativeWeeklyAllocation, error) {
	if weekly == nil || weekly.Latest == nil || weekly.Latest.Cumulative == nil || weekly.Latest.Cumulative.Confidence != "trusted" ||
		weekly.Observation.UsedPercent != weekly.Latest.Observation.UsedPercent || !access.AllData && !slices.Contains(access.Features, "pools") {
		return nil, nil
	}
	scope, args, err := access.ScopeSQL(nil, pgx.Identifier{"member", "participant_id"}, 6)
	if err != nil {
		return nil, err
	}
	args = append([]any{a.ID, a.SourceID, c.Revision, a.fingerprint, access.AllData}, args...)
	rows, err := tx.Query(ctx, `SELECT p.id,pc.id::text,pc.name,
	 CASE WHEN $5 THEN (SELECT COALESCE(sum(share_units),0)::bigint FROM pool_contract_participants WHERE contract_id=pc.id) END,
	 member.participant_id,member.name,member.share_units
	 FROM pool_contract_accounts ca JOIN pool_contracts pc ON pc.id=ca.contract_id
	 JOIN pools p ON p.id=pc.pool_id AND p.revision=pc.revision
	 LEFT JOIN LATERAL (
	  SELECT member.participant_id,member.name,member.share_units FROM pool_contract_participants member
	  JOIN participants m ON m.id=member.participant_id
	  JOIN participant_bindings b ON b.id=member.binding_id
	  WHERE member.contract_id=pc.id AND m.enabled AND m.deleted_at IS NULL
	   AND b.ended_at IS NULL AND b.source_id=$2 AND `+scope+`
	 ) member ON true
	 WHERE ca.account_id=$1 AND ca.current AND ca.source_id=$2 AND ca.resource_fingerprint=$4
	  AND pc.source_id=$2 AND pc.connection_revision=$3 AND pc.enabled
	 ORDER BY member.participant_id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := &nativeWeeklyAllocation{Members: []nativeWeeklyMember{}}
	capacity, _ := weekly.Latest.Capacity.Rational()
	remaining, _ := weekly.Remaining.Rational()
	for rows.Next() {
		var id, name *string
		var units, configured *int64
		if err = rows.Scan(&out.PoolID, &out.ContractID, &out.PoolName, &configured, &id, &name, &units); err != nil {
			return nil, err
		}
		if units != nil {
			out.Members = append(out.Members, nativeWeeklyMember{*id, *name, projectedWeeklyShare(capacity, remaining, *units)})
		}
		if configured != nil && out.Reserve == nil {
			reserve := projectedWeeklyShare(capacity, remaining, shareTotal-*configured)
			out.Reserve = &reserve
		}
	}
	if err = rows.Err(); err != nil || out.Reserve == nil && len(out.Members) == 0 {
		return nil, err
	}
	return out, nil
}
