package connection

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"sub-pool/backend/internal/audit"
	"sub-pool/backend/internal/auth"
)

type ParticipantBinding struct {
	ID             string      `json:"id"`
	ParticipantID  string      `json:"participant_id"`
	SourceID       string      `json:"source_id"`
	UpstreamUserID string      `json:"upstream_user_id"`
	StartedAt      time.Time   `json:"started_at"`
	EndedAt        *time.Time  `json:"ended_at"`
	Identity       *NativeUser `json:"identity"`
}

type Participant struct {
	ID            string             `json:"id"`
	Revision      string             `json:"revision"`
	Name          string             `json:"name"`
	Contact       *string            `json:"contact"`
	Notes         *string            `json:"notes"`
	Enabled       bool               `json:"enabled"`
	CreatedAt     time.Time          `json:"created_at"`
	SavedAt       time.Time          `json:"saved_at"`
	SourceCurrent bool               `json:"source_current"`
	Binding       ParticipantBinding `json:"binding"`
}

type ParticipantPage struct {
	Participants []Participant `json:"participants"`
	NextAfter    *string       `json:"next_after"`
}

type ParticipantInput struct {
	SourceID           string `json:"source_id"`
	ConnectionRevision string `json:"connection_revision"`
	UpstreamID         string `json:"upstream_id"`
	Name               string `json:"name"`
	Contact            string `json:"contact"`
	Notes              string `json:"notes"`
	Enabled            bool   `json:"enabled"`
}

type ParticipantUpdate struct {
	ParticipantInput
	ID       string `json:"id"`
	Revision string `json:"revision"`
}

type ParticipantMutation struct {
	Participant *Participant `json:"participant"`
	Read        *Check       `json:"read"`
}

type UserCandidates struct {
	SourceID           string       `json:"source_id"`
	ConnectionRevision string       `json:"connection_revision"`
	Page               int          `json:"page"`
	NextPage           *int         `json:"next_page"`
	Total              *string      `json:"total"`
	Users              []NativeUser `json:"users"`
	Read               Check        `json:"read"`
}

func (s *Service) DiscoverUsers(ctx context.Context, token string, input DiscoverInput) (UserCandidates, error) {
	if !validRevision(input.Revision) || !managedID(input.SourceID) || input.Page < 1 || input.Page > 2147483647 || len(input.Search) > 256 || !utf8.ValidString(input.Search) || strings.ContainsFunc(input.Search, unicode.IsControl) || utf8.RuneCountInString(input.Search) > 100 {
		return UserCandidates{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return UserCandidates{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return UserCandidates{}, err
	}
	if c.SourceID == nil || *c.SourceID != input.SourceID || c.Revision != input.Revision {
		return UserCandidates{}, auth.ErrConflict
	}
	kind, secret, err := s.decrypt(c)
	if err != nil {
		return UserCandidates{}, err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return UserCandidates{}, err
	}
	query := url.Values{"page": {strconv.Itoa(input.Page)}, "page_size": {"100"}, "search": {input.Search}, "include_subscriptions": {"false"}, "sort_by": {"id"}, "sort_order": {"asc"}}
	body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/users?"+query.Encode(), kind, secret, "users.read", true)
	result := UserCandidates{SourceID: *c.SourceID, ConnectionRevision: c.Revision, Page: input.Page, Users: []NativeUser{}, Read: check}
	if check.State == "available" {
		page, valid := parsePage(body, input.Page, 100)
		seen := map[string]bool{}
		if valid {
			for _, item := range page.Items {
				user, ok := projectNativeUser(item, "", secret)
				if !ok || seen[user.UpstreamID] {
					valid = false
					break
				}
				seen[user.UpstreamID] = true
				result.Users = append(result.Users, user)
			}
		}
		if valid && *page.Total <= 214748364700 {
			total := strconv.FormatInt(*page.Total, 10)
			result.Total = &total
			if int64(input.Page)*100 < *page.Total {
				next := input.Page + 1
				result.NextPage = &next
			}
		} else {
			result.Users, result.Read = []NativeUser{}, invalid(check)
		}
	}
	outcome := "succeeded"
	if result.Read.State != "available" {
		outcome = "failed"
	}
	_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor.ID, Action: "participant.candidates", Outcome: outcome, TargetType: "connection", TargetID: *c.SourceID, SourceID: c.SourceID, Revision: &c.Revision})
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return UserCandidates{}, err
	}
	return result, nil
}

func validParticipant(input ParticipantInput) bool {
	return managedID(input.SourceID) && validRevision(input.ConnectionRevision) && validRevision(input.UpstreamID) && localAccountInput(input.Name, "passive") &&
		utf8.ValidString(input.Contact) && len(input.Contact) <= 512 && !strings.ContainsFunc(input.Contact, unicode.IsControl) &&
		utf8.ValidString(input.Notes) && len(input.Notes) <= 2048 && !strings.ContainsFunc(input.Notes, unicode.IsControl)
}

const participantColumns = `p.id,p.revision,p.name,p.contact,p.notes,p.enabled,p.created_at,p.saved_at,b.id,b.source_id,b.upstream_user_id,b.started_at,b.ended_at,b.snapshot`
const participantJoin = ` FROM participants p JOIN LATERAL (SELECT * FROM participant_bindings WHERE participant_id=p.id ORDER BY participant_bindings.id DESC LIMIT 1) b ON true`

func scanParticipant(row pgx.Row, c storedConfig, all bool) (Participant, error) {
	var p Participant
	var rev, binding, upstream int64
	var body []byte
	err := row.Scan(&p.ID, &rev, &p.Name, &p.Contact, &p.Notes, &p.Enabled, &p.CreatedAt, &p.SavedAt, &binding, &p.Binding.SourceID, &upstream, &p.Binding.StartedAt, &p.Binding.EndedAt, &body)
	if errors.Is(err, pgx.ErrNoRows) {
		err = auth.ErrNotFound
	}
	if err != nil {
		return p, err
	}
	p.Revision, p.Binding.ID, p.Binding.UpstreamUserID = strconv.FormatInt(rev, 10), strconv.FormatInt(binding, 10), strconv.FormatInt(upstream, 10)
	p.Binding.ParticipantID = p.ID
	p.SourceCurrent = c.SourceID != nil && *c.SourceID == p.Binding.SourceID
	if all {
		err = json.Unmarshal(body, &p.Binding.Identity)
	} else {
		p.Contact, p.Notes = nil, nil
	}
	p.CreatedAt, p.SavedAt, p.Binding.StartedAt = p.CreatedAt.UTC(), p.SavedAt.UTC(), p.Binding.StartedAt.UTC()
	p.Binding.EndedAt = utc(p.Binding.EndedAt)
	return p, err
}

func readParticipant(ctx context.Context, tx pgx.Tx, id string, c storedConfig, write, all bool) (Participant, error) {
	lock := " FOR SHARE OF p"
	if write {
		// Non-key edits must remain compatible with captured-history foreign keys.
		lock = " FOR NO KEY UPDATE OF p"
	}
	return scanParticipant(tx.QueryRow(ctx, `SELECT `+participantColumns+participantJoin+` WHERE p.id=$1 AND p.deleted_at IS NULL`+lock, id), c, all)
}

func (s *Service) ListParticipants(ctx context.Context, token, after, search string, limit int) (ParticipantPage, error) {
	if after != "" && !managedID(after) || limit < 1 || limit > 100 || len(search) > 256 || !utf8.ValidString(search) || strings.ContainsFunc(search, unicode.IsControl) {
		return ParticipantPage{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return ParticipantPage{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "pools")
	if err != nil {
		return ParticipantPage{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ParticipantPage{}, err
	}
	scope, args, err := access.ScopeSQL(nil, pgx.Identifier{"p", "id"}, 1)
	if err != nil {
		return ParticipantPage{}, err
	}
	n := len(args) + 1
	pattern := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(search) + "%"
	args = append(args, after, pattern, limit+1)
	rows, err := tx.Query(ctx, `SELECT `+participantColumns+participantJoin+` WHERE p.deleted_at IS NULL AND `+scope+` AND p.id>$`+strconv.Itoa(n)+` AND p.name ILIKE $`+strconv.Itoa(n+1)+` ORDER BY p.id LIMIT $`+strconv.Itoa(n+2), args...)
	if err != nil {
		return ParticipantPage{}, err
	}
	result := ParticipantPage{Participants: []Participant{}}
	for rows.Next() {
		p, e := scanParticipant(rows, c, access.AllData)
		if e != nil {
			rows.Close()
			return ParticipantPage{}, e
		}
		result.Participants = append(result.Participants, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ParticipantPage{}, err
	}
	if len(result.Participants) > limit {
		result.Participants = result.Participants[:limit]
		result.NextAfter = &result.Participants[limit-1].ID
	}
	if _, err = s.auth.AuthorizeRead(ctx, tx, token, "pools"); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return ParticipantPage{}, err
	}
	return result, nil
}

func (s *Service) GetParticipant(ctx context.Context, token, id string) (Participant, error) {
	if !managedID(id) {
		return Participant{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return Participant{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "pools")
	if err == nil {
		err = access.CheckParticipant(id)
	}
	if err != nil {
		return Participant{}, err
	}
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return Participant{}, err
	}
	p, err := readParticipant(ctx, tx, id, c, false, access.AllData)
	if err == nil {
		_, err = s.auth.AuthorizeRead(ctx, tx, token, "pools")
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return Participant{}, err
	}
	return p, nil
}

func participantEvent(ctx context.Context, tx pgx.Tx, actor string, p Participant, action string) error {
	_, err := tx.Exec(ctx, `INSERT INTO participant_events(occurred_at,actor_id,participant_id,revision,action) VALUES ($1,$2,$3,$4,$5)`, time.Now().UTC(), actor, p.ID, p.Revision, action)
	if err == nil {
		_, err = audit.Record(ctx, tx, audit.Entry{ActorID: &actor, Action: action, Outcome: "succeeded", TargetType: "participant", TargetID: p.ID, Revision: &p.Revision, SourceID: &p.Binding.SourceID})
	}
	return err
}

func insertParticipantBinding(ctx context.Context, tx pgx.Tx, id string, input ParticipantInput, user NativeUser, now time.Time) error {
	body, err := json.Marshal(user)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO participant_bindings(participant_id,source_id,upstream_user_id,snapshot,started_at) VALUES ($1,$2,$3,$4,$5)`, id, input.SourceID, input.UpstreamID, body, now)
	}
	return accountFailure(err)
}

func lockParticipantHistory(ctx context.Context, tx pgx.Tx, id string) error {
	// Old intervals can receive late captures too; serialize every binding before checking history.
	if _, err := tx.Exec(ctx, `SELECT id::text FROM participant_bindings WHERE participant_id=$1 ORDER BY id FOR UPDATE`, id); err != nil {
		return err
	}
	var history bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM usage_attributions WHERE participant_id=$1)`, id).Scan(&history); err != nil {
		return err
	}
	// shortcut: BE-008 and later financial modules must add their locked obligation guards here.
	if history {
		return auth.ErrConflict
	}
	return nil
}

func (s *Service) SaveParticipant(ctx context.Context, token string, input ParticipantUpdate) (ParticipantMutation, error) {
	creating := input.ID == ""
	if !validParticipant(input.ParticipantInput) || !creating && (!managedID(input.ID) || !validRevision(input.Revision)) || creating && input.Revision != "" {
		return ParticipantMutation{}, auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return ParticipantMutation{}, err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return ParticipantMutation{}, err
	}
	var p Participant
	if !creating {
		p, err = readParticipant(ctx, tx, input.ID, c, true, true)
		if err != nil {
			return ParticipantMutation{}, err
		}
		if p.Revision != input.Revision {
			return ParticipantMutation{}, auth.ErrConflict
		}
	}
	changing := creating || p.Binding.SourceID != input.SourceID || p.Binding.UpstreamUserID != input.UpstreamID
	var user NativeUser
	result := ParticipantMutation{}
	if changing {
		if c.SourceID == nil || *c.SourceID != input.SourceID || c.Revision != input.ConnectionRevision {
			return result, auth.ErrConflict
		}
		if !creating {
			if err = lockParticipantHistory(ctx, tx, p.ID); err != nil {
				return result, err
			}
		}
		kind, secret, err := s.decrypt(c)
		if err != nil {
			return result, err
		}
		if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
			return result, err
		}
		body, check := s.upstream.get(ctx, c.BaseURL, "/api/v1/admin/users/"+input.UpstreamID, kind, secret, "users.read", true)
		result.Read = &check
		var valid bool
		user, valid = projectNativeUser(body, input.UpstreamID, secret)
		if check.State == "available" && !valid {
			*result.Read = invalid(check)
		}
		if result.Read.State != "available" {
			return result, s.finish(ctx, tx, token, true)
		}
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return result, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	action := "participant.update"
	if creating {
		id := make([]byte, 16)
		if _, err = rand.Read(id); err != nil {
			return result, err
		}
		input.ID = base64.RawURLEncoding.EncodeToString(id)
		_, err = tx.Exec(ctx, `INSERT INTO participants(id,name,contact,notes,enabled,created_at,saved_at) VALUES ($1,$2,$3,$4,$5,$6,$6)`, input.ID, input.Name, input.Contact, input.Notes, input.Enabled, now)
		action = "participant.create"
	} else {
		_, err = tx.Exec(ctx, `UPDATE participants SET name=$2,contact=$3,notes=$4,enabled=$5,revision=revision+1,saved_at=$6 WHERE id=$1`, input.ID, input.Name, input.Contact, input.Notes, input.Enabled, now)
	}
	if err == nil && changing {
		if !creating {
			_, err = tx.Exec(ctx, `UPDATE participant_bindings SET ended_at=$2 WHERE participant_id=$1 AND ended_at IS NULL`, input.ID, now)
		}
		if err == nil {
			err = insertParticipantBinding(ctx, tx, input.ID, input.ParticipantInput, user, now)
		}
	}
	if err == nil {
		p, err = readParticipant(ctx, tx, input.ID, c, false, true)
	}
	if err == nil {
		err = participantEvent(ctx, tx, actor.ID, p, action)
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	if err != nil {
		return ParticipantMutation{}, err
	}
	result.Participant = &p
	return result, nil
}

func (s *Service) DeleteParticipant(ctx context.Context, token, id, revision, confirmation string) error {
	if !managedID(id) || !validRevision(revision) {
		return auth.ErrInput
	}
	tx, actor, err := s.begin(ctx, token, true)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	c, err := s.config(ctx, tx, false)
	if err != nil {
		return err
	}
	p, err := readParticipant(ctx, tx, id, c, true, true)
	if err != nil {
		return err
	}
	if p.Revision != revision {
		return auth.ErrConflict
	}
	if p.Name != confirmation {
		return auth.ErrInput
	}
	if err = lockParticipantHistory(ctx, tx, id); err != nil {
		return err
	}
	if _, err = s.auth.AuthorizeAdmin(ctx, tx, token); err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err = tx.Exec(ctx, `UPDATE participant_bindings SET ended_at=$2 WHERE participant_id=$1 AND ended_at IS NULL`, id, now)
	if err == nil {
		err = tx.QueryRow(ctx, `UPDATE participants SET enabled=false,deleted_at=$2,saved_at=$2,revision=revision+1 WHERE id=$1 RETURNING revision::text`, id, now).Scan(&p.Revision)
	}
	if err == nil {
		err = participantEvent(ctx, tx, actor.ID, p, "participant.delete")
	}
	if err == nil {
		err = s.finish(ctx, tx, token, true)
	}
	return err
}

type ParticipantBindingPage struct {
	Bindings  []ParticipantBinding `json:"bindings"`
	NextAfter *string              `json:"next_after"`
}

func (s *Service) ListParticipantBindings(ctx context.Context, token, id, after string, limit int) (ParticipantBindingPage, error) {
	if !managedID(id) || !validUsageAfter(after) || limit < 1 || limit > 100 {
		return ParticipantBindingPage{}, auth.ErrInput
	}
	tx, _, err := s.begin(ctx, token, false)
	if err != nil {
		return ParticipantBindingPage{}, err
	}
	defer tx.Rollback(ctx)
	access, err := s.auth.AuthorizeRead(ctx, tx, token, "pools")
	if err == nil {
		err = access.CheckParticipant(id)
	}
	if err != nil {
		return ParticipantBindingPage{}, err
	}
	var exists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM participants WHERE id=$1)`, id).Scan(&exists); err != nil {
		return ParticipantBindingPage{}, err
	}
	if !exists {
		return ParticipantBindingPage{}, auth.ErrNotFound
	}
	rows, err := tx.Query(ctx, `SELECT id::text,source_id,upstream_user_id::text,started_at,ended_at,snapshot FROM participant_bindings WHERE participant_id=$1 AND id>$2::bigint ORDER BY participant_bindings.id LIMIT $3`, id, after, limit+1)
	if err != nil {
		return ParticipantBindingPage{}, err
	}
	result := ParticipantBindingPage{Bindings: []ParticipantBinding{}}
	for rows.Next() {
		b := ParticipantBinding{ParticipantID: id}
		var body []byte
		if err = rows.Scan(&b.ID, &b.SourceID, &b.UpstreamUserID, &b.StartedAt, &b.EndedAt, &body); err == nil && access.AllData {
			err = json.Unmarshal(body, &b.Identity)
		}
		if err != nil {
			rows.Close()
			return ParticipantBindingPage{}, err
		}
		b.StartedAt, b.EndedAt = b.StartedAt.UTC(), utc(b.EndedAt)
		result.Bindings = append(result.Bindings, b)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return ParticipantBindingPage{}, err
	}
	if len(result.Bindings) > limit {
		result.Bindings = result.Bindings[:limit]
		result.NextAfter = &result.Bindings[limit-1].ID
	}
	if _, err = s.auth.AuthorizeRead(ctx, tx, token, "pools"); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		return ParticipantBindingPage{}, err
	}
	return result, nil
}
