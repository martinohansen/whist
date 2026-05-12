package main

import (
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
	Players  []db.Player
	Meldings []db.Melding
	PlayedAt string
	Note     string
	Error    string
}

func (a *App) handleNewGame(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.renderNewGame(w, r, club, "")
}

func (a *App) renderNewGame(w http.ResponseWriter, r *http.Request, club db.Club, errMsg string) {
	players, err := a.store.ListPlayers(club.ID)
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
		layoutData: a.newLayout(r, club.Name+" — Nyt spil", clubPath(&club, "new"), &club),
		Players:    players,
		Meldings:   meldings,
		PlayedAt:   time.Now().Format(dateLayout),
		Error:      errMsg,
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/new.html")
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

	meldingID, err := strconv.Atoi(strings.TrimSpace(r.FormValue("melding_id")))
	if err != nil {
		a.renderNewGame(w, r, club, "Vælg en melding.")
		return
	}
	melding, err := a.store.GetMelding(club.ID, meldingID)
	if err != nil {
		a.renderNewGame(w, r, club, "Ukendt melding.")
		return
	}

	ids, err := parseIDs(r.Form["player_id"])
	if err != nil {
		a.renderNewGame(w, r, club, "Ugyldigt spillervalg.")
		return
	}
	ids = dedupeInts(ids)
	if len(ids) < 4 {
		a.renderNewGame(w, r, club, "Vælg fire spillere.")
		return
	}

	players, err := a.store.PlayersByIDs(club.ID, ids)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if len(players) != len(ids) {
		a.renderNewGame(w, r, club, "Ukendt spiller.")
		return
	}

	var melderCount, makkerCount, modspilCount int
	var inputs []game.PlayerEntry
	for _, id := range ids {
		role := strings.TrimSpace(r.FormValue("role_" + strconv.Itoa(id)))
		switch role {
		case "melder":
			melderCount++
		case "makker":
			makkerCount++
		case "modspil":
			modspilCount++
		default:
			a.renderNewGame(w, r, club, "Hver spiller skal have en rolle.")
			return
		}
		tricksRaw := strings.TrimSpace(r.FormValue("tricks_" + strconv.Itoa(id)))
		if tricksRaw == "" {
			tricksRaw = "0"
		}
		tricks, err := strconv.Atoi(tricksRaw)
		if err != nil || tricks < 0 || tricks > 13 {
			a.renderNewGame(w, r, club, "Stik skal være 0–13.")
			return
		}
		inputs = append(inputs, game.PlayerEntry{PlayerID: id, Role: role, Tricks: tricks})
	}
	if melding.Type == db.MeldingTypeNolo {
		if melderCount != 1 || makkerCount+modspilCount != 3 {
			a.renderNewGame(w, r, club, "Vælg én melder og tre andre spillere.")
			return
		}
	} else {
		if melderCount != 1 || makkerCount != 1 {
			a.renderNewGame(w, r, club, "Vælg én melder og én makker.")
			return
		}
	}

	playedAt, msg := parsePlayedAt(r.FormValue("played_at"))
	if msg != "" {
		a.renderNewGame(w, r, club, msg)
		return
	}

	note := strings.TrimSpace(r.FormValue("note"))
	if _, err := a.store.AddGame(club.ID, playedAt, melding, inputs, note); err != nil {
		slog.Error("add game", "error", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, clubPath(&club, "games"), http.StatusSeeOther)
}
