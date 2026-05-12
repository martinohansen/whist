package main

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/martinohansen/whist/internal/db"
)

func (a *App) handleAddPlayer(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Redirect(w, r, redirectAfterPlayerChange(r, club), http.StatusSeeOther)
		return
	}
	if _, err := a.store.AddPlayer(club.ID, name); err != nil {
		slog.Warn("add player", "club", club.ID, "name", name, "error", err)
	}
	http.Redirect(w, r, redirectAfterPlayerChange(r, club), http.StatusSeeOther)
}

func (a *App) handleDeletePlayer(w http.ResponseWriter, r *http.Request, club db.Club, id int) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.store.DeletePlayer(club.ID, id); err != nil {
		if errors.Is(err, db.ErrPlayerHasGames) {
			a.renderSettings(w, r, club, "Spilleren har spil og kan ikke slettes.", "")
			return
		}
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Warn("delete player", "club", club.ID, "id", id, "error", err)
		a.renderSettings(w, r, club, "Kunne ikke slette spilleren.", "")
		return
	}
	http.Redirect(w, r, clubPath(&club, "settings"), http.StatusSeeOther)
}

// redirectAfterPlayerChange picks the most useful page to land on after
// adding a player: stay on the page the request came from when it's the
// new-game flow, else the reglement page.
func redirectAfterPlayerChange(r *http.Request, club db.Club) string {
	ref := r.Referer()
	if strings.HasSuffix(ref, "/new") || strings.Contains(ref, "/new?") {
		return clubPath(&club, "new")
	}
	return clubPath(&club, "settings")
}
