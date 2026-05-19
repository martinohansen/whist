package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/martinohansen/whist/internal/game"
)

const (
	DraftStatusPending  = "pending"
	DraftStatusApproved = "approved"
)

// Draft is a proposed game extracted from a paper photo, awaiting review.
type Draft struct {
	ID          int
	ClubID      string
	BatchID     string
	PlayedAt    time.Time // zero if unknown
	MeldingID   int       // 0 if not matched
	MeldingName string    // raw name from OCR, kept for display
	Note        string
	Status      string
	CreatedAt   time.Time
	Scores      []DraftScore // ordered by position
}

// DraftScore is a single player entry on a draft. PlayerID is 0 when the
// extracted name didn't match any existing player; RawName is always set.
type DraftScore struct {
	Position int
	PlayerID int
	RawName  string
	Role     string
	Tricks   int
}

// AddDrafts inserts a batch of drafts in one transaction. Each draft's
// initial state is snapshotted so ApproveDrafts can detect user edits.
func (s *Store) AddDrafts(clubID, batchID string, drafts []Draft) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	for _, d := range drafts {
		var playedAt any
		if !d.PlayedAt.IsZero() {
			playedAt = d.PlayedAt
		}
		var meldingID any
		if d.MeldingID > 0 {
			meldingID = d.MeldingID
		}
		status := d.Status
		if status == "" {
			status = DraftStatusPending
		}
		snapshot := draftFingerprint(d)
		res, err := tx.Exec(`INSERT INTO game_drafts
				(club_id, batch_id, played_at, melding_id, melding_name, note, status, original_snapshot)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			clubID, batchID, playedAt, meldingID, d.MeldingName, d.Note, status, snapshot)
		if err != nil {
			return err
		}
		id64, _ := res.LastInsertId()
		draftID := int(id64)
		for _, sc := range d.Scores {
			var playerID any
			if sc.PlayerID > 0 {
				playerID = sc.PlayerID
			}
			if _, err := tx.Exec(`INSERT INTO game_draft_scores
					(draft_id, position, player_id, raw_name, role, tricks)
				VALUES (?, ?, ?, ?, ?, ?)`,
				draftID, sc.Position, playerID, sc.RawName, sc.Role, sc.Tricks); err != nil {
				return err
			}
		}
	}
	return tx.Commit()
}

// ImportBatch is the model inputs/outputs for a single import session: the
// OCR'd markdown that fed the extractor and the raw JSON list of games it
// produced. Kept so we can replay or debug user-adjusted approvals.
type ImportBatch struct {
	BatchID       string
	ClubID        string
	Markdown      string
	ExtractedJSON string
	CreatedAt     time.Time
}

// AddImportBatch records the OCR'd markdown and the raw model extraction
// for a batch. Safe to call before AddDrafts.
func (s *Store) AddImportBatch(clubID, batchID, markdown, extractedJSON string) error {
	_, err := s.db.Exec(`INSERT OR REPLACE INTO import_batches
			(batch_id, club_id, markdown, extracted_json)
		VALUES (?, ?, ?, ?)`, batchID, clubID, markdown, extractedJSON)
	return err
}

// GetImportBatch returns the stored markdown/extracted JSON for a batch.
func (s *Store) GetImportBatch(batchID string) (ImportBatch, error) {
	var b ImportBatch
	err := s.db.QueryRow(`SELECT batch_id, club_id, markdown, extracted_json, created_at
		FROM import_batches WHERE batch_id = ?`, batchID).
		Scan(&b.BatchID, &b.ClubID, &b.Markdown, &b.ExtractedJSON, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ImportBatch{}, ErrNotFound
	}
	return b, err
}

// draftFingerprint produces a canonical JSON representation of the
// user-visible fields of a draft. Two drafts that produce the same
// fingerprint are considered equivalent for the "was this edited?" check.
func draftFingerprint(d Draft) string {
	type scoreFP struct {
		Position int    `json:"position"`
		PlayerID int    `json:"player_id"`
		RawName  string `json:"raw_name"`
		Role     string `json:"role"`
		Tricks   int    `json:"tricks"`
	}
	type fp struct {
		PlayedAt    string    `json:"played_at"`
		MeldingID   int       `json:"melding_id"`
		MeldingName string    `json:"melding_name"`
		Note        string    `json:"note"`
		Scores      []scoreFP `json:"scores"`
	}
	out := fp{
		MeldingID:   d.MeldingID,
		MeldingName: strings.TrimSpace(d.MeldingName),
		Note:        strings.TrimSpace(d.Note),
		Scores:      make([]scoreFP, 0, len(d.Scores)),
	}
	if !d.PlayedAt.IsZero() {
		out.PlayedAt = d.PlayedAt.UTC().Format("2006-01-02")
	}
	scores := make([]DraftScore, len(d.Scores))
	copy(scores, d.Scores)
	sort.Slice(scores, func(i, j int) bool { return scores[i].Position < scores[j].Position })
	for _, sc := range scores {
		out.Scores = append(out.Scores, scoreFP{
			Position: sc.Position,
			PlayerID: sc.PlayerID,
			RawName:  strings.TrimSpace(sc.RawName),
			Role:     sc.Role,
			Tricks:   sc.Tricks,
		})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// ListPendingDrafts returns all pending drafts for a club, with scores attached.
func (s *Store) ListPendingDrafts(clubID string) ([]Draft, error) {
	rows, err := s.db.Query(`
		SELECT id, club_id, batch_id, played_at, COALESCE(melding_id, 0),
			melding_name, note, status, created_at
		FROM game_drafts
		WHERE club_id = ? AND status = ?
		ORDER BY created_at, id`, clubID, DraftStatusPending)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var drafts []Draft
	var ids []int
	for rows.Next() {
		var d Draft
		var playedAt sql.NullTime
		if err := rows.Scan(&d.ID, &d.ClubID, &d.BatchID, &playedAt, &d.MeldingID,
			&d.MeldingName, &d.Note, &d.Status, &d.CreatedAt); err != nil {
			return nil, err
		}
		if playedAt.Valid {
			d.PlayedAt = playedAt.Time
		}
		drafts = append(drafts, d)
		ids = append(ids, d.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(drafts) == 0 {
		return drafts, nil
	}
	scores, err := s.loadDraftScores(ids)
	if err != nil {
		return nil, err
	}
	for i, d := range drafts {
		d.Scores = scores[d.ID]
		drafts[i] = d
	}
	return drafts, nil
}

// GetDraft returns a single draft with scores.
func (s *Store) GetDraft(clubID string, id int) (Draft, error) {
	var d Draft
	var playedAt sql.NullTime
	err := s.db.QueryRow(`
		SELECT id, club_id, batch_id, played_at, COALESCE(melding_id, 0),
			melding_name, note, status, created_at
		FROM game_drafts WHERE club_id = ? AND id = ?`, clubID, id).
		Scan(&d.ID, &d.ClubID, &d.BatchID, &playedAt, &d.MeldingID,
			&d.MeldingName, &d.Note, &d.Status, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Draft{}, ErrNotFound
	}
	if err != nil {
		return Draft{}, err
	}
	if playedAt.Valid {
		d.PlayedAt = playedAt.Time
	}
	scores, err := s.loadDraftScores([]int{d.ID})
	if err != nil {
		return Draft{}, err
	}
	d.Scores = scores[d.ID]
	return d, nil
}

// UpdateDraft replaces a draft's fields and scores in a single transaction.
func (s *Store) UpdateDraft(clubID string, id int, playedAt time.Time, meldingID int, note string, scores []DraftScore) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var playedAtArg any
	if !playedAt.IsZero() {
		playedAtArg = playedAt
	}
	var meldingArg any
	if meldingID > 0 {
		meldingArg = meldingID
	}
	res, err := tx.Exec(`UPDATE game_drafts
		SET played_at = ?, melding_id = ?, note = ?
		WHERE club_id = ? AND id = ? AND status = ?`,
		playedAtArg, meldingArg, note, clubID, id, DraftStatusPending)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM game_draft_scores WHERE draft_id = ?`, id); err != nil {
		return err
	}
	for _, sc := range scores {
		var playerID any
		if sc.PlayerID > 0 {
			playerID = sc.PlayerID
		}
		if _, err := tx.Exec(`INSERT INTO game_draft_scores
				(draft_id, position, player_id, raw_name, role, tricks)
			VALUES (?, ?, ?, ?, ?, ?)`,
			id, sc.Position, playerID, sc.RawName, sc.Role, sc.Tricks); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// RejectPendingDrafts removes all of a club's pending drafts.
func (s *Store) RejectPendingDrafts(clubID string) error {
	_, err := s.db.Exec(`DELETE FROM game_drafts WHERE club_id = ? AND status = ?`,
		clubID, DraftStatusPending)
	return err
}

// DeleteDraft removes a pending draft and its scores.
func (s *Store) DeleteDraft(clubID string, id int) error {
	res, err := s.db.Exec(`DELETE FROM game_drafts WHERE club_id = ? AND id = ?`, clubID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DraftEdit describes a draft whose state at approval time diverged from
// the original AI-extracted snapshot recorded in AddDrafts.
type DraftEdit struct {
	DraftID    int
	BatchID    string
	Original   string // canonical-JSON snapshot from import time
	Approved   string // canonical-JSON snapshot at approval time
}

// ApproveDrafts converts all pending drafts for a club into real games inside
// a single transaction. Drafts that are invalid (missing melding, unlinked
// player, wrong player count, tricks ≠ 13 in normal mode) are skipped and
// returned as a separate slice; valid ones become games and their drafts are
// marked approved. edits reports approved drafts whose current state
// differs from the AI-extracted snapshot.
func (s *Store) ApproveDrafts(clubID string) (created int, skipped []int, edits []DraftEdit, err error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, nil, nil, err
	}
	defer tx.Rollback()

	// Load pending drafts inside the tx for consistency.
	rows, err := tx.Query(`
		SELECT id, batch_id, COALESCE(melding_id, 0), melding_name,
			COALESCE(played_at, ''), note, COALESCE(original_snapshot, '')
		FROM game_drafts
		WHERE club_id = ? AND status = ?
		ORDER BY created_at, id`, clubID, DraftStatusPending)
	if err != nil {
		return 0, nil, nil, err
	}
	type pend struct {
		id          int
		batchID     string
		meldingID   int
		meldingName string
		playedAt    sql.NullString
		note        string
		snapshot    string
	}
	var pendings []pend
	for rows.Next() {
		var p pend
		if err := rows.Scan(&p.id, &p.batchID, &p.meldingID, &p.meldingName, &p.playedAt, &p.note, &p.snapshot); err != nil {
			rows.Close()
			return 0, nil, nil, err
		}
		pendings = append(pendings, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, nil, nil, err
	}

	for _, p := range pendings {
		if p.meldingID == 0 {
			skipped = append(skipped, p.id)
			continue
		}
		// Fetch melding inside tx.
		var m Melding
		if err := tx.QueryRow(`SELECT id, club_id, position, name, bid, points, type
			FROM meldings WHERE club_id = ? AND id = ?`, clubID, p.meldingID).
			Scan(&m.ID, &m.ClubID, &m.Position, &m.Name, &m.Bid, &m.Points, &m.Type); err != nil {
			skipped = append(skipped, p.id)
			continue
		}

		// Fetch scores.
		sRows, err := tx.Query(`
			SELECT position, COALESCE(player_id, 0), raw_name, role, tricks
			FROM game_draft_scores WHERE draft_id = ? ORDER BY position`, p.id)
		if err != nil {
			return 0, nil, nil, err
		}
		var entries []game.PlayerEntry
		var dscores []DraftScore
		bad := false
		for sRows.Next() {
			var sc DraftScore
			if err := sRows.Scan(&sc.Position, &sc.PlayerID, &sc.RawName, &sc.Role, &sc.Tricks); err != nil {
				sRows.Close()
				return 0, nil, nil, err
			}
			dscores = append(dscores, sc)
			if sc.PlayerID == 0 || sc.Tricks < 0 || sc.Tricks > 13 {
				bad = true
				continue
			}
			switch sc.Role {
			case game.RoleMelder, game.RoleMakker, game.RoleModspil:
			default:
				bad = true
			}
			entries = append(entries, game.PlayerEntry{PlayerID: sc.PlayerID, Role: sc.Role, Tricks: sc.Tricks})
		}
		sRows.Close()
		if bad || len(game.ValidateEntries(m.Type, entries)) > 0 {
			skipped = append(skipped, p.id)
			continue
		}

		playedAt := time.Now()
		if p.playedAt.Valid && p.playedAt.String != "" {
			if t, err := time.Parse(time.RFC3339, p.playedAt.String); err == nil {
				playedAt = t
			} else if t, err := time.Parse("2006-01-02 15:04:05-07:00", p.playedAt.String); err == nil {
				playedAt = t
			} else if t, err := time.Parse("2006-01-02 15:04:05", p.playedAt.String); err == nil {
				playedAt = t
			}
		}

		if err := insertGameTx(tx, clubID, playedAt, m, entries, p.note); err != nil {
			return 0, nil, nil, err
		}
		if _, err := tx.Exec(`UPDATE game_drafts SET status = ? WHERE id = ?`,
			DraftStatusApproved, p.id); err != nil {
			return 0, nil, nil, err
		}
		created++

		// Compare approved state against the original snapshot. Skip when
		// no snapshot was recorded (drafts inserted before this column existed).
		if p.snapshot == "" {
			continue
		}
		current := draftFingerprint(Draft{
			PlayedAt:    playedAt,
			MeldingID:   p.meldingID,
			MeldingName: p.meldingName,
			Note:        p.note,
			Scores:      dscores,
		})
		if current != p.snapshot {
			edits = append(edits, DraftEdit{
				DraftID:  p.id,
				BatchID:  p.batchID,
				Original: p.snapshot,
				Approved: current,
			})
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, nil, nil, err
	}
	return created, skipped, edits, nil
}

// insertGameTx mirrors AddGame but runs inside an existing transaction so we
// can batch many drafts into a single commit.
func insertGameTx(tx *sql.Tx, clubID string, playedAt time.Time, m Melding, scores []game.PlayerEntry, note string) error {
	mtype := m.Type
	if mtype == "" {
		mtype = MeldingTypeNormal
	}
	res, err := tx.Exec(`INSERT INTO games (club_id, played_at, melding_id, melding_name, melding_type, bid, melding_points, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, clubID, playedAt, m.ID, m.Name, mtype, m.Bid, m.Points, note)
	if err != nil {
		return err
	}
	id64, _ := res.LastInsertId()
	gameID := int(id64)

	return replaceGameScoresTx(tx, clubID, gameID, mtype, m.Bid, m.Points, scores)
}

func (s *Store) loadDraftScores(draftIDs []int) (map[int][]DraftScore, error) {
	if len(draftIDs) == 0 {
		return map[int][]DraftScore{}, nil
	}
	placeholders := make([]string, len(draftIDs))
	args := make([]any, len(draftIDs))
	for i, id := range draftIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `
		SELECT draft_id, position, COALESCE(player_id, 0), raw_name, role, tricks
		FROM game_draft_scores
		WHERE draft_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY draft_id, position`
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int][]DraftScore, len(draftIDs))
	for rows.Next() {
		var did int
		var sc DraftScore
		if err := rows.Scan(&did, &sc.Position, &sc.PlayerID, &sc.RawName, &sc.Role, &sc.Tricks); err != nil {
			return nil, err
		}
		out[did] = append(out[did], sc)
	}
	return out, rows.Err()
}
