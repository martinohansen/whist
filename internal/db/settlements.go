package db

import (
	"database/sql"
	"errors"
	"strings"
	"time"
)

type Settlement struct {
	ID                  int
	ClubID              string
	Type                string
	AmountCents         int
	FromGameID          int
	ThroughGameID       int
	FirstGameID         int
	FirstGamePlayedAt   time.Time
	ThroughGamePlayedAt time.Time
	CreatedAt           time.Time
	Rows                []SettlementRow
}

type SettlementRow struct {
	PlayerID    int
	PlayerName  string
	PlayerEmoji string
	Points      int
	AmountCents int
}

type SettlementPoint struct {
	PlayerID    int
	PlayerName  string
	PlayerEmoji string
	Points      int
}

func (s *Store) LatestSettlement(clubID string) (Settlement, bool, error) {
	var settlement Settlement
	err := s.db.QueryRow(`
		SELECT id, club_id, type, amount_cents, from_game_id, through_game_id, created_at
		FROM settlements
		WHERE club_id = ?
		ORDER BY id DESC
		LIMIT 1`, clubID).
		Scan(&settlement.ID, &settlement.ClubID, &settlement.Type, &settlement.AmountCents,
			&settlement.FromGameID, &settlement.ThroughGameID, &settlement.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Settlement{}, false, nil
	}
	if err != nil {
		return Settlement{}, false, err
	}
	return settlement, true, nil
}

// SettlementGamesSince returns unsettled games in registration order.
func (s *Store) SettlementGamesSince(clubID string, afterGameID int) ([]Game, error) {
	rows, err := s.db.Query(`
		SELECT id, club_id, played_at, melding_id, melding_name, melding_type, bid, melding_points, note, created_at
		FROM games
		WHERE club_id = ? AND id > ?
		ORDER BY id`, clubID, afterGameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var games []Game
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.ClubID, &g.PlayedAt, &g.MeldingID, &g.MeldingName, &g.MeldingType,
			&g.Bid, &g.MeldingPoints, &g.Note, &g.CreatedAt); err != nil {
			return nil, err
		}
		games = append(games, g)
	}
	return games, rows.Err()
}

// SettlementPointsBetween returns the period totals for all players who
// participated in the selected game-id interval.
func (s *Store) SettlementPointsBetween(clubID string, afterGameID, throughGameID int) ([]SettlementPoint, error) {
	rows, err := s.db.Query(`
		SELECT p.id, p.name, p.emoji, SUM(gs.score) AS points
		FROM games g
		JOIN game_scores gs ON gs.game_id = g.id
		JOIN players p ON p.id = gs.player_id
		WHERE g.club_id = ? AND g.id > ? AND g.id <= ?
		GROUP BY p.id
		ORDER BY points DESC, p.name COLLATE NOCASE, p.id`, clubID, afterGameID, throughGameID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SettlementPoint
	for rows.Next() {
		var point SettlementPoint
		if err := rows.Scan(&point.PlayerID, &point.PlayerName, &point.PlayerEmoji, &point.Points); err != nil {
			return nil, err
		}
		out = append(out, point)
	}
	return out, rows.Err()
}

func (s *Store) AddSettlement(clubID string, settlement Settlement) (Settlement, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return Settlement{}, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(`
		INSERT INTO settlements (club_id, type, amount_cents, from_game_id, through_game_id)
		VALUES (?, ?, ?, ?, ?)`,
		clubID, settlement.Type, settlement.AmountCents, settlement.FromGameID, settlement.ThroughGameID)
	if err != nil {
		return Settlement{}, err
	}
	id64, err := res.LastInsertId()
	if err != nil {
		return Settlement{}, err
	}
	settlement.ID = int(id64)
	settlement.ClubID = clubID

	for _, row := range settlement.Rows {
		if _, err := tx.Exec(`
			INSERT INTO settlement_rows
				(settlement_id, player_id, player_name, player_emoji, points, amount_cents)
			VALUES (?, ?, ?, ?, ?, ?)`,
			settlement.ID, row.PlayerID, row.PlayerName, row.PlayerEmoji, row.Points, row.AmountCents); err != nil {
			return Settlement{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Settlement{}, err
	}
	return settlement, nil
}

func (s *Store) ListSettlements(clubID string) ([]Settlement, error) {
	rows, err := s.db.Query(`
		SELECT s.id, s.club_id, s.type, s.amount_cents, s.from_game_id, s.through_game_id,
			COALESCE((
				SELECT g.id FROM games g
				WHERE g.club_id = s.club_id
				  AND g.id > s.from_game_id
				  AND g.id <= s.through_game_id
				ORDER BY g.id
				LIMIT 1
			), 0) AS first_game_id,
			(
				SELECT g.played_at FROM games g
				WHERE g.club_id = s.club_id
				  AND g.id > s.from_game_id
				  AND g.id <= s.through_game_id
				ORDER BY g.id
				LIMIT 1
			) AS first_game_played_at,
			tg.played_at AS through_game_played_at,
			s.created_at
		FROM settlements s
		LEFT JOIN games tg ON tg.club_id = s.club_id AND tg.id = s.through_game_id
		WHERE s.club_id = ?
		ORDER BY s.id DESC`, clubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var settlements []Settlement
	for rows.Next() {
		var settlement Settlement
		var firstPlayedAt, throughPlayedAt sql.NullTime
		if err := rows.Scan(&settlement.ID, &settlement.ClubID, &settlement.Type, &settlement.AmountCents,
			&settlement.FromGameID, &settlement.ThroughGameID, &settlement.FirstGameID,
			&firstPlayedAt, &throughPlayedAt, &settlement.CreatedAt); err != nil {
			return nil, err
		}
		if firstPlayedAt.Valid {
			settlement.FirstGamePlayedAt = firstPlayedAt.Time
		}
		if throughPlayedAt.Valid {
			settlement.ThroughGamePlayedAt = throughPlayedAt.Time
		}
		settlements = append(settlements, settlement)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(settlements) == 0 {
		return settlements, nil
	}
	placeholders := make([]string, len(settlements))
	args := make([]any, len(settlements))
	for i, settlement := range settlements {
		placeholders[i] = "?"
		args[i] = settlement.ID
	}
	rowQuery := `
		SELECT settlement_id, player_id, player_name, player_emoji, points, amount_cents
		FROM settlement_rows
		WHERE settlement_id IN (` + strings.Join(placeholders, ",") + `)
		ORDER BY settlement_id, points DESC, player_name COLLATE NOCASE, player_id`
	rows, err = s.db.Query(rowQuery, args...)
	if err != nil {
		return nil, err
	}
	rowsBySettlement := make(map[int][]SettlementRow, len(settlements))
	for rows.Next() {
		var settlementID int
		var row SettlementRow
		if err := rows.Scan(&settlementID, &row.PlayerID, &row.PlayerName, &row.PlayerEmoji, &row.Points, &row.AmountCents); err != nil {
			rows.Close()
			return nil, err
		}
		rowsBySettlement[settlementID] = append(rowsBySettlement[settlementID], row)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	for i := range settlements {
		settlements[i].Rows = rowsBySettlement[settlements[i].ID]
	}
	return settlements, nil
}
