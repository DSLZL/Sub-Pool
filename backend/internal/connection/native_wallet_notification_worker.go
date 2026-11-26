package connection

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/auth"
	"sub-pool/backend/internal/notifications"
)

func (s *Service) processNativeWalletNotifications(ctx context.Context, after string) (string, error) {
	release, err := s.Runtime.Enter(ctx)
	if err != nil {
		return after, err
	}
	defer release()
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	c, p, revision, preferences, err := s.nextNativeWalletNotification(ctx, after)
	if err != nil || p.ID == "" {
		return "", err
	}
	return p.ID, s.sampleNativeWalletNotification(ctx, c, p, revision, preferences)
}

func (s *Service) sampleNativeWalletNotification(ctx context.Context, c storedConfig, p Participant, revision string, preferences notifications.Preferences) error {
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return err
	}
	read := newWalletRead(c, p)
	network, stopRead := context.WithTimeout(ctx, 3*time.Second)
	read.FieldAuthority = s.nativeWalletAuthority(network, c, p, kind, secret)
	if read.FieldAuthority.State == "available" {
		s.readNativeWalletProfile(network, c, p, kind, secret, &read)
	}
	stopRead()
	var advice nativeWalletFinancialSnapshot
	if preferences.RecommendationChanged && qualifiedNativeWalletRead(read) {
		projection, stopProjection := context.WithTimeout(ctx, 3*time.Second)
		advice, err = s.nativeWalletFinancialAdvice(projection, read)
		stopProjection()
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
	}
	return s.saveNativeWalletFinancialSample(ctx, read, revision, preferences, advice)
}

func (s *Service) nextNativeWalletNotification(ctx context.Context, after string) (storedConfig, Participant, string, notifications.Preferences, error) {
	var member Participant
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return storedConfig{}, member, "", notifications.Preferences{}, err
	}
	defer tx.Rollback(ctx)
	policy, err := readMonitoringConfig(ctx, tx, false)
	if err != nil || !policy.Enabled {
		return storedConfig{}, member, "", notifications.Preferences{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil || nativeFinancialSource(c) == "" || s.simulationEpoch(c) != "" {
		return c, member, "", notifications.Preferences{}, err
	}
	revision, preferences, err := notifications.ReadNativePolicy(ctx, tx, *c.SourceID)
	if err != nil || !preferences.BalanceLow && !preferences.RecommendationChanged {
		return c, member, revision, preferences, err
	}
	var id string
	dueBefore := time.Now().UTC().Add(-time.Duration(policy.IntervalSeconds) * time.Second)
	err = tx.QueryRow(ctx, `SELECT p.id FROM participants p
	 JOIN participant_bindings b ON b.participant_id=p.id AND b.ended_at IS NULL AND b.source_id=$1
	 LEFT JOIN LATERAL (SELECT checked_at,policy_revision,connection_revision,participant_revision
	  FROM native_wallet_financial_samples WHERE participant_id=p.id AND binding_id=b.id ORDER BY id DESC LIMIT 1) n ON true
	 WHERE p.id>$2 AND p.deleted_at IS NULL AND p.enabled AND
	  (n.checked_at IS NULL OR n.checked_at<=$3 OR n.policy_revision<>$4 OR n.connection_revision<>$5 OR n.participant_revision<>p.revision)
	  AND EXISTS(SELECT 1 FROM pool_contract_participants m JOIN pool_contracts pc ON pc.id=m.contract_id
	   JOIN pools q ON q.id=pc.pool_id AND q.revision=pc.revision
	   WHERE m.participant_id=p.id AND m.binding_id=b.id AND q.enabled AND pc.enabled)
	 ORDER BY p.id LIMIT 1`, c.SourceID, after, dueBefore, revision, c.Revision).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, member, revision, preferences, nil
	}
	if err != nil {
		return c, member, revision, preferences, err
	}
	member, err = readParticipant(ctx, tx, id, c, false, true)
	if errors.Is(err, auth.ErrNotFound) {
		return c, Participant{}, revision, preferences, nil
	}
	return c, member, revision, preferences, err
}

func (s *Service) saveNativeWalletFinancialSample(ctx context.Context, read WalletRead, revision string, preferences notifications.Preferences, advice nativeWalletFinancialSnapshot) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if err = lockWallet(ctx, tx, read.ParticipantID); err != nil {
		return err
	}
	env, ok, err := s.weeklyWalletGroupEnvironment(ctx, tx, read.ParticipantID, nil)
	if err != nil || !ok || !currentNativeWalletRead(read, env) {
		return err
	}
	now := time.Now().UTC()
	at := now.Truncate(time.Microsecond)
	if read.ReadAt != nil {
		at = read.ReadAt.Truncate(time.Microsecond)
	}
	staleAfter := time.Duration(env.policy.StaleAfterSeconds) * time.Second
	var snapshot nativeWalletFinancialSnapshot
	if qualifiedNativeWalletRead(read) && !at.After(now) && now.Sub(at) <= staleAfter {
		current, err := financialApplicationsCurrent(ctx, tx, &read)
		if err != nil {
			return err
		}
		if current {
			snapshot, err = s.nativeWalletFinancialBasis(ctx, tx, env, now, preferences.RecommendationChanged)
			if err != nil {
				return err
			}
			if len(snapshot.Cycles) != 0 {
				snapshot.Balance = read.Balance.Value
				if snapshot.sameBasis(advice, true) && slices.Equal(snapshot.AdviceInputs, advice.AdviceInputs) {
					snapshot.Advice = advice.Advice
				}
			}
			if snapshot.Advice == "" {
				snapshot.AdviceInputs = nil
			}
		}
	}
	previous, err := latestNativeWalletFinancialSample(ctx, tx, read.ParticipantID, read.BindingID)
	if err != nil || previous != nil && !read.AttemptedAt.After(previous.CheckedAt) {
		return err
	}
	sample := nativeWalletFinancialSample{Source: read.SourceID, Connection: read.ConnectionRevision,
		Participant: read.ParticipantRevision, Policy: revision, SampledAt: at, CheckedAt: at, Snapshot: snapshot}
	if previous != nil && sample.Source == previous.Source && sample.Connection == previous.Connection &&
		sample.Participant == previous.Participant && sample.Policy == previous.Policy && reflect.DeepEqual(snapshot, previous.Snapshot) &&
		at.Sub(previous.CheckedAt) <= staleAfter {
		if _, err = tx.Exec(ctx, `UPDATE native_wallet_financial_samples SET checked_at=$2 WHERE id=$1`, previous.ID, at); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	err = tx.QueryRow(ctx, `INSERT INTO native_wallet_financial_samples(participant_id,binding_id,source_id,connection_revision,participant_revision,policy_revision,sampled_at,checked_at,snapshot)
	 VALUES($1,$2,$3,$4,$5,$6,$7,$7,$8) RETURNING id::text`,
		read.ParticipantID, read.BindingID, sample.Source, sample.Connection, sample.Participant, sample.Policy, at, snapshot).Scan(&sample.ID)
	if err != nil {
		return err
	}
	for _, kind := range []string{"balance_low", "recommendation_changed"} {
		if kind == "balance_low" && !preferences.BalanceLow || kind == "recommendation_changed" && !preferences.RecommendationChanged {
			continue
		}
		if !nativeWalletFinancialChanged(kind, previous, &sample, staleAfter) {
			continue
		}
		native := &notifications.NativeFinancialEvidence{PolicyRevision: revision, BindingID: read.BindingID}
		var change string
		if kind == "balance_low" {
			native.CycleKey = snapshot.cycleKey()
			change = fmt.Sprintf("Available balance fell from %s to %s below 20 percent of the confirmed issuance %s.",
				*previous.Snapshot.Balance, *snapshot.Balance, snapshot.Issuance)
		} else {
			change = fmt.Sprintf("The recommended weekly issuance changed from %s to %s.", previous.Snapshot.Advice, snapshot.Advice)
		}
		body := fmt.Sprintf("%s Native wallet units for participant %s on source %s. Issuance evidence %s. Previous sample %s at %s, current sample %s at %s.",
			change, read.ParticipantID, read.SourceID, snapshot.ConfirmedID, previous.ID, previous.CheckedAt.Format(time.RFC3339), sample.ID, at.Format(time.RFC3339))
		err = notifications.RecordFinancial(ctx, tx, notifications.FinancialEvidence{
			Kind: kind, SubjectID: read.ParticipantID, SourceID: read.SourceID, ConnectionRevision: read.ConnectionRevision,
			SubjectRevision: read.ParticipantRevision, RecordID: sample.ID, PreviousID: previous.ID, AccountIDs: snapshot.accounts(), AsOf: at, Native: native,
		}, body)
		if err != nil {
			return err
		}
	}
	if previous != nil {
		// Retain the two comparison samples and every sample referenced by a durable event.
		_, err = tx.Exec(ctx, `DELETE FROM native_wallet_financial_samples w
		 WHERE w.participant_id=$1 AND w.binding_id=$2 AND w.id<$3 AND NOT EXISTS (
		  SELECT 1 FROM notifications n WHERE n.participant_id=w.participant_id AND n.financial_evidence ? 'native'
		   AND n.financial_evidence #>> '{native,binding_id}'=w.binding_id::text
		   AND (n.financial_evidence->>'record_id'=w.id::text OR n.financial_evidence->>'previous_id'=w.id::text))`,
			read.ParticipantID, read.BindingID, previous.ID)
		if err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
