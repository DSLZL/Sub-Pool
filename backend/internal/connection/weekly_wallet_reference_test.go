package connection

import (
	"math/big"
	"testing"

	"sub-pool/backend/internal/estimation"
)

func TestWeeklyWalletReferenceCheckpointsUseCompleteActualCosts(t *testing.T) {
	work := nativeWeeklyWork{
		Plan: nativeWeeklyPlan{Points: []NativeWeeklyPoint{{UsedPercent: "55"}, {UsedPercent: "56"}}},
		Costs: []nativeWeeklyCost{
			{Cost: estimation.Exact(big.NewRat(150, 1)), WalletCost: estimation.Exact(big.NewRat(109, 1)), Records: 1, WalletRows: 1},
			{Cost: estimation.Exact(big.NewRat(7, 1)), WalletCost: estimation.Exact(big.NewRat(2, 1)), Records: 1, WalletRows: 1},
		},
		Tail: []nativeWeeklyCost{{WalletCost: estimation.Exact(big.NewRat(1, 1)), Records: 1, WalletRows: 1}, {}},
	}
	points, records, complete := work.checkpoints(true)
	if len(points) != 2 || records != 3 || !complete {
		t.Fatalf("expected two complete actual-cost samples, got %d", len(points))
	}
	for _, point := range points {
		capacity, ok := point.Capacity.Rational()
		if !ok || capacity.Cmp(big.NewRat(200, 1)) != 0 {
			t.Fatalf("actual costs should produce a capacity of 200: %+v", point)
		}
		member := weeklyWalletFloor(new(big.Rat).Mul(capacity, big.NewRat(2, 5)))
		if usageDecimal(member) != "80" {
			t.Fatal("40% of the reference capacity should be 80", member)
		}
	}
	work.Costs[1].WalletRows = 0
	if points, _, complete := work.checkpoints(true); len(points) != 1 || points[0].Observation.UsedPercent != "55" || complete {
		t.Fatal("incomplete costs should retain only the earlier verified sample", points)
	}
	work.Costs[0].WalletCost = nil
	if points, _, complete := work.checkpoints(false); len(points) != 2 || !complete {
		t.Fatal("incomplete actual costs must not invalidate standard evidence", points)
	}
	if points, _, _ := work.checkpoints(true); len(points) != 0 {
		t.Fatal("missing actual fees must result in no estimate", points)
	}
	work.Tail = work.Tail[:1]
	if points, _, _ := work.checkpoints(true); len(points) != 0 {
		t.Fatal("a partial stored tail was treated as complete", points)
	}
}
