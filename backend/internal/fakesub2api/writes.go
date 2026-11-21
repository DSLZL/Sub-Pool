package fakesub2api

import (
	"encoding/json"
	"io"
	"maps"
	"math/big"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

func readJSON(r *http.Request, dst any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	return decoder.Decode(dst) == nil && decoder.Decode(new(json.RawMessage)) == io.EOF
}

func (f *fixture) setSchedulable(r *http.Request, _ int) result {
	if r.PathValue("id") != "1" {
		return result{status: http.StatusNotFound}
	}
	var body struct {
		Schedulable *bool `json:"schedulable"`
	}
	if !readJSON(r, &body) || body.Schedulable == nil {
		return result{status: http.StatusBadRequest}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.account["schedulable"] = *body.Schedulable
	f.account["updated_at"] = f.account["updated_at"].(time.Time).Add(time.Microsecond)
	return result{data: maps.Clone(f.account)}
}

func (f *fixture) updateGroup(r *http.Request, _ int) result {
	if r.PathValue("id") != "1" {
		return result{status: http.StatusNotFound}
	}
	var patch map[string]any
	if !readJSON(r, &patch) || len(patch) == 0 {
		return result{status: http.StatusBadRequest}
	}
	for key, value := range patch {
		switch key {
		case "force_openai_fast", "free_openai_fast", "long_context_pricing_enabled":
			if _, ok := value.(bool); !ok {
				return result{status: http.StatusBadRequest}
			}
		case "model_pricing":
			if _, ok := value.([]any); !ok && value != nil {
				return result{status: http.StatusBadRequest}
			}
		default:
			return result{status: http.StatusBadRequest}
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	maps.Copy(f.group, patch)
	f.group["updated_at"] = f.group["updated_at"].(time.Time).Add(time.Microsecond)
	return result{data: maps.Clone(f.group)}
}

type balanceInput struct {
	amount    string
	operation string
	notes     string
}

type appliedBalance struct {
	input balanceInput
	user  map[string]any
}

func (f *fixture) adjustBalance(r *http.Request, _ int) result {
	if r.PathValue("id") != "21" {
		return result{status: http.StatusNotFound}
	}
	var body struct {
		Balance   json.RawMessage `json:"balance"`
		Operation string          `json:"operation"`
		Notes     string          `json:"notes"`
	}
	if !readJSON(r, &body) || body.Operation != "add" && body.Operation != "subtract" {
		return result{status: http.StatusBadRequest}
	}
	amount, valid := positiveAmount(body.Balance)
	if !valid {
		return result{status: http.StatusBadRequest}
	}
	input := balanceInput{amount.RatString(), body.Operation, body.Notes}
	key := r.Header.Get("Idempotency-Key")
	f.mu.Lock()
	defer f.mu.Unlock()
	if previous, ok := f.applied[key]; ok {
		if previous.input != input {
			return result{status: http.StatusConflict}
		}
		return result{data: previous.user}
	}
	if body.Operation == "subtract" {
		amount.Neg(amount)
	}
	next := new(big.Rat).Add(f.balance, amount)
	if next.Sign() < 0 {
		return result{status: http.StatusBadRequest}
	}
	f.balance = next
	id := len(f.history) + 1
	at := f.at.Add(time.Duration(id) * time.Microsecond)
	f.history = append(f.history, map[string]any{
		"id": id, "used_by": 21, "type": "admin_balance", "status": "used",
		"value": decimal(amount), "notes": body.Notes, "created_at": at, "used_at": at,
	})
	user := f.user()
	if key != "" {
		f.applied[key] = appliedBalance{input: input, user: user}
	}
	return result{data: user}
}

func positiveAmount(raw json.RawMessage) (*big.Rat, bool) {
	value := string(raw)
	if len(value) == 0 || len(value) > 64 || value[0] < '0' || value[0] > '9' {
		return nil, false
	}
	if i := strings.IndexAny(value, "eE"); i >= 0 {
		exponent, err := strconv.Atoi(value[i+1:])
		if err != nil || exponent < -18 || exponent > 18 {
			return nil, false
		}
	}
	amount, ok := new(big.Rat).SetString(value)
	return amount, ok && amount.Sign() > 0
}

func decimal(value *big.Rat) json.Number {
	digits, _ := value.FloatPrec()
	return json.Number(value.FloatString(digits))
}

func (f *fixture) listBalanceHistory(r *http.Request, delta int) result {
	if r.PathValue("id") != "21" {
		return result{status: http.StatusNotFound}
	}
	if !matches(r.URL.Query().Get("type"), "admin_balance") {
		return listing(r, nil, delta)
	}
	f.mu.Lock()
	history := slices.Clone(f.history)
	f.mu.Unlock()
	slices.Reverse(history)
	return listing(r, history, delta)
}
