package db

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/martinohansen/whist/internal/game"
	_ "github.com/mattn/go-sqlite3"
)

type Store struct {
	db *sql.DB
}

type Club struct {
	ID                           string
	Name                         string
	Emoji                        string
	Rules                        string
	DefaultSettlementType        string
	DefaultSettlementAmountCents int
	CommonDebtEqualPercent       int
	PasswordOn                   bool // true if password_hash is set
	CreatedAt                    time.Time
}

type Player struct {
	ID        int
	ClubID    string
	Name      string
	Emoji     string
	Games     int
	Points    int
	Wins      int // games where this player ended with positive score
	Losses    int // games where this player ended with negative score
	Meldings  int // games as melder where the bid was made (score >= 0)
	CreatedAt time.Time
}

type Melding struct {
	ID       int
	ClubID   string
	Position int
	Name     string
	Bid      int    // for "normal": tricks required; for "nolo": max tricks allowed
	Points   int    // base points per level for melder/makker (or per opponent for nolo)
	Type     string // "normal" or "nolo"
}

const (
	MeldingTypeNormal = "normal"
	MeldingTypeNolo   = "nolo"
)

type PlayerScore struct {
	Player Player
	Role   string // melder, makker, modspil
	Tricks int
	Score  int
}

type Game struct {
	ID            int
	ClubID        string
	PlayedAt      time.Time
	MeldingID     int
	MeldingName   string
	MeldingType   string
	Bid           int
	MeldingPoints int
	Note          string
	CreatedAt     time.Time
	Scores        []PlayerScore // ordered by score desc, then name
}

var ErrNotFound = errors.New("not found")

func Open(path string) (*Store, error) {
	database, err := sql.Open("sqlite3", path+"?_foreign_keys=on")
	if err != nil {
		return nil, err
	}
	if err := ensureSchema(database); err != nil {
		database.Close()
		return nil, err
	}
	return &Store{db: database}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func ensureSchema(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS clubs (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	emoji TEXT NOT NULL DEFAULT '🃏',
	rules TEXT NOT NULL DEFAULT '',
	default_settlement_type TEXT NOT NULL DEFAULT 'common_debt',
	default_settlement_amount_cents INTEGER NOT NULL DEFAULT 40000,
	common_debt_equal_percent INTEGER NOT NULL DEFAULT 50,
	password_hash TEXT NOT NULL DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS clubs_name_idx ON clubs(name COLLATE NOCASE);

CREATE TABLE IF NOT EXISTS players (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	club_id TEXT NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	emoji TEXT NOT NULL DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
	UNIQUE(club_id, name)
);

CREATE INDEX IF NOT EXISTS players_club_idx ON players(club_id);

CREATE TABLE IF NOT EXISTS meldings (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	club_id TEXT NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	name TEXT NOT NULL,
	bid INTEGER NOT NULL,
	points INTEGER NOT NULL,
	type TEXT NOT NULL DEFAULT 'normal',
	UNIQUE(club_id, name)
);

CREATE INDEX IF NOT EXISTS meldings_club_idx ON meldings(club_id, position);

CREATE TABLE IF NOT EXISTS games (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	club_id TEXT NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
	played_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
	melding_id INTEGER NOT NULL REFERENCES meldings(id),
	melding_name TEXT NOT NULL DEFAULT '',
	melding_type TEXT NOT NULL DEFAULT 'normal',
	bid INTEGER NOT NULL DEFAULT 0,
	melding_points INTEGER NOT NULL DEFAULT 0,
	note TEXT NOT NULL DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS games_club_idx ON games(club_id, played_at DESC);

CREATE TABLE IF NOT EXISTS game_scores (
	game_id INTEGER NOT NULL REFERENCES games(id) ON DELETE CASCADE,
	player_id INTEGER NOT NULL REFERENCES players(id) ON DELETE CASCADE,
	role TEXT NOT NULL DEFAULT 'modspil',
	tricks INTEGER NOT NULL DEFAULT 0,
	score INTEGER NOT NULL,
	PRIMARY KEY (game_id, player_id)
);

CREATE INDEX IF NOT EXISTS game_scores_player_idx ON game_scores(player_id);

CREATE TABLE IF NOT EXISTS settlements (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	club_id TEXT NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
	type TEXT NOT NULL,
	amount_cents INTEGER NOT NULL,
	from_game_id INTEGER NOT NULL,
	through_game_id INTEGER NOT NULL,
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS settlements_club_idx ON settlements(club_id, id DESC);

CREATE TABLE IF NOT EXISTS settlement_rows (
	settlement_id INTEGER NOT NULL REFERENCES settlements(id) ON DELETE CASCADE,
	player_id INTEGER NOT NULL,
	player_name TEXT NOT NULL,
	player_emoji TEXT NOT NULL DEFAULT '',
	points INTEGER NOT NULL,
	amount_cents INTEGER NOT NULL,
	PRIMARY KEY (settlement_id, player_id)
);

CREATE TABLE IF NOT EXISTS seasons (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	club_id TEXT NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
	name TEXT NOT NULL,
	start_date TEXT NOT NULL,
	end_date TEXT
);

CREATE INDEX IF NOT EXISTS seasons_club_idx ON seasons(club_id, start_date DESC);

CREATE TABLE IF NOT EXISTS game_drafts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	club_id TEXT NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
	batch_id TEXT NOT NULL,
	played_at DATETIME,
	melding_id INTEGER REFERENCES meldings(id) ON DELETE SET NULL,
	melding_name TEXT NOT NULL DEFAULT '',
	note TEXT NOT NULL DEFAULT '',
	status TEXT NOT NULL DEFAULT 'pending',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS game_drafts_club_idx ON game_drafts(club_id, status, created_at);

CREATE TABLE IF NOT EXISTS game_draft_scores (
	draft_id INTEGER NOT NULL REFERENCES game_drafts(id) ON DELETE CASCADE,
	position INTEGER NOT NULL,
	player_id INTEGER REFERENCES players(id) ON DELETE SET NULL,
	raw_name TEXT NOT NULL DEFAULT '',
	role TEXT NOT NULL DEFAULT 'modspil',
	tricks INTEGER NOT NULL DEFAULT 0,
	PRIMARY KEY (draft_id, position)
);

CREATE TABLE IF NOT EXISTS import_batches (
	batch_id TEXT PRIMARY KEY,
	club_id TEXT NOT NULL REFERENCES clubs(id) ON DELETE CASCADE,
	markdown TEXT NOT NULL DEFAULT '',
	extracted_json TEXT NOT NULL DEFAULT '',
	created_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
`
	if _, err := db.Exec(schema); err != nil {
		return err
	}
	if err := ensureClubColumns(db); err != nil {
		return err
	}
	if err := ensureDraftColumns(db); err != nil {
		return err
	}
	return nil
}

func ensureDraftColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(game_drafts)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	hasOriginalSnapshot := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "original_snapshot" {
			hasOriginalSnapshot = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasOriginalSnapshot {
		if _, err := db.Exec(`ALTER TABLE game_drafts ADD COLUMN original_snapshot TEXT NOT NULL DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

func ensureClubColumns(db *sql.DB) error {
	rows, err := db.Query(`PRAGMA table_info(clubs)`)
	if err != nil {
		return err
	}
	defer rows.Close()
	hasDefaultSettlementType := false
	hasDefaultSettlementAmountCents := false
	hasCommonDebtEqualPercent := false
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == "common_debt_equal_percent" {
			hasCommonDebtEqualPercent = true
		}
		if name == "default_settlement_type" {
			hasDefaultSettlementType = true
		}
		if name == "default_settlement_amount_cents" {
			hasDefaultSettlementAmountCents = true
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !hasDefaultSettlementType {
		if _, err := db.Exec(`ALTER TABLE clubs ADD COLUMN default_settlement_type TEXT NOT NULL DEFAULT 'common_debt'`); err != nil {
			return err
		}
	}
	if !hasDefaultSettlementAmountCents {
		if _, err := db.Exec(`ALTER TABLE clubs ADD COLUMN default_settlement_amount_cents INTEGER NOT NULL DEFAULT 40000`); err != nil {
			return err
		}
	}
	if !hasCommonDebtEqualPercent {
		if _, err := db.Exec(`ALTER TABLE clubs ADD COLUMN common_debt_equal_percent INTEGER NOT NULL DEFAULT 50`); err != nil {
			return err
		}
	}
	return nil
}

// --- Passwords -------------------------------------------------------------

func hashPassword(password string) string {
	var salt [16]byte
	rand.Read(salt[:])
	h := sha256.Sum256(append(salt[:], []byte(password)...))
	return hex.EncodeToString(salt[:]) + ":" + hex.EncodeToString(h[:])
}

func verifyPassword(stored, password string) bool {
	if stored == "" {
		return false
	}
	parts := strings.SplitN(stored, ":", 2)
	if len(parts) != 2 {
		return false
	}
	salt, err := hex.DecodeString(parts[0])
	if err != nil {
		return false
	}
	want, err := hex.DecodeString(parts[1])
	if err != nil {
		return false
	}
	got := sha256.Sum256(append(salt, []byte(password)...))
	return subtle.ConstantTimeCompare(want, got[:]) == 1
}

// --- Clubs -----------------------------------------------------------------

// CreateClub creates a new private club. Passwords are set later.
func (s *Store) CreateClub(name string) (Club, error) {
	id, err := NewID()
	if err != nil {
		return Club{}, err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return Club{}, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO clubs (id, name, emoji) VALUES (?, ?, ?)`,
		id, name, emoji(name)); err != nil {
		return Club{}, err
	}
	if err := seedDefaultMeldingsTx(tx, id); err != nil {
		return Club{}, err
	}
	if err := tx.Commit(); err != nil {
		return Club{}, err
	}
	return s.GetClub(id)
}

func (s *Store) GetClub(id string) (Club, error) {
	var c Club
	var hash string
	err := s.db.QueryRow(`
		SELECT id, name, emoji, rules, default_settlement_type, default_settlement_amount_cents,
			common_debt_equal_percent, password_hash, created_at
		FROM clubs WHERE id = ?`, id).
		Scan(&c.ID, &c.Name, &c.Emoji, &c.Rules, &c.DefaultSettlementType, &c.DefaultSettlementAmountCents,
			&c.CommonDebtEqualPercent, &hash, &c.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Club{}, ErrNotFound
	}
	c.PasswordOn = hash != ""
	return c, err
}

// UpdateClub updates the club's name, emoji and rules.
func (s *Store) UpdateClub(id, name, emoji, rules string) error {
	_, err := s.db.Exec(`UPDATE clubs SET name = ?, emoji = ?, rules = ? WHERE id = ?`,
		name, emoji, rules, id)
	return err
}

// VerifyClubPassword checks the supplied password against the club's stored hash.
func (s *Store) VerifyClubPassword(id, password string) (bool, error) {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM clubs WHERE id = ?`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	if hash == "" {
		// No password set — anyone with the URL passes.
		return true, nil
	}
	return verifyPassword(hash, password), nil
}

// ClubPasswordHash returns the raw stored hash. Used by handlers to derive
// signed unlock cookies; "" means no password set.
func (s *Store) ClubPasswordHash(id string) (string, error) {
	var hash string
	err := s.db.QueryRow(`SELECT password_hash FROM clubs WHERE id = ?`, id).Scan(&hash)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	return hash, err
}

func (s *Store) SetClubPassword(id, password string) error {
	hash := ""
	if password != "" {
		hash = hashPassword(password)
	}
	_, err := s.db.Exec(`UPDATE clubs SET password_hash = ? WHERE id = ?`, hash, id)
	return err
}

// SearchClubs returns name-matching clubs that have a password set. The
// query is matched case-insensitively as a substring.
func (s *Store) SearchClubs(query string) ([]Club, error) {
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	rows, err := s.db.Query(`
		SELECT id, name, emoji, rules, default_settlement_type, default_settlement_amount_cents,
			common_debt_equal_percent, password_hash, created_at
		FROM clubs
		WHERE password_hash != ''
		  AND name LIKE ? COLLATE NOCASE
		ORDER BY name COLLATE NOCASE
		LIMIT 50`, "%"+q+"%")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Club
	for rows.Next() {
		var c Club
		var hash string
		if err := rows.Scan(&c.ID, &c.Name, &c.Emoji, &c.Rules, &c.DefaultSettlementType, &c.DefaultSettlementAmountCents,
			&c.CommonDebtEqualPercent, &hash, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.PasswordOn = hash != ""
		out = append(out, c)
	}
	return out, rows.Err()
}

// --- Players ---------------------------------------------------------------

func (s *Store) AddPlayer(clubID, name string) (Player, error) {
	res, err := s.db.Exec(`INSERT INTO players (club_id, name, emoji) VALUES (?, ?, ?)`,
		clubID, name, emoji(name))
	if err != nil {
		return Player{}, err
	}
	id, _ := res.LastInsertId()
	return s.GetPlayer(clubID, int(id))
}

// UpdatePlayer updates a player's name and emoji.
func (s *Store) UpdatePlayer(clubID string, id int, name, e string) error {
	res, err := s.db.Exec(`UPDATE players SET name = ?, emoji = ? WHERE club_id = ? AND id = ?`,
		name, e, clubID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ErrPlayerHasGames is returned when DeletePlayer is called on a player who
// has participated in games. Hard-deleting would destroy history via cascade.
var ErrPlayerHasGames = errors.New("player has games")

// DeletePlayer removes a player. It refuses to delete a player who has
// participated in any game, to preserve game history.
func (s *Store) DeletePlayer(clubID string, id int) error {
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM game_scores WHERE player_id = ?`, id).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return ErrPlayerHasGames
	}
	res, err := s.db.Exec(`DELETE FROM players WHERE club_id = ? AND id = ?`, clubID, id)
	if err != nil {
		return err
	}
	if rows, _ := res.RowsAffected(); rows == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) GetPlayer(clubID string, id int) (Player, error) {
	var p Player
	err := s.db.QueryRow(`
		SELECT id, club_id, name, emoji, created_at
		FROM players WHERE club_id = ? AND id = ?`, clubID, id).
		Scan(&p.ID, &p.ClubID, &p.Name, &p.Emoji, &p.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Player{}, ErrNotFound
	}
	return p, err
}

func (s *Store) ListPlayers(clubID string) ([]Player, error) {
	rows, err := s.db.Query(`
		SELECT id, club_id, name, emoji, created_at
		FROM players WHERE club_id = ?
		ORDER BY name COLLATE NOCASE`, clubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Player
	for rows.Next() {
		var p Player
		if err := rows.Scan(&p.ID, &p.ClubID, &p.Name, &p.Emoji, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) PlayersByIDs(clubID string, ids []int) ([]Player, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, clubID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	q := fmt.Sprintf(`
		SELECT id, club_id, name, emoji, created_at
		FROM players WHERE club_id = ? AND id IN (%s)
		ORDER BY name COLLATE NOCASE`, strings.Join(placeholders, ","))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Player
	for rows.Next() {
		var p Player
		if err := rows.Scan(&p.ID, &p.ClubID, &p.Name, &p.Emoji, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LeaderboardFilter narrows which games count toward leaderboard totals.
// Zero values mean "no bound".
type LeaderboardFilter struct {
	// Only games with played_at < Before are counted (used for rank deltas).
	Before time.Time
	// Only games with played_at >= After are counted (used for seasons).
	After time.Time
	// Only games with played_at <= Until are counted (used for seasons).
	Until time.Time
}

func (s *Store) Leaderboard(clubID string) ([]Player, error) {
	return s.LeaderboardFiltered(clubID, LeaderboardFilter{})
}

// LeaderboardFiltered restricts the games counted to those matching the filter.
func (s *Store) LeaderboardFiltered(clubID string, f LeaderboardFilter) ([]Player, error) {
	var args []any
	conds := []string{"gs.player_id = p.id"}
	if !f.Before.IsZero() {
		conds = append(conds, "gs.game_id IN (SELECT id FROM games WHERE played_at < ?)")
		args = append(args, f.Before)
	}
	if !f.After.IsZero() {
		conds = append(conds, "gs.game_id IN (SELECT id FROM games WHERE played_at >= ?)")
		args = append(args, f.After)
	}
	if !f.Until.IsZero() {
		conds = append(conds, "gs.game_id IN (SELECT id FROM games WHERE played_at <= ?)")
		args = append(args, f.Until)
	}
	args = append(args, clubID)

	q := `
		SELECT p.id, p.club_id, p.name, p.emoji, p.created_at,
			COALESCE(SUM(CASE WHEN gs.game_id IS NOT NULL THEN gs.score END), 0) AS points,
			COUNT(gs.game_id) AS games,
			SUM(CASE WHEN gs.score > 0 THEN 1 ELSE 0 END) AS wins,
			SUM(CASE WHEN gs.score < 0 THEN 1 ELSE 0 END) AS losses,
			SUM(CASE WHEN gs.role = 'melder' AND gs.score >= 0 THEN 1 ELSE 0 END) AS meldings
		FROM players p
		LEFT JOIN game_scores gs ON ` + strings.Join(conds, " AND ") + `
		WHERE p.club_id = ?
		GROUP BY p.id
		ORDER BY points DESC, games DESC, p.name COLLATE NOCASE`

	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Player
	for rows.Next() {
		var p Player
		var wins, losses, melds sql.NullInt64
		if err := rows.Scan(&p.ID, &p.ClubID, &p.Name, &p.Emoji, &p.CreatedAt,
			&p.Points, &p.Games, &wins, &losses, &melds); err != nil {
			return nil, err
		}
		p.Wins = int(wins.Int64)
		p.Losses = int(losses.Int64)
		p.Meldings = int(melds.Int64)
		out = append(out, p)
	}
	return out, rows.Err()
}

// --- Meldings --------------------------------------------------------------

// DefaultMeldings is the list of meldings seeded into new clubs.
var DefaultMeldings = []Melding{
	{Position: 1, Name: "7", Bid: 7, Points: 1, Type: MeldingTypeNormal},
	{Position: 2, Name: "8", Bid: 8, Points: 2, Type: MeldingTypeNormal},
	{Position: 3, Name: "9", Bid: 9, Points: 3, Type: MeldingTypeNormal},
	{Position: 4, Name: "10", Bid: 10, Points: 4, Type: MeldingTypeNormal},
	{Position: 5, Name: "11", Bid: 11, Points: 6, Type: MeldingTypeNormal},
	{Position: 6, Name: "12", Bid: 12, Points: 8, Type: MeldingTypeNormal},
	{Position: 7, Name: "13", Bid: 13, Points: 16, Type: MeldingTypeNormal},
	{Position: 8, Name: "Sol", Bid: 2, Points: 1, Type: MeldingTypeNolo},
	{Position: 9, Name: "Ren sol", Bid: 1, Points: 3, Type: MeldingTypeNolo},
}

func seedDefaultMeldingsTx(tx *sql.Tx, clubID string) error {
	for _, m := range DefaultMeldings {
		if _, err := tx.Exec(`INSERT INTO meldings (club_id, position, name, bid, points, type)
			VALUES (?, ?, ?, ?, ?, ?)`, clubID, m.Position, m.Name, m.Bid, m.Points, m.Type); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) ListMeldings(clubID string) ([]Melding, error) {
	rows, err := s.db.Query(`
		SELECT id, club_id, position, name, bid, points, type
		FROM meldings WHERE club_id = ? ORDER BY position, id`, clubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Melding
	for rows.Next() {
		var m Melding
		if err := rows.Scan(&m.ID, &m.ClubID, &m.Position, &m.Name, &m.Bid, &m.Points, &m.Type); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) GetMelding(clubID string, id int) (Melding, error) {
	var m Melding
	err := s.db.QueryRow(`
		SELECT id, club_id, position, name, bid, points, type
		FROM meldings WHERE club_id = ? AND id = ?`, clubID, id).
		Scan(&m.ID, &m.ClubID, &m.Position, &m.Name, &m.Bid, &m.Points, &m.Type)
	if errors.Is(err, sql.ErrNoRows) {
		return Melding{}, ErrNotFound
	}
	return m, err
}

// ReplaceMeldings overwrites the club's meldings with the supplied list,
// preserving submission order as position.
func (s *Store) ReplaceMeldings(clubID string, meldings []Melding) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM meldings WHERE club_id = ?`, clubID); err != nil {
		return err
	}
	for i, m := range meldings {
		t := m.Type
		if t == "" {
			t = MeldingTypeNormal
		}
		if _, err := tx.Exec(`INSERT INTO meldings (club_id, position, name, bid, points, type)
			VALUES (?, ?, ?, ?, ?, ?)`, clubID, i+1, m.Name, m.Bid, m.Points, t); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// --- Games -----------------------------------------------------------------

// AddGame stores a played game. The per-player score is computed from
// melding/bid/role/tricks via internal/game.ComputeScores.
func (s *Store) AddGame(clubID string, playedAt time.Time, m Melding, scores []game.PlayerEntry, note string) (int, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	mtype := m.Type
	if mtype == "" {
		mtype = MeldingTypeNormal
	}
	res, err := tx.Exec(`INSERT INTO games (club_id, played_at, melding_id, melding_name, melding_type, bid, melding_points, note)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, clubID, playedAt, m.ID, m.Name, mtype, m.Bid, m.Points, note)
	if err != nil {
		return 0, err
	}
	id64, _ := res.LastInsertId()
	gameID := int(id64)

	if err := replaceGameScoresTx(tx, clubID, gameID, mtype, m.Bid, m.Points, scores); err != nil {
		return 0, err
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return gameID, nil
}

func (s *Store) ListGames(clubID string) ([]Game, error) {
	rows, err := s.db.Query(`
		SELECT id, club_id, played_at, melding_id, melding_name, melding_type, bid, melding_points, note, created_at
		FROM games WHERE club_id = ?
		ORDER BY played_at DESC, id DESC`, clubID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var games []Game
	var ids []int
	for rows.Next() {
		var g Game
		if err := rows.Scan(&g.ID, &g.ClubID, &g.PlayedAt, &g.MeldingID, &g.MeldingName, &g.MeldingType, &g.Bid, &g.MeldingPoints, &g.Note, &g.CreatedAt); err != nil {
			return nil, err
		}
		games = append(games, g)
		ids = append(ids, g.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(games) == 0 {
		return games, nil
	}
	scores, err := s.loadGameScores(ids)
	if err != nil {
		return nil, err
	}
	for i, g := range games {
		g.Scores = scores[g.ID]
		games[i] = g
	}
	return games, nil
}

func (s *Store) GetGame(clubID string, id int) (Game, error) {
	var g Game
	err := s.db.QueryRow(`
		SELECT id, club_id, played_at, melding_id, melding_name, melding_type, bid, melding_points, note, created_at
		FROM games WHERE club_id = ? AND id = ?`, clubID, id).
		Scan(&g.ID, &g.ClubID, &g.PlayedAt, &g.MeldingID, &g.MeldingName, &g.MeldingType, &g.Bid, &g.MeldingPoints, &g.Note, &g.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Game{}, ErrNotFound
	}
	if err != nil {
		return Game{}, err
	}
	scores, err := s.loadGameScores([]int{g.ID})
	if err != nil {
		return Game{}, err
	}
	g.Scores = scores[g.ID]
	return g, nil
}

func (s *Store) DeleteGame(clubID string, id int) error {
	res, err := s.db.Exec(`DELETE FROM games WHERE club_id = ? AND id = ?`, clubID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) UpdateGame(clubID string, id int, playedAt time.Time, m Melding, scores []game.PlayerEntry, note string) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	mtype := m.Type
	if mtype == "" {
		mtype = MeldingTypeNormal
	}
	res, err := tx.Exec(`
		UPDATE games
		SET played_at = ?, melding_id = ?, melding_name = ?, melding_type = ?, bid = ?, melding_points = ?, note = ?
		WHERE club_id = ? AND id = ?`,
		playedAt, m.ID, m.Name, mtype, m.Bid, m.Points, note, clubID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(`DELETE FROM game_scores WHERE game_id = ?`, id); err != nil {
		return err
	}
	if err := replaceGameScoresTx(tx, clubID, id, mtype, m.Bid, m.Points, scores); err != nil {
		return err
	}
	return tx.Commit()
}

func replaceGameScoresTx(tx *sql.Tx, clubID string, gameID int, mtype string, bid int, points int, scores []game.PlayerEntry) error {
	computed := game.ComputeScores(mtype, bid, points, scores)
	for _, sc := range scores {
		var club string
		if err := tx.QueryRow(`SELECT club_id FROM players WHERE id = ?`, sc.PlayerID).Scan(&club); err != nil {
			return fmt.Errorf("player %d: %w", sc.PlayerID, err)
		}
		if club != clubID {
			return fmt.Errorf("player %d not in club", sc.PlayerID)
		}
		if _, err := tx.Exec(`INSERT INTO game_scores (game_id, player_id, role, tricks, score) VALUES (?, ?, ?, ?, ?)`,
			gameID, sc.PlayerID, sc.Role, sc.Tricks, computed[sc.PlayerID]); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) loadGameScores(gameIDs []int) (map[int][]PlayerScore, error) {
	if len(gameIDs) == 0 {
		return map[int][]PlayerScore{}, nil
	}
	placeholders := make([]string, len(gameIDs))
	args := make([]any, len(gameIDs))
	for i, id := range gameIDs {
		placeholders[i] = "?"
		args[i] = id
	}
	q := fmt.Sprintf(`
		SELECT gs.game_id, gs.score, gs.role, gs.tricks,
			p.id, p.club_id, p.name, p.emoji, p.created_at
		FROM game_scores gs
		JOIN players p ON p.id = gs.player_id
		WHERE gs.game_id IN (%s)
		ORDER BY gs.game_id, gs.score DESC, p.name COLLATE NOCASE`,
		strings.Join(placeholders, ","))
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make(map[int][]PlayerScore, len(gameIDs))
	for rows.Next() {
		var gameID int
		var ps PlayerScore
		if err := rows.Scan(&gameID, &ps.Score, &ps.Role, &ps.Tricks,
			&ps.Player.ID, &ps.Player.ClubID, &ps.Player.Name, &ps.Player.Emoji, &ps.Player.CreatedAt); err != nil {
			return nil, err
		}
		out[gameID] = append(out[gameID], ps)
	}
	return out, rows.Err()
}
