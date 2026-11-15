package connection

import (
	"bytes"
	"encoding/json"
	"math/big"
	"strings"
	"testing"
	"time"

	"sub-pool/backend/internal/estimation"
)

func TestSimulationWalletApplicationTargetUsesDeclaredQuantumAndRetainsRemainder(t *testing.T) {
	for _, tc := range []struct{ original, decimal, remainder, reason string }{
		{"59/26", "2.26923076", "3/325000000", ""},
		{"1/100000000", "0.00000001", "0", ""},
		{"1/100000001", "", "1/100000001", "ZERO_TARGET_PENDING"},
		{"0", "", "0", "ZERO_TARGET_PENDING"},
		{"-1", "", "", "SIMULATION_TARGET_UNAVAILABLE"},
		{"10000000000000000", "", "", "SIMULATION_TARGET_UNAVAILABLE"},
	} {
		t.Run(tc.original, func(t *testing.T) {
			value, ok := new(big.Rat).SetString(tc.original)
			if !ok {
				t.Fatal("invalid independent test input")
			}
			target, remainder, decimal, reason := simulationWalletTarget(estimation.Exact(value), simulationWalletQuantum)
			if decimal != tc.decimal || reason != tc.reason {
				t.Fatal("wrong exact target or gate", target, remainder, decimal, reason)
			}
			if tc.remainder == "" {
				if target != nil || remainder != nil {
					t.Fatal("unqualified target retained money")
				}
				return
			}
			if remainder == nil {
				t.Fatal("unallocated remainder lost")
			}
			remaining, valid := remainder.Rational()
			want, _ := new(big.Rat).SetString(tc.remainder)
			if !valid || remaining.Cmp(want) != 0 {
				t.Fatal("remainder rounded or redistributed", remainder)
			}
			if decimal == "" {
				if target != nil {
					t.Fatal("quantized zero became a small positive substitute")
				}
				return
			}
			amount, valid := target.Rational()
			if !valid || amount.Sign() <= 0 || new(big.Rat).Add(amount, remaining).Cmp(value) != 0 {
				t.Fatal("quantization invented or destroyed entitlement")
			}
		})
	}
	for _, q := range []string{"", "0", "0.01", "0.000000001"} {
		if target, remainder, _, reason := simulationWalletTarget(estimation.Exact(big.NewRat(1, 1)), q); target != nil || remainder != nil || reason != "SIMULATION_TARGET_UNAVAILABLE" {
			t.Fatal("unqualified precision accepted", q)
		}
	}
	if target, _, _, reason := simulationWalletTarget(nil, simulationWalletQuantum); target != nil || reason != "SIMULATION_TARGET_UNAVAILABLE" {
		t.Fatal("unknown became zero")
	}
}

func simulationWalletProofFixture() (simulationWalletWriteIntent, simulationWalletWriteProof) {
	before := simulationWalletScope{SimulationProvenance: SimulationProvenance{Protocol: simulationProtocol, Environment: "simulated", Epoch: simulationTestEpoch}, UserID: "21", Balance: "95", FrozenBalance: "0", WalletVersion: "9007199254740992", LedgerRevision: "17", ClosedThrough: time.Now().UTC().Add(-time.Second), Pending: "0", SourceAccountIDs: []string{"1"}, ScopeComplete: true, BillingType: "standard_wallet", LedgerClosed: true}
	request := simulationWalletWriteRequest{SimulationProvenance: before.SimulationProvenance, OperationID: strings.Repeat("a", 64), UserID: before.UserID, ExpectedWalletVersion: before.WalletVersion, ExpectedLedgerRevision: before.LedgerRevision, ExpectedClosedThrough: before.ClosedThrough, SourceAccountIDs: []string{"1"}, Target: "2.26923076", Quantum: simulationWalletQuantum}
	intent := simulationWalletWriteIntent{Request: request, Before: before, OriginalTarget: *estimation.Exact(big.NewRat(59, 26)), Remainder: *estimation.Exact(big.NewRat(3, 325000000))}
	after := before
	after.Balance, after.WalletVersion, after.LedgerRevision, after.ClosedThrough = request.Target, "9007199254740993", "18", time.Now().UTC().Add(-time.Millisecond)
	proof := simulationWalletWriteProof{SimulationProvenance: before.SimulationProvenance, OperationID: request.OperationID, UserID: before.UserID, RequestHash: simulationWriteHash(request), Request: request, State: "applied", Reason: "APPLIED", Before: before, After: after, OccurredAt: after.ClosedThrough}
	return intent, proof
}

func TestSimulationWalletApplicationProofRequiresExactOperationAndIndependentRead(t *testing.T) {
	intent, proof := simulationWalletProofFixture()
	body, _ := json.Marshal(proof)
	accepted, valid := inspectSimulationWalletProof(body, intent)
	if !valid || accepted == nil || !simulationWalletConfirms(proof.After, *accepted) {
		t.Fatal("exact operation proof and independent wallet were rejected")
	}
	for _, tc := range []struct {
		name   string
		change func(*simulationWalletWriteProof)
	}{
		{"epoch", func(p *simulationWalletWriteProof) { p.Epoch = strings.Repeat("A", 22) }},
		{"operation", func(p *simulationWalletWriteProof) { p.OperationID = strings.Repeat("b", 64) }},
		{"user", func(p *simulationWalletWriteProof) { p.UserID = "22" }},
		{"digest", func(p *simulationWalletWriteProof) { p.RequestHash = strings.Repeat("0", 64) }},
		{"request", func(p *simulationWalletWriteProof) { p.Request.Target = "3" }},
		{"before-balance", func(p *simulationWalletWriteProof) { p.Before.Balance = "96" }},
		{"before-freeze", func(p *simulationWalletWriteProof) { p.Before.FrozenBalance = "1" }},
		{"after-freeze", func(p *simulationWalletWriteProof) { p.After.FrozenBalance = "1" }},
		{"after-pending", func(p *simulationWalletWriteProof) { p.After.Pending = "1" }},
		{"target", func(p *simulationWalletWriteProof) { p.After.Balance = "3" }},
		{"wallet-version", func(p *simulationWalletWriteProof) { p.After.WalletVersion = "9007199254740994" }},
		{"ledger-version", func(p *simulationWalletWriteProof) { p.After.LedgerRevision = "17" }},
		{"scope", func(p *simulationWalletWriteProof) { p.After.SourceAccountIDs = []string{"1", "2"} }},
		{"unsealed", func(p *simulationWalletWriteProof) { p.After.LedgerClosed = false }},
		{"future", func(p *simulationWalletWriteProof) { p.OccurredAt = time.Now().UTC().Add(time.Hour) }},
		{"unsupported-state", func(p *simulationWalletWriteProof) { p.State = "failed" }},
		{"wrong-reason", func(p *simulationWalletWriteProof) { p.Reason = "PRECONDITION_CHANGED" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var changed simulationWalletWriteProof
			_ = json.Unmarshal(body, &changed)
			tc.change(&changed)
			raw, _ := json.Marshal(changed)
			if _, ok := inspectSimulationWalletProof(raw, intent); ok {
				t.Fatal("unproved operation accepted")
			}
		})
	}
	for _, raw := range [][]byte{
		bytes.Replace(body, []byte(`"balance":"95"`), []byte(`"balance":"95","balance":"95"`), 1),
		bytes.Replace(body, []byte(`"quantum":"0.00000001"`), []byte(`"quantum":null`), 1),
		bytes.Replace(body, []byte(`"pending_requests":"0",`), nil, 1),
		bytes.Replace(body, []byte(`"state":"applied"`), []byte(`"state":"applied","\u0073tate":"applied"`), 1),
	} {
		if _, ok := inspectSimulationWalletProof(raw, intent); ok {
			t.Fatal("duplicate, null or missing nested proof accepted")
		}
	}
	current := proof.After
	current.Balance = "1"
	if simulationWalletConfirms(current, proof) {
		t.Fatal("same version with different balance confirmed")
	}
	current.WalletVersion, current.LedgerRevision = "9007199254740994", "19"
	if !simulationWalletConfirms(current, proof) {
		t.Fatal("later independently versioned debit erased historical operation proof")
	}
	current.WalletVersion, current.Balance = intent.Before.WalletVersion, proof.After.Balance
	if simulationWalletConfirms(current, proof) {
		t.Fatal("balance equality substituted for operation version")
	}
	proof.State, proof.Reason, proof.After = "rejected", "PRECONDITION_CHANGED", proof.Before
	body, _ = json.Marshal(proof)
	if _, valid := inspectSimulationWalletProof(body, intent); !valid {
		t.Fatal("immutable explicit no-effect proof rejected")
	}
}
