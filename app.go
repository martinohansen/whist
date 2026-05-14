package main

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/martinohansen/whist/internal/db"
	"github.com/martinohansen/whist/internal/game"
	"github.com/martinohansen/whist/internal/mistral"
)

const dateLayout = "2006-01-02"

// Store is the storage surface the handlers need. *db.Store satisfies it;
// tests can swap in a fake.
type Store interface {
	// Clubs
	CreateClub(name string) (db.Club, error)
	GetClub(id string) (db.Club, error)
	UpdateClub(id, name, emoji, rules string) error
	VerifyClubPassword(id, password string) (bool, error)
	ClubPasswordHash(id string) (string, error)
	SetClubPassword(id, password string) error
	SearchClubs(query string) ([]db.Club, error)

	// Players
	AddPlayer(clubID, name string) (db.Player, error)
	UpdatePlayer(clubID string, id int, name, emoji string) error
	DeletePlayer(clubID string, id int) error
	ListPlayers(clubID string) ([]db.Player, error)
	PlayersByIDs(clubID string, ids []int) ([]db.Player, error)
	Leaderboard(clubID string) ([]db.Player, error)
	LeaderboardFiltered(clubID string, f db.LeaderboardFilter) ([]db.Player, error)

	// Seasons
	ListSeasons(clubID string) ([]db.Season, error)
	SeasonByID(clubID string, id int) (db.Season, bool, error)
	SeasonForDate(clubID, date string) (db.Season, bool, error)
	LatestSeason(clubID string) (db.Season, bool, error)
	AddSeason(clubID, name, startDate, endDate string) error
	UpdateSeason(clubID string, id int, name, startDate, endDate string) error
	DeleteSeason(clubID string, id int) error

	// Meldings
	ListMeldings(clubID string) ([]db.Melding, error)
	GetMelding(clubID string, id int) (db.Melding, error)
	ReplaceMeldings(clubID string, ms []db.Melding) error

	// Games
	AddGame(clubID string, playedAt time.Time, m db.Melding, scores []game.PlayerEntry, note string) (int, error)
	ListGames(clubID string) ([]db.Game, error)
	GetGame(clubID string, id int) (db.Game, error)
	DeleteGame(clubID string, id int) error

	// Drafts (paper-import flow)
	AddDrafts(clubID, batchID string, drafts []db.Draft) error
	ListPendingDrafts(clubID string) ([]db.Draft, error)
	GetDraft(clubID string, id int) (db.Draft, error)
	UpdateDraft(clubID string, id int, playedAt time.Time, meldingID int, note string, scores []db.DraftScore) error
	DeleteDraft(clubID string, id int) error
	ApproveDrafts(clubID string) (int, []int, error)
	RejectPendingDrafts(clubID string) error
}

// ImportClient is the Mistral surface used by the import handlers. Concrete
// type is *mistral.Client; tests can stub it.
type ImportClient interface {
	Enabled() bool
	OCR(ctx context.Context, img []byte, mime string) (string, error)
	Extract(ctx context.Context, markdown string, meldings []db.Melding, players []db.Player) ([]mistral.DraftGame, error)
}

type App struct {
	store       Store
	mistral     ImportClient
	clubLimiter *rateLimiter
}

func newApp(store Store, ic ImportClient) *App {
	return &App{
		store:       store,
		mistral:     ic,
		clubLimiter: newRateLimiter(1000, 5*time.Minute),
	}
}

func (a *App) routes() http.Handler {
	mux := http.NewServeMux()
	staticServer := http.FileServer(http.FS(staticContent))
	mux.Handle("/static/", http.StripPrefix("/static/", staticServer))
	mux.HandleFunc("/robots.txt", handleRobots)

	mux.HandleFunc("/", a.handleHome)
	mux.HandleFunc("/clubs", a.handleClubs)
	mux.HandleFunc("/clubs/new", a.handleCreateClub)
	mux.HandleFunc("/go", a.handleClubLookup)

	// Club-scoped routes
	mux.HandleFunc("/c/", a.handleClubRoute)
	return mux
}

func handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("User-agent: *\nDisallow: /c/\n"))
}

// handleClubRoute dispatches /c/{id}/... and enforces password gating.
func (a *App) handleClubRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/c/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	clubID := strings.ToLower(parts[0])
	if !a.allowClubPath(w, r) {
		return
	}
	if !db.ValidID(clubID) {
		http.NotFound(w, r)
		return
	}
	club, err := a.store.GetClub(clubID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}

	// The unlock page is the only route a password-protected club exposes
	// without the cookie.
	if sub == "unlock" {
		a.handleUnlock(w, r, club)
		return
	}

	// Enforce password if set
	if club.PasswordOn {
		hash, err := a.store.ClubPasswordHash(club.ID)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		if !hasUnlockCookie(r, club.ID, hash) {
			http.Redirect(w, r, clubPath(&club, "unlock"), http.StatusSeeOther)
			return
		}
	}

	// Past auth — record this club as recently visited.
	touchRecent(w, r, club)

	switch {
	case sub == "" || sub == "/":
		a.handleLeaderboard(w, r, club)
	case sub == "games":
		a.handleGames(w, r, club)
	case sub == "new":
		a.handleNewGame(w, r, club)
	case sub == "players/add":
		a.handleAddPlayer(w, r, club)
	case strings.HasPrefix(sub, "players/"):
		rest := strings.TrimPrefix(sub, "players/")
		idStr, ok := strings.CutSuffix(rest, "/delete")
		if !ok {
			http.NotFound(w, r)
			return
		}
		pid, err := strconv.Atoi(idStr)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		a.handleDeletePlayer(w, r, club, pid)
	case sub == "games/save":
		a.handleSaveGame(w, r, club)
	case sub == "import/analyze":
		a.handleAnalyzeImport(w, r, club)
	case sub == "import/review":
		a.handleReviewImport(w, r, club)
	case sub == "import/approve":
		a.handleApproveDrafts(w, r, club)
	case sub == "import/reject":
		a.handleRejectDrafts(w, r, club)
	case strings.HasPrefix(sub, "import/"):
		rest := strings.TrimPrefix(sub, "import/")
		if idStr, ok := strings.CutSuffix(rest, "/delete"); ok {
			did, err := strconv.Atoi(idStr)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			a.handleDeleteDraft(w, r, club, did)
			return
		}
		if idStr, ok := strings.CutSuffix(rest, "/save"); ok {
			did, err := strconv.Atoi(idStr)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			a.handleSaveDraft(w, r, club, did)
			return
		}
		http.NotFound(w, r)
	case sub == "settings":
		a.handleSettings(w, r, club)
	case sub == "settings/save":
		a.handleSaveSettings(w, r, club)
	case strings.HasPrefix(sub, "seasons/"):
		rest := strings.TrimPrefix(sub, "seasons/")
		if idStr, ok := strings.CutSuffix(rest, "/delete"); ok {
			sid, err := strconv.Atoi(idStr)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			a.handleDeleteSeason(w, r, club, sid)
			return
		}
		http.NotFound(w, r)
	case strings.HasPrefix(sub, "games/"):
		idStr := strings.TrimPrefix(sub, "games/")
		if rest, ok := strings.CutSuffix(idStr, "/delete"); ok {
			gameID, err := strconv.Atoi(rest)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			a.handleDeleteGame(w, r, club, gameID)
			return
		}
		gameID, err := strconv.Atoi(idStr)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		a.handleGameDetail(w, r, club, gameID)
	default:
		http.NotFound(w, r)
	}
}

func (a *App) allowClubPath(w http.ResponseWriter, r *http.Request) bool {
	if a.clubLimiter == nil {
		return true
	}
	if a.clubLimiter.allow(clientIP(r), time.Now()) {
		return true
	}
	http.Error(w, "429 Too Many Requests", http.StatusTooManyRequests)
	return false
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

type layoutData struct {
	Title          string
	Path           string
	Club           *db.Club
	Seasons        []db.Season
	SeasonID       int
	SeasonExplicit bool
}

func (a *App) newLayout(r *http.Request, title, path string, club *db.Club) layoutData {
	data := layoutData{
		Title: title,
		Path:  path,
		Club:  club,
	}
	if club != nil && r != nil {
		if ctx, err := a.loadSeasonContext(r, *club); err == nil {
			data.Seasons = ctx.Seasons
			data.SeasonID = ctx.SeasonID
			data.SeasonExplicit = ctx.SeasonExplicit
		}
	}
	return data
}

func clubPath(c *db.Club, sub string) string {
	if c == nil {
		return "/"
	}
	if sub == "" {
		return "/c/" + c.ID
	}
	return "/c/" + c.ID + "/" + strings.TrimPrefix(sub, "/")
}

func parseIDs(values []string) ([]int, error) {
	var ids []int
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		id, err := strconv.Atoi(v)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parsePlayedAt(raw string) (time.Time, string) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now(), ""
	}
	t, err := time.Parse(dateLayout, raw)
	if err != nil {
		return time.Time{}, "Ugyldig dato."
	}
	return t, ""
}

func dedupeInts(in []int) []int {
	seen := make(map[int]struct{}, len(in))
	out := make([]int, 0, len(in))
	for _, v := range in {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}
