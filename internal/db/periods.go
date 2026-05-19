package db

import (
	"database/sql"
	"errors"
	"strings"
)

var (
	ErrSeasonOverlap  = errors.New("season overlaps existing season")
	ErrSeasonNotFound = errors.New("season not found")
)

type Season struct {
	ID        int
	ClubID    string
	Name      string
	StartDate string // ISO date YYYY-MM-DD
	EndDate   string // ISO date YYYY-MM-DD, "" means open-ended
}

func (s *Store) ListSeasons(clubID string) ([]Season, error) {
	rows, err := s.db.Query(`
		SELECT id, club_id, name, start_date, end_date
		FROM seasons WHERE club_id = ?
		ORDER BY start_date DESC, id DESC`, clubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Season
	for rows.Next() {
		var ss Season
		var end sql.NullString
		if err := rows.Scan(&ss.ID, &ss.ClubID, &ss.Name, &ss.StartDate, &end); err != nil {
			return nil, err
		}
		if end.Valid {
			ss.EndDate = end.String
		}
		out = append(out, ss)
	}
	return out, rows.Err()
}

func (s *Store) SeasonByID(clubID string, id int) (Season, bool, error) {
	var ss Season
	var end sql.NullString
	err := s.db.QueryRow(`
		SELECT id, club_id, name, start_date, end_date
		FROM seasons WHERE club_id = ? AND id = ?`, clubID, id).
		Scan(&ss.ID, &ss.ClubID, &ss.Name, &ss.StartDate, &end)
	if errors.Is(err, sql.ErrNoRows) {
		return Season{}, false, nil
	}
	if err != nil {
		return Season{}, false, err
	}
	if end.Valid {
		ss.EndDate = end.String
	}
	return ss, true, nil
}

// SeasonForDate returns the season covering the given date for the club, if any.
func (s *Store) SeasonForDate(clubID, date string) (Season, bool, error) {
	var ss Season
	var end sql.NullString
	err := s.db.QueryRow(`
		SELECT id, club_id, name, start_date, end_date
		FROM seasons
		WHERE club_id = ? AND start_date <= ?
		  AND (end_date IS NULL OR end_date >= ?)
		ORDER BY start_date DESC, id DESC
		LIMIT 1`, clubID, date, date).
		Scan(&ss.ID, &ss.ClubID, &ss.Name, &ss.StartDate, &end)
	if errors.Is(err, sql.ErrNoRows) {
		return Season{}, false, nil
	}
	if err != nil {
		return Season{}, false, err
	}
	if end.Valid {
		ss.EndDate = end.String
	}
	return ss, true, nil
}

// LatestSeason returns the most recently started season for the club, if any.
func (s *Store) LatestSeason(clubID string) (Season, bool, error) {
	var ss Season
	var end sql.NullString
	err := s.db.QueryRow(`
		SELECT id, club_id, name, start_date, end_date
		FROM seasons WHERE club_id = ?
		ORDER BY start_date DESC, id DESC
		LIMIT 1`, clubID).
		Scan(&ss.ID, &ss.ClubID, &ss.Name, &ss.StartDate, &end)
	if errors.Is(err, sql.ErrNoRows) {
		return Season{}, false, nil
	}
	if err != nil {
		return Season{}, false, err
	}
	if end.Valid {
		ss.EndDate = end.String
	}
	return ss, true, nil
}

func (s *Store) AddSeason(clubID, name, startDate, endDate string) error {
	if err := s.checkSeasonOverlap(clubID, 0, startDate, endDate); err != nil {
		return err
	}
	end := nullableDate(endDate)
	_, err := s.db.Exec(`
		INSERT INTO seasons (club_id, name, start_date, end_date)
		VALUES (?, ?, ?, ?)`, clubID, strings.TrimSpace(name), startDate, end)
	return err
}

func (s *Store) UpdateSeason(clubID string, id int, name, startDate, endDate string) error {
	if err := s.checkSeasonOverlap(clubID, id, startDate, endDate); err != nil {
		return err
	}
	end := nullableDate(endDate)
	res, err := s.db.Exec(`
		UPDATE seasons SET name = ?, start_date = ?, end_date = ?
		WHERE club_id = ? AND id = ?`, strings.TrimSpace(name), startDate, end, clubID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSeasonNotFound
	}
	return nil
}

func (s *Store) DeleteSeason(clubID string, id int) error {
	res, err := s.db.Exec(`DELETE FROM seasons WHERE club_id = ? AND id = ?`, clubID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrSeasonNotFound
	}
	return nil
}

func nullableDate(d string) any {
	d = strings.TrimSpace(d)
	if d == "" {
		return nil
	}
	return d
}

type seasonOverlapQueryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}

// checkSeasonOverlap returns ErrSeasonOverlap if [start,end] overlaps any
// other season for the club (excluding the given id, if non-zero).
func (s *Store) checkSeasonOverlap(clubID string, excludeID int, start, end string) error {
	return checkSeasonOverlapQuery(s.db, clubID, excludeID, start, end)
}

func checkSeasonOverlapQuery(q seasonOverlapQueryer, clubID string, excludeID int, start, end string) error {
	start = strings.TrimSpace(start)
	end = strings.TrimSpace(end)
	if start == "" {
		return errors.New("start date required")
	}
	var rows *sql.Rows
	var err error
	if end == "" {
		rows, err = q.Query(`
			SELECT id FROM seasons WHERE club_id = ? AND id != ?
			  AND (end_date IS NULL OR end_date >= ?)`, clubID, excludeID, start)
	} else {
		rows, err = q.Query(`
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
