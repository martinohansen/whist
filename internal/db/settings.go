package db

import (
	"database/sql"
	"errors"
	"strings"
)

// SettingsUpdate is the fully validated shape of a club-settings save.
// Password is nil when the existing password should be kept, "" when it
// should be cleared, or a non-empty password when it should be replaced.
type SettingsUpdate struct {
	Name     string
	Emoji    string
	Rules    string
	Meldings []Melding
	Players  []PlayerUpdate
	Seasons  []SeasonUpdate
	Password *string
}

type PlayerUpdate struct {
	ID    int
	Name  string
	Emoji string
}

type SeasonUpdate struct {
	ID        int
	Name      string
	StartDate string
	EndDate   string
}

// UpdateSettings applies the full settings form atomically.
func (s *Store) UpdateSettings(clubID string, update SettingsUpdate) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`UPDATE clubs SET name = ?, emoji = ?, rules = ? WHERE id = ?`,
		update.Name, update.Emoji, update.Rules, clubID); err != nil {
		return err
	}
	if update.Password != nil {
		hash := ""
		if *update.Password != "" {
			hash = hashPassword(*update.Password)
		}
		if _, err := tx.Exec(`UPDATE clubs SET password_hash = ? WHERE id = ?`, hash, clubID); err != nil {
			return err
		}
	}

	rows, err := tx.Query(`SELECT id FROM meldings WHERE club_id = ?`, clubID)
	if err != nil {
		return err
	}
	var existingMeldingIDs []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		existingMeldingIDs = append(existingMeldingIDs, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if err := rows.Err(); err != nil {
		return err
	}

	keptMeldingIDs := make(map[int]bool, len(update.Meldings))
	for _, m := range update.Meldings {
		if m.ID != 0 {
			keptMeldingIDs[m.ID] = true
		}
	}
	for _, id := range existingMeldingIDs {
		if keptMeldingIDs[id] {
			continue
		}
		if _, err := tx.Exec(`DELETE FROM meldings WHERE club_id = ? AND id = ?`, clubID, id); err != nil {
			return err
		}
	}

	for i, m := range update.Meldings {
		t := m.Type
		if t == "" {
			t = MeldingTypeNormal
		}
		if m.ID == 0 {
			if _, err := tx.Exec(`INSERT INTO meldings (club_id, position, name, bid, points, type)
				VALUES (?, ?, ?, ?, ?, ?)`, clubID, i+1, m.Name, m.Bid, m.Points, t); err != nil {
				return err
			}
			continue
		}
		res, err := tx.Exec(`
			UPDATE meldings
			SET position = ?, name = ?, bid = ?, points = ?, type = ?
			WHERE club_id = ? AND id = ?`,
			i+1, m.Name, m.Bid, m.Points, t, clubID, m.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}

	for _, p := range update.Players {
		if p.ID == 0 {
			if _, err := tx.Exec(`INSERT INTO players (club_id, name, emoji) VALUES (?, ?, ?)`,
				clubID, p.Name, p.Emoji); err != nil {
				return err
			}
			continue
		}
		res, err := tx.Exec(`UPDATE players SET name = ?, emoji = ? WHERE club_id = ? AND id = ?`,
			p.Name, p.Emoji, clubID, p.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
	}

	for _, ss := range update.Seasons {
		if err := checkSeasonOverlapTx(tx, clubID, ss.ID, ss.StartDate, ss.EndDate); err != nil {
			return err
		}
		end := nullableDate(ss.EndDate)
		if ss.ID == 0 {
			if _, err := tx.Exec(`
				INSERT INTO seasons (club_id, name, start_date, end_date)
				VALUES (?, ?, ?, ?)`, clubID, strings.TrimSpace(ss.Name), ss.StartDate, end); err != nil {
				return err
			}
			continue
		}
		res, err := tx.Exec(`
			UPDATE seasons SET name = ?, start_date = ?, end_date = ?
			WHERE club_id = ? AND id = ?`,
			strings.TrimSpace(ss.Name), ss.StartDate, end, clubID, ss.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrSeasonNotFound
		}
	}

	return tx.Commit()
}

func checkSeasonOverlapTx(tx *sql.Tx, clubID string, excludeID int, start, end string) error {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" {
		return errors.New("start date required")
	}
	var rows *sql.Rows
	var err error
	if end == "" {
		rows, err = tx.Query(`
			SELECT id FROM seasons WHERE club_id = ? AND id != ?
			  AND (end_date IS NULL OR end_date >= ?)`, clubID, excludeID, start)
	} else {
		rows, err = tx.Query(`
			SELECT id FROM seasons WHERE club_id = ? AND id != ?
			  AND start_date <= ?
			  AND (end_date IS NULL OR end_date >= ?)`, clubID, excludeID, end, start)
	}
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return ErrSeasonOverlap
	}
	return rows.Err()
}
