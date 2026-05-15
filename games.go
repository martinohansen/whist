package main

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/martinohansen/whist/internal/db"
)

type gamesData struct {
	layoutData
	Games []db.Game
}

type gameDetailData struct {
	layoutData
	Game      db.Game
	OlderGame *db.Game
	NewerGame *db.Game
}

func (a *App) handleGames(w http.ResponseWriter, r *http.Request, club db.Club) {
	games, err := a.store.ListGames(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	data := gamesData{
		layoutData: a.newLayout(r, club.Name+" — Spil", clubPath(&club, "games"), &club),
		Games:      games,
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/games.html")
}

func (a *App) handleGameDetail(w http.ResponseWriter, r *http.Request, club db.Club, id int) {
	g, err := a.store.GetGame(club.ID, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	games, err := a.store.ListGames(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	older, newer := adjacentGames(games, g.ID)
	data := gameDetailData{
		layoutData: a.newLayout(r, club.Name+" — Spil #"+strconv.Itoa(g.ID), clubPath(&club, "games"), &club),
		Game:       g,
		OlderGame:  older,
		NewerGame:  newer,
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/game.html")
}

func adjacentGames(games []db.Game, currentID int) (*db.Game, *db.Game) {
	for i := range games {
		if games[i].ID != currentID {
			continue
		}
		var older *db.Game
		if i+1 < len(games) {
			older = &games[i+1]
		}
		var newer *db.Game
		if i > 0 {
			newer = &games[i-1]
		}
		return older, newer
	}
	return nil, nil
}

func (a *App) handleEditGame(w http.ResponseWriter, r *http.Request, club db.Club, id int) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.renderEditGame(w, r, club, id, "")
}

func (a *App) renderEditGame(w http.ResponseWriter, r *http.Request, club db.Club, id int, errMsg string) {
	g, err := a.store.GetGame(club.ID, id)
	if err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	players, err := a.loadGameFormPlayers(club.ID, g.Scores)
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
		layoutData:        a.newLayout(r, club.Name+" — Ret spil", clubPath(&club, "games"), &club),
		Players:           players,
		Meldings:          meldings,
		PlayedAt:          g.PlayedAt.Format(dateLayout),
		Note:              g.Note,
		Error:             errMsg,
		Editing:           true,
		GameID:            g.ID,
		FormAction:        clubPath(&club, "games/"+strconv.Itoa(g.ID)+"/update"),
		SubmitLabel:       "Gem ændringer",
		SelectedMeldingID: g.MeldingID,
	}
	renderTemplate(w, "layout", data,
		"templates/layout.html",
		"templates/game_entry_shared.html",
		"templates/new.html",
	)
}

func (a *App) handleUpdateGame(w http.ResponseWriter, r *http.Request, club db.Club, id int) {
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
		a.renderEditGame(w, r, club, id, msg)
		return
	}
	if err := a.store.UpdateGame(club.ID, id, form.PlayedAt, form.Melding, form.Entries, form.Note); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, clubPathForRequest(r, &club, "games/"+strconv.Itoa(id)), http.StatusSeeOther)
}

func (a *App) handleDeleteGame(w http.ResponseWriter, r *http.Request, club db.Club, id int) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.store.DeleteGame(club.ID, id); err != nil && !errors.Is(err, db.ErrNotFound) {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, clubPathForRequest(r, &club, "games"), http.StatusSeeOther)
}
