package connection

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
)

type ParticipantExit struct {
	ParticipantID    string    `json:"participant_id"`
	PreviousRevision string    `json:"previous_revision"`
	Revision         string    `json:"revision"`
	BindingID        string    `json:"binding_id"`
	Name             string    `json:"name"`
	EffectiveAt      time.Time `json:"effective_at"`
	PoolContractIDs  []string  `json:"pool_contract_ids"`
}

var errParticipantExitBlocked = &auth.Failure{Status: 409, Code: "PARTICIPANT_EXIT_BLOCKED"}
var errParticipantExitLimit = &auth.Failure{Status: 409, Code: "PARTICIPANT_EXIT_LIMIT"}

const participantExitContractLimit = 10_000

func participantObligations(ctx context.Context, tx pgx.Tx, ids ...string) error {
	var blocked bool
	// Native requests remain undispatched; simulated outcomes require their persisted resolution.
	err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM wallet_application_requests r WHERE r.participant_id=ANY($1::text[])
			AND NOT (`+walletApplicationResolvedSQL+`))`, ids).Scan(&blocked)
	if err == nil && !blocked {
		blocked, err = temporaryObligations(ctx, tx, nil, ids)
	}
	if err == nil && blocked {
		return errParticipantExitBlocked
	}
	return err
}

func (s *Service) ExitParticipant(ctx context.Context, token, id, revision, confirmation string) (ParticipantExit, error) {
	if !managedID(id) || !validRevision(revision) {
		return ParticipantExit{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return ParticipantExit{}, err
	}
	defer tx.Rollback(ctx)
	// A rare exit serializes all current pool/capture/wallet producers, including a newly created pool.
	c, err := s.config(ctx, tx, true)
	if err != nil {
		return ParticipantExit{}, err
	}
	p, err := readParticipant(ctx, tx, id, c, true, true)
	if err != nil {
		return ParticipantExit{}, err
	}
	if p.Revision != revision {
		return ParticipantExit{}, auth.ErrConflict
	}
	if p.Name != confirmation {
		return ParticipantExit{}, auth.ErrInput
	}
	if err = participantObligations(ctx, tx, id); err != nil {
		return ParticipantExit{}, err
	}
	const memberships = ` FROM pool_contracts c JOIN pool_contract_participants m ON m.contract_id=c.id
		WHERE m.participant_id=$1 AND c.ended_at IS NULL`
	var count int
	if err = tx.QueryRow(ctx, `SELECT count(*)`+memberships, id).Scan(&count); err != nil {
		return ParticipantExit{}, err
	}
	if count > participantExitContractLimit {
		return ParticipantExit{}, errParticipantExitLimit
	}
	if !p.Enabled && count == 0 {
		return ParticipantExit{}, auth.ErrConflict
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return ParticipantExit{}, err
	}
	out := ParticipantExit{ParticipantID: id, PreviousRevision: revision, BindingID: p.Binding.ID, Name: p.Name,
		EffectiveAt: time.Now().UTC().Truncate(time.Microsecond), PoolContractIDs: []string{}}
	for after := ""; len(out.PoolContractIDs) < count; {
		var ids []string
		err = tx.QueryRow(ctx, `SELECT array_agg(id::text ORDER BY pool_id), COALESCE(max(pool_id),'')
			FROM (SELECT c.id,c.pool_id`+memberships+` AND c.pool_id>$2 ORDER BY c.pool_id LIMIT $3) page`,
			id, after, poolContractBatchSize).Scan(&ids, &after)
		if err != nil {
			return ParticipantExit{}, err
		}
		if len(ids) == 0 {
			return ParticipantExit{}, auth.ErrConflict
		}
		for _, contract := range ids {
			previous, err := readPoolContract(ctx, tx, contract, c, auth.Access{AllData: true})
			if err != nil {
				return ParticipantExit{}, err
			}
			members := slices.DeleteFunc(previous.Participants, func(member PoolParticipant) bool { return member.ID == id })
			next, err := appendPoolContract(ctx, tx, actor.ID, previous.PoolID, previous.Revision, previous.Name, previous.Enabled,
				previous.SourceID, previous.ConnectionRevision, previous.Accounts, members, "pool.participant.exit", out.EffectiveAt)
			if err != nil {
				return ParticipantExit{}, err
			}
			out.PoolContractIDs = append(out.PoolContractIDs, next)
		}
	}
	err = tx.QueryRow(ctx, `UPDATE participants SET enabled=false,saved_at=$2,revision=revision+1 WHERE id=$1 RETURNING revision::text`, id, out.EffectiveAt).Scan(&p.Revision)
	out.Revision = p.Revision
	var details []byte
	if err == nil {
		details, err = json.Marshal(out)
	}
	if err == nil {
		err = participantEvent(ctx, tx, actor.ID, p, "participant.exit", details)
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return ParticipantExit{}, err
	}
	return out, nil
}

func (s *Service) GetParticipantExit(ctx context.Context, token, id, previousRevision string) (ParticipantExit, error) {
	if !managedID(id) || !validRevision(previousRevision) {
		return ParticipantExit{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, true)
	if err != nil {
		return ParticipantExit{}, err
	}
	defer tx.Rollback(ctx)
	var body []byte
	err = tx.QueryRow(ctx, `SELECT details FROM participant_events WHERE participant_id=$1 AND action='participant.exit'
		AND details->>'previous_revision'=$2`, id, previousRevision).Scan(&body)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	var out ParticipantExit
	if err == nil {
		err = json.Unmarshal(body, &out)
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return ParticipantExit{}, err
	}
	return out, nil
}
