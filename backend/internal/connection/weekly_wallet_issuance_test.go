package connection

import (
	"encoding/json"
	"testing"
	"time"
)

func TestWeeklyIssuanceUsesConfirmedCreditAndCarriesExcessOnce(t *testing.T) {
	start := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	key := weeklyWalletAccountKey{"pool", "account"}
	cycle := weeklyWalletCycle{key, start, start.Add(nativeWeek)}
	next := weeklyWalletCycle{key, cycle.End, cycle.End.Add(nativeWeek)}
	advance := weeklyIssuancePosition{
		weeklyIssuanceContribution: weeklyIssuanceContribution{weeklyWalletCycle: cycle, Phase: "advance", Gross: "50", Amount: "50"},
		origin:                     "issuance:1:0",
	}
	formal := weeklyIssuancePlan{Contributions: []weeklyIssuanceContribution{{weeklyWalletCycle: cycle, Phase: "settlement", Gross: "30"}}}
	if issues := (weeklyIssuanceBook{advance}).apply(&formal); len(issues) != 0 || formal.Amount != "0" ||
		formal.Contributions[0].Debt != "20" || formal.Contributions[0].Prior != "50" {
		t.Fatalf("expected no subtraction and debt 20: %+v %v", formal, issues)
	}
	book := weeklyIssuanceBook{advance, {weeklyIssuanceContribution: formal.Contributions[0], origin: "issuance:2:0"}}
	prepay := weeklyIssuancePlan{Contributions: []weeklyIssuanceContribution{{weeklyWalletCycle: next, Phase: "advance", Gross: "50"}}}
	if issues := book.apply(&prepay); len(issues) != 0 || prepay.Amount != "30" || len(prepay.Contributions[0].Offsets) != 1 ||
		prepay.Contributions[0].Offsets[0].Origin != "issuance:2:0" || prepay.Contributions[0].Offsets[0].Amount != "20" {
		t.Fatalf("expected a single 20 offset and credit 30: %+v %v", prepay, issues)
	}
	book = append(book, weeklyIssuancePosition{weeklyIssuanceContribution: prepay.Contributions[0], origin: "issuance:3:0"})
	following := weeklyIssuancePlan{Contributions: []weeklyIssuanceContribution{{weeklyWalletCycle: next, Phase: "settlement", Gross: "50"}}}
	if issues := book.apply(&following); len(issues) != 0 || following.Amount != "0" || following.Contributions[0].Debt != "0" ||
		len(following.Contributions[0].Offsets) != 0 {
		t.Fatalf("previously spent offset was counted again: %+v %v", following, issues)
	}

	baseline, oldTarget := "30", "50"
	p := Participant{Binding: ParticipantBinding{SourceID: "source", UpstreamUserID: "21"}}
	request := WalletApplicationSnapshot{Unit: "native_wallet", State: "pending", Details: &WalletApplicationDetails{
		Input:          WalletApplicationInput{WalletExpected: WalletExpected{SourceID: p.Binding.SourceID}, NativeDelta: "20"},
		UpstreamUserID: p.Binding.UpstreamUserID, NativeAdjustment: &nativeWalletAdjustment{BalanceBefore: &baseline},
	}}
	original, _ := json.Marshal(request)
	request.State, request.DispatchState = "applied", "confirmed"
	request.Details.NativeAdjustment.BalanceAfter, request.Details.NativeAdjustment.LedgerID = &oldTarget, "1"
	result, _ := json.Marshal(request)
	credited, proven := legacyWeeklyCredit(original, result, p)
	advance.Amount, advance.origin = credited, "legacy:single:1"
	remaining := weeklyIssuancePlan{Contributions: []weeklyIssuanceContribution{{weeklyWalletCycle: cycle, Phase: "settlement", Gross: "100"}}}
	if issues := (weeklyIssuanceBook{advance}).apply(&remaining); !proven || credited != "20" || remaining.Amount != "80" || len(issues) != 0 {
		t.Fatalf("expected credit 80 based on old native credit 20: %+v %v", remaining, issues)
	}
	request.Details.Input.NativeDelta = "-20"
	mismatched, _ := json.Marshal(request)
	if _, ok := legacyWeeklyCredit(original, mismatched, p); ok {
		t.Fatal("accepted a mismatched adjustment as legacy proof")
	}
}

func TestWeeklyIssuancePrecisionLeavesTheTail(t *testing.T) {
	plan := weeklyIssuancePlan{Contributions: []weeklyIssuanceContribution{
		{weeklyWalletCycle: weeklyWalletCycle{weeklyWalletAccountKey: weeklyWalletAccountKey{"pool", "a"}}, Gross: "0.000000019"},
		{weeklyWalletCycle: weeklyWalletCycle{weeklyWalletAccountKey: weeklyWalletAccountKey{"pool", "b"}}, Gross: "999999999999.12345678"},
	}}
	issues := (weeklyIssuanceBook{}).apply(&plan)
	if plan.Amount != "0.00000001" || len(plan.Contributions) != 1 || plan.Contributions[0].Tail != "0.000000009" ||
		len(issues) != 1 || issues[0].AccountID != "b" {
		t.Fatalf("incorrect native precision boundary: %+v %v", plan, issues)
	}
}
