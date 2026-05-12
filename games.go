package main

import (
	"errors"
	"net/http"

	"github.com/martinohansen/whist/internal/db"
)

type gamesData struct {
	layoutData
	Games []db.Game
}

type gameDetailData struct {
	layoutData
	Game db.Game
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
	data := gameDetailData{
		layoutData: a.newLayout(r, club.Name+" — Spil", clubPath(&club, "games"), &club),
		Game:       g,
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/game.html")
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
	http.Redirect(w, r, clubPath(&club, "games"), http.StatusSeeOther)
}
