package main

import (
	"net/http"

	"github.com/martinohansen/whist/internal/db"
)

type unlockData struct {
	layoutData
	Error string
}

func (a *App) handleUnlock(w http.ResponseWriter, r *http.Request, club db.Club) {
	if !club.PasswordOn {
		http.Redirect(w, r, clubPath(&club, ""), http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		ok, err := a.store.VerifyClubPassword(club.ID, r.FormValue("password"))
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		if !ok {
			data := unlockData{
				layoutData: a.newLayout(r, club.Name, clubPath(&club, "unlock"), nil),
				Error:      "Forkert kodeord.",
			}
			renderTemplate(w, "layout", data, "templates/layout.html", "templates/unlock.html")
			return
		}
		hash, _ := a.store.ClubPasswordHash(club.ID)
		setUnlockCookie(w, club.ID, hash)
		http.Redirect(w, r, clubPath(&club, ""), http.StatusSeeOther)
		return
	}
	data := unlockData{
		layoutData: a.newLayout(r, club.Name, clubPath(&club, "unlock"), nil),
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/unlock.html")
}
