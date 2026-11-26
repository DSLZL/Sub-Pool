package backups

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net/http/httptest"
	"strconv"
	"testing"

	"sub-pool/backend/internal/auth"
)

func TestStreamRejectsWrongKeyReorderedTruncatedAndExtendedFiles(t *testing.T) {
	var archive bytes.Buffer
	plain := bytes.Repeat([]byte("sample"), frameSize/4)
	encode, err := newStreamEncoder(&archive, fixtureKey, lightScope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = encode.Write(plain); err != nil {
		t.Fatal(err)
	}
	if err = encode.finish(streamFooter{BodySHA256: hash(plain)}); err != nil {
		t.Fatal(err)
	}
	read := func(raw, key []byte) error {
		decoder, err := newStreamDecoder(bytes.NewReader(raw), key)
		if err != nil {
			return err
		}
		body, err := io.ReadAll(decoder)
		if err != nil {
			return err
		}
		footer, err := decoder.finish()
		if err == nil && (decoder.scope != lightScope || !bytes.Equal(body, plain) || footer.BodySHA256 != hash(plain)) {
			t.Fatal("archive changed scope, body or footer")
		}
		return err
	}
	raw := archive.Bytes()
	if err = read(raw, fixtureKey); err != nil {
		t.Fatal(err)
	}
	first := streamHeaderSize + framePrefixSize + int(binary.BigEndian.Uint32(raw[streamHeaderSize+9:]))
	next := first + framePrefixSize + int(binary.BigEndian.Uint32(raw[first+9:]))
	reordered := append(append(append(bytes.Clone(raw[:streamHeaderSize]), raw[first:next]...), raw[streamHeaderSize:first]...), raw[next:]...)
	tampered := bytes.Clone(raw)
	tampered[len(streamHeader)] = 1
	for name, invalid := range map[string][]byte{
		"reordered": reordered, "missing footer": raw[:next], "truncated tag": raw[:len(raw)-1],
		"trailing byte": append(bytes.Clone(raw), 0), "modified scope": tampered,
	} {
		t.Run(name, func(t *testing.T) {
			if err := read(invalid, fixtureKey); !errors.Is(err, ErrInvalid) {
				t.Fatal("invalid archive accepted", err)
			}
		})
	}
	if err = read(raw, bytes.Repeat([]byte{7}, 32)); !errors.Is(err, ErrInvalid) {
		t.Fatal("different protection key accepted", err)
	}
}

func downloadBytes(t *testing.T, f fixture, archiveID, planID string) []byte {
	t.Helper()
	response := httptest.NewRecorder()
	if err := f.s.download(context.Background(), response, f.token, archiveID, planID); err != nil {
		t.Fatal(err)
	}
	raw := response.Body.Bytes()
	if response.Code != 200 || response.Header().Get("X-Backup-SHA256") != hash(raw) ||
		response.Header().Get("Content-Length") != strconv.Itoa(len(raw)) {
		t.Fatal("download receipt differs from saved archive", response.Code)
	}
	return raw
}

func TestStreamPreIssuanceCatalogAndHistoryProtection(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	current, err := readSchema(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := schemaBeforeWeeklyIssuance(current)
	if err != nil {
		t.Fatal(err)
	}
	var raw bytes.Buffer
	encoder, err := newStreamEncoder(&raw, f.s.key, fullScope)
	if err != nil {
		t.Fatal(err)
	}
	footer, err := captureStream(ctx, tx, previous, fullScope, true, encoder)
	if err != nil {
		t.Fatal(err)
	}
	if err = encoder.finish(footer); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	legacy := raw.Bytes()
	plan, err := f.s.preflightFile(ctx, f.token, id(t), bytes.NewReader(legacy))
	if err != nil || plan.State != "ready" {
		t.Fatalf("039 could not read a 038 protected archive: %s %v %v", plan.State, plan.Blockers, err)
	}
	seedEveryBusinessTable(t, f)
	// Only the 039 ledgers should account for the protected-history blocker in this fixture.
	exec(t, f.pool, `DELETE FROM weekly_wallet_groups; DELETE FROM weekly_wallet_allocations`)
	plan, err = f.s.preflightFile(ctx, f.token, id(t), bytes.NewReader(legacy))
	if err != nil || plan.State != "blocked" || !contains(plan.Blockers, "WEEKLY_WALLET_HISTORY_CHANGED") {
		t.Fatalf("038 archive erased confirmed issuance history: %s %v %v", plan.State, plan.Blockers, err)
	}
}

func TestStreamLightOverwriteAndRecoveryCopy(t *testing.T) {
	f := setup(t)
	seedEveryBusinessTable(t, f)
	ctx := context.Background()
	exec(t, f.pool, `UPDATE notifications SET state='queued',reason='QUEUED',started_at=NULL,finished_at=NULL`)
	busy, err := f.s.export(ctx, f.token, id(t), true, lightScope)
	if err != nil {
		t.Fatal(err)
	}
	exec(t, f.pool, `UPDATE notifications SET state='unknown',reason='WORKER_INTERRUPTED',started_at=clock_timestamp(),finished_at=clock_timestamp()`)
	blocked, err := f.s.preflightStream(ctx, f.token, id(t), nil, busy.ID)
	if err != nil || blocked.State != "blocked" || !contains(blocked.Blockers, "BACKUP_MAIL_ACTIVE") ||
		!contains(blocked.Blockers, "WEEKLY_WALLET_HISTORY_CHANGED") || blocked.BeforeArchiveID != nil {
		t.Fatal("light archive lost source blockers or current protected history", blocked.Blockers, err)
	}
	exec(t, f.pool, `DELETE FROM weekly_wallet_issuance_phases; DELETE FROM weekly_wallet_issuances;
	 DELETE FROM weekly_wallet_groups; DELETE FROM weekly_wallet_allocations`)
	exec(t, f.pool, `UPDATE usage_capture_facts SET fact=fact || jsonb_build_object('wide',repeat('x',1300000))`)
	otherToken := login(t, f, f.admin.Username)
	archive, err := f.s.export(ctx, f.token, id(t), true, lightScope)
	if err != nil || len(archive.Tables) != len(lightTables) {
		t.Fatal("light export", archive.Tables, err)
	}
	raw := downloadBytes(t, f, archive.ID, "")
	planID := id(t)
	plan, err := f.s.preflightFile(ctx, f.token, planID, bytes.NewReader(raw))
	if err != nil || plan.State != "ready" || plan.BeforeArchiveID == nil || len(plan.ClearedTables) != len(businessTables)-len(lightTables) {
		t.Fatal("light preflight", plan.State, plan.Blockers, err)
	}
	for _, name := range []string{"usage_capture_facts", "quota_observations"} {
		found := false
		for _, table := range plan.ClearedTables {
			found = found || table.Name == name && table.Rows == "1"
		}
		if !found {
			t.Fatal("missing actual cleanup count", name)
		}
	}
	again, err := f.s.preflightFile(ctx, f.token, planID, bytes.NewReader(raw))
	if err != nil || *again.BeforeArchiveID != *plan.BeforeArchiveID {
		t.Fatal("reused preflight created another copy", err)
	}
	if _, err = f.s.preflightFile(ctx, f.token, planID, bytes.NewReader(append(bytes.Clone(raw), 0))); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("a changed upload reused the preflight", err)
	}
	input := ApplyInput{plan.ID, plan.TargetBasis, true}
	if _, err = f.s.Apply(ctx, f.token, input); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("restore did not require copy transfer proof", err)
	}
	before := downloadBytes(t, f, *plan.BeforeArchiveID, plan.ID)
	if len(before) <= frameSize {
		t.Fatal("wide business row was not included")
	}
	if _, err = f.s.Apply(ctx, otherToken, input); !errors.Is(err, auth.ErrConflict) {
		t.Fatal("different session reused transfer proof", err)
	}
	applied, err := f.s.Apply(ctx, f.token, input)
	if err != nil || applied.State != "applied" {
		t.Fatal("light overwrite", applied.State, applied.Reason, err)
	}
	var usage, observations, bindings int
	if err = f.pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM usage_capture_facts),
		(SELECT count(*) FROM quota_observations),(SELECT count(*) FROM participant_bindings)`).Scan(&usage, &observations, &bindings); err != nil ||
		usage != 0 || observations != 0 || bindings != 1 {
		t.Fatal("wrong historical cleanup or retained binding", usage, observations, bindings, err)
	}
	f.token = login(t, f, f.admin.Username)
	rollback, err := f.s.preflightFile(ctx, f.token, id(t), bytes.NewReader(before))
	if err != nil || rollback.State != "ready" || rollback.BeforeArchiveID == nil {
		t.Fatal("full recovery copy", rollback.Blockers, err)
	}
	downloadBytes(t, f, *rollback.BeforeArchiveID, rollback.ID)
	applied, err = f.s.Apply(ctx, f.token, ApplyInput{rollback.ID, rollback.TargetBasis, true})
	if err != nil || applied.State != "applied" {
		t.Fatal("recovery copy apply", applied.State, applied.Reason, err)
	}
	var width int
	var amount string
	if err = f.pool.QueryRow(ctx, `SELECT length(fact->>'wide'),fact->>'actual_cost' FROM usage_capture_facts`).Scan(&width, &amount); err != nil ||
		width != 1300000 || amount != "12345678901234567890.1234567890123456789" {
		t.Fatal("wide row or exact amount was lost", width, amount, err)
	}
}

func TestStreamUploadFailureLeavesNoPayloadOrPlan(t *testing.T) {
	f := setup(t)
	ctx := context.Background()
	seedEveryBusinessTable(t, f)
	exec(t, f.pool, `UPDATE usage_capture_facts SET fact=fact || jsonb_build_object('wide',repeat('x',1300000))`)
	archive, err := f.s.export(ctx, f.token, id(t), true, fullScope)
	if err != nil {
		t.Fatal(err)
	}
	raw := downloadBytes(t, f, archive.ID, "")
	if len(raw) < frameSize {
		t.Fatal("fixture must cross a stored chunk boundary")
	}
	fail := func(reader io.Reader, want error) {
		t.Helper()
		planID := id(t)
		if _, err := f.s.preflightFile(ctx, f.token, planID, reader); !errors.Is(err, want) {
			t.Fatal("upload did not return the expected failure", want, err)
		}
		if _, err := f.s.GetPlan(ctx, f.token, planID); !errors.Is(err, auth.ErrNotFound) {
			t.Fatal("failed upload published a restore plan", err)
		}
		var payloads int
		if err := f.pool.QueryRow(ctx, `SELECT count(*) FROM backup_blobs`).Scan(&payloads); err != nil || payloads != 1 {
			t.Fatal("failed upload retained a payload", payloads, err)
		}
	}
	max := f.s.MaxBytes
	f.s.MaxBytes = int64(len(raw) - 1)
	fail(bytes.NewReader(raw), ErrLimit)
	f.s.MaxBytes = max
	unknown := bytes.Clone(raw[:len(streamHeader)])
	unknown[len(unknown)-2] = '9'
	fail(bytes.NewReader(unknown), ErrSchema)
	fail(io.MultiReader(bytes.NewReader(raw[:len(raw)-20]), canceledRead{}), context.Canceled)
	f.s.MaxBytes = int64(len(raw))
	exec(t, f.pool, `UPDATE notifications SET state='queued',reason='QUEUED',started_at=NULL,finished_at=NULL`)
	atLimit, err := f.s.preflightFile(ctx, f.token, id(t), bytes.NewReader(raw))
	if err != nil || atLimit.State != "blocked" || atLimit.SourceBytes == nil || *atLimit.SourceBytes != strconv.Itoa(len(raw)) ||
		!contains(atLimit.Blockers, "MAIL_ACTIVE") || atLimit.BeforeArchiveID != nil {
		t.Fatal("exact-size upload did not reach target state validation", atLimit.State, err)
	}
}

type canceledRead struct{}

func (canceledRead) Read([]byte) (int, error) { return 0, context.Canceled }
