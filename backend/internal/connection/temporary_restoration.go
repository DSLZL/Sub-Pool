package connection

import (
	"reflect"
	"time"
)

func temporaryRemoteRestored(application WalletApplication, current WalletRecommendationSnapshot, basis walletBasis, read *WalletRead) bool {
	d := application.Details
	if application.State != "applied" || d == nil || d.SimulationProof == nil || d.SimulationWrite == nil || current.Target == nil || !current.Complete || !reflect.DeepEqual(d.SimulationWrite.OriginalTarget, *current.Target) {
		return false
	}
	if d.EvaluatedBasisHash == current.BasisHash {
		return true
	}
	// A refresh may replace only the read head; every other advice input must still match.
	before := basis
	before.WalletHead = d.Input.LatestID
	if d.EvaluatedBasisHash != walletRecommendation(before, application.ParticipantID, time.Now().UTC()).BasisHash || read == nil || read.SimulationScope == nil || read.Read.State != "available" {
		return false
	}
	scope, proof := read.SimulationScope, d.SimulationProof
	return !read.AttemptedAt.Before(proof.OccurredAt) && scope.SimulationProvenance == proof.SimulationProvenance && scope.UserID == proof.UserID && scope.WalletVersion == proof.After.WalletVersion && simulationWalletClosed(*scope) &&
		simulationMoneyEqual(scope.Balance, proof.After.Balance) && simulationMoneyEqual(scope.FrozenBalance, proof.After.FrozenBalance) && simulationWalletAccounts(scope.SourceAccountIDs, proof.After.SourceAccountIDs)
}
