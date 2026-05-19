package main

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/martinohansen/whist/internal/db"
	"github.com/martinohansen/whist/internal/game"
)

type newGameData struct {
	layoutData
	Players           []gameFormPlayer
	Meldings          []db.Melding
	PlayedAt          string
	Note              string
	Error             string
	Editing           bool
	GameID            int
	FormAction        string
	SubmitLabel       string
	SelectedMeldingID int
	PendingDrafts     int
}

type gameFormPlayer struct {
	db.Player
	Role   string
	Tricks int
}

type parsedGameForm struct {
	Melding  db.Melding
	Entries  []game.PlayerEntry
	PlayedAt time.Time
	Note     string
}

func (a *App) handleNewGame(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.renderNewGame(w, r, club, r.URL.Query().Get("import_error"))
}

func (a *App) renderNewGame(w http.ResponseWriter, r *http.Request, club db.Club, errMsg string) {
	players, err := a.loadGameFormPlayers(club.ID, nil)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	meldings, err := a.store.ListMeldings(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	data := newGameData{
		layoutData:  a.newLayout(r, club.Name+" — Nyt spil", clubPath(&club, "new"), &club),
		Players:     players,
		Meldings:    meldings,
		PlayedAt:    time.Now().Format(dateLayout),
		Error:       errMsg,
		FormAction:  clubPath(&club, "games/save"),
		SubmitLabel: "Gem spil",
	}
	if a.importEnabled() {
		drafts, err := a.store.ListPendingDrafts(club.ID)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		data.PendingDrafts = len(drafts)
	}
	renderTemplate(w, "layout", data,
		"templates/layout.html",
		"templates/game_entry_shared.html",
		"templates/new.html",
	)
}

func (a *App) handleSaveGame(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	form, msg, err := a.parseGameForm(r, club)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if msg != "" {
		a.renderNewGame(w, r, club, msg)
		return
	}

	if _, err := a.store.AddGame(club.ID, form.PlayedAt, form.Melding, form.Entries, form.Note); err != nil {
		slog.Error("add game", "error", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if r.FormValue("after") == "new" {
		http.Redirect(w, r, clubPathForRequest(r, &club, "new"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, clubPathForRequest(r, &club, "games"), http.StatusSeeOther)
}

// handleGameDefaults returns the trick distribution that the new-game form
// would default to for the given melding + role layout. Roles arrive as
// repeated `role` form values in display order (one per player card);
// the response `tricks` array matches that length 1:1.
func (a *App) handleGameDefaults(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	rawRoles := r.Form["role"]
	roles := make([]string, len(rawRoles))
	for i, raw := range rawRoles {
		role, _ := game.NormalizeRole(raw)
		roles[i] = role
	}
	tricks := make([]int, len(roles))
	meldingID, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("melding_id")))
	if meldingID > 0 {
		melding, err := a.store.GetMelding(club.ID, meldingID)
		if err == nil {
			tricks = game.DefaultTricks(melding.Type, melding.Bid, roles)
		} else if !errors.Is(err, db.ErrNotFound) {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(struct {
		Tricks []int `json:"tricks"`
	}{Tricks: tricks}); err != nil {
		slog.Error("encode game defaults", "err", err)
	}
}

func (a *App) parseGameEntries(r *http.Request, club db.Club) (parsedGameForm, string, error) {
	meldingID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("melding_id")))
	if err != nil {
		return parsedGameForm{}, "Vælg en melding.", nil
	}
	melding, err := a.store.GetMelding(club.ID, meldingID)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			return parsedGameForm{}, "Ukendt melding.", nil
		}
		return parsedGameForm{}, "", err
	}

	ids, err := parseIDs(r.Form["player_id"])
	if err != nil {
		return parsedGameForm{}, "Ugyldigt spillervalg.", nil
	}
	ids = dedupeInts(ids)
	if len(ids) < 4 {
		return parsedGameForm{}, "Vælg fire spillere.", nil
	}

	players, err := a.store.PlayersByIDs(club.ID, ids)
	if err != nil {
		return parsedGameForm{}, "", err
	}
	if len(players) != len(ids) {
		return parsedGameForm{}, "Ukendt spiller.", nil
	}

	var inputs []game.PlayerEntry
	for _, id := range ids {
		role, ok := game.NormalizeRole(r.FormValue("role_" + strconv.Itoa(id)))
		if !ok {
			return parsedGameForm{}, "Hver spiller skal have en rolle.", nil
		}
		tricksRaw := strings.TrimSpace(r.FormValue("tricks_" + strconv.Itoa(id)))
		if tricksRaw == "" {
			tricksRaw = "0"
		}
		tricks, err := strconv.Atoi(tricksRaw)
		if err != nil || tricks < 0 || tricks > 13 {
			return parsedGameForm{}, "Stik skal være 0–13.", nil
		}
		inputs = append(inputs, game.PlayerEntry{PlayerID: id, Role: role, Tricks: tricks})
	}
	if msg := gameEntryMessage(melding.Type, game.ValidateEntries(melding.Type, inputs)); msg != "" {
		return parsedGameForm{}, msg, nil
	}
	return parsedGameForm{Melding: melding, Entries: inputs}, "", nil
}

func (a *App) parseGameForm(r *http.Request, club db.Club) (parsedGameForm, string, error) {
	form, msg, err := a.parseGameEntries(r, club)
	if err != nil || msg != "" {
		return form, msg, err
	}

	playedAt, msg := parsePlayedAt(r.FormValue("played_at"))
	if msg != "" {
		return parsedGameForm{}, msg, nil
	}
	form.PlayedAt = playedAt
	form.Note = strings.TrimSpace(r.FormValue("note"))
	return form, "", nil
}

func (a *App) loadGameFormPlayers(clubID string, scores []db.PlayerScore) ([]gameFormPlayer, error) {
	players, err := a.store.ListPlayers(clubID)
	if err != nil {
		return nil, err
	}
	byID := make(map[int]db.PlayerScore, len(scores))
	for _, score := range scores {
		byID[score.Player.ID] = score
	}
	formPlayers := make([]gameFormPlayer, 0, len(players))
	for _, player := range players {
		formPlayer := gameFormPlayer{Player: player}
		if score, ok := byID[player.ID]; ok {
			formPlayer.Role = score.Role
			formPlayer.Tricks = score.Tricks
		}
		formPlayers = append(formPlayers, formPlayer)
	}
	return formPlayers, nil
}

func gameEntryMessage(meldingType string, issues []game.ValidationIssue) string {
	for _, issue := range issues {
		if msg := game.IssueMessage(meldingType, issue); msg != "" {
			return msg
		}
	}
	return ""
}
