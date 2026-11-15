package connection

import (
	"testing"
	"time"

	"sub-pool/backend/internal/estimation"
)

func TestTemporaryRestorationSurvivesOnlyAConfirmedReadHeadChange(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*WalletApplication, *WalletRecommendationSnapshot, *walletBasis, *WalletRead)
		want   bool
	}{
		{"confirmed-refresh", func(*WalletApplication, *WalletRecommendationSnapshot, *walletBasis, *WalletRead) {}, true},
		{"unknown-application", func(a *WalletApplication, _ *WalletRecommendationSnapshot, _ *walletBasis, _ *WalletRead) {
			a.State = "unknown"
		}, false},
		{"incomplete-advice", func(_ *WalletApplication, c *WalletRecommendationSnapshot, _ *walletBasis, _ *WalletRead) {
			c.Complete = false
		}, false},
		{"changed-target", func(_ *WalletApplication, c *WalletRecommendationSnapshot, _ *walletBasis, _ *WalletRead) {
			c.Target = &estimation.Amount{Numerator: "10", Denominator: "1"}
		}, false},
		{"changed-policy", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, b *walletBasis, _ *WalletRead) {
			b.PolicyRevision = "2"
		}, false},
		{"changed-source", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, b *walletBasis, _ *WalletRead) {
			b.SourceID = "other"
		}, false},
		{"changed-contract", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, b *walletBasis, _ *WalletRead) {
			b.Sources = []walletSource{{ContractHead: "2"}}
		}, false},
		{"changed-rights", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, b *walletBasis, _ *WalletRead) {
			b.TemporaryHead = "2"
		}, false},
		{"old-read", func(a *WalletApplication, _ *WalletRecommendationSnapshot, _ *walletBasis, r *WalletRead) {
			r.AttemptedAt = a.Details.SimulationProof.OccurredAt.Add(-time.Second)
		}, false},
		{"failed-read", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, _ *walletBasis, r *WalletRead) {
			r.Read.State = "unavailable"
		}, false},
		{"other-epoch", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, _ *walletBasis, r *WalletRead) {
			r.SimulationScope.Epoch = "other"
		}, false},
		{"other-user", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, _ *walletBasis, r *WalletRead) {
			r.SimulationScope.UserID = "22"
		}, false},
		{"later-wallet-version", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, _ *walletBasis, r *WalletRead) {
			r.SimulationScope.WalletVersion = "4"
		}, false},
		{"different-balance", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, _ *walletBasis, r *WalletRead) {
			r.SimulationScope.Balance = "8"
		}, false},
		{"pending-request", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, _ *walletBasis, r *WalletRead) {
			r.SimulationScope.Pending = "1"
		}, false},
		{"missing-source", func(_ *WalletApplication, _ *WalletRecommendationSnapshot, _ *walletBasis, r *WalletRead) {
			r.SimulationScope.SourceAccountIDs = []string{"1"}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := time.Now().UTC()
			basis := walletBasis{WalletHead: "1", PolicyRevision: "1", SourceID: "source"}
			target := estimation.Amount{Numerator: "9", Denominator: "1"}
			scope := simulationWalletScope{SimulationProvenance: SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: simulationTestEpoch}, UserID: "21", Balance: "9", FrozenBalance: "0", WalletVersion: "3", Pending: "0", SourceAccountIDs: []string{"1", "2"}, ScopeComplete: true, BillingType: "standard_wallet", LedgerClosed: true}
			application := WalletApplication{ParticipantID: "member", WalletApplicationSnapshot: WalletApplicationSnapshot{State: "applied", Details: &WalletApplicationDetails{
				Input:              WalletApplicationInput{WalletExpected: WalletExpected{LatestID: "1"}},
				EvaluatedBasisHash: walletRecommendation(basis, "member", now).BasisHash,
				SimulationWrite:    &simulationWalletWriteIntent{OriginalTarget: target},
				SimulationProof:    &simulationWalletWriteProof{SimulationProvenance: scope.SimulationProvenance, UserID: "21", After: scope, OccurredAt: now.Add(-time.Second)},
			}}}
			basis.WalletHead = "2"
			current := WalletRecommendationSnapshot{Complete: true, Target: &target}
			read := WalletRead{AttemptedAt: now, WalletReadSnapshot: WalletReadSnapshot{Read: Check{State: "available"}, SimulationScope: &scope}}
			tc.change(&application, &current, &basis, &read)
			current.BasisHash = walletRecommendation(basis, "member", now).BasisHash
			if got := temporaryRemoteRestored(application, current, basis, &read); got != tc.want {
				t.Fatalf("restored=%v, want %v", got, tc.want)
			}
		})
	}
}
