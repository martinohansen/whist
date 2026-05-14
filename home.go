package main

import (
	"net/http"
	"strings"

	"github.com/martinohansen/whist/internal/db"
)

type homeData struct {
	layoutData
	Error    string
	Query    string
	Matches  []db.Club
	Searched bool
	Recents  []RecentClub
}

type clubsData struct {
	layoutData
	Error    string
	Query    string
	Matches  []db.Club
	Searched bool
	Recents  []RecentClub
}

func (a *App) handleHome(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	q := strings.TrimSpace(r.FormValue("q"))
	var matches []db.Club
	searched := false
	if q != "" {
		var err error
		matches, err = a.store.SearchClubs(q)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		searched = true
	}
	data := homeData{
		layoutData: a.newLayout(r, "Whist", "/", nil),
		Query:      q,
		Matches:    matches,
		Searched:   searched,
		Recents:    readRecentClubs(r),
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/home.html")
}

func (a *App) handleCreateClub(w http.ResponseWriter, r *http.Request) {
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
		data := homeData{
			layoutData: a.newLayout(r, "Whist", "/", nil),
			Error:      "Klubben mangler et navn.",
		}
		renderTemplate(w, "layout", data, "templates/layout.html", "templates/home.html")
		return
	}
	club, err := a.store.CreateClub(name)
	if err != nil {
		http.Error(w, "kunne ikke oprette klub", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/c/"+club.ID, http.StatusSeeOther)
}

// handleClubLookup lets users jump to an existing club by ID.
func (a *App) handleClubLookup(w http.ResponseWriter, r *http.Request) {
	code := strings.ToLower(strings.TrimSpace(r.FormValue("club")))
	if !db.ValidID(code) {
		data := homeData{
			layoutData: a.newLayout(r, "Whist", "/", nil),
			Error:      "Ugyldig klub-kode.",
		}
		renderTemplate(w, "layout", data, "templates/layout.html", "templates/home.html")
		return
	}
	if _, err := a.store.GetClub(code); err != nil {
		data := homeData{
			layoutData: a.newLayout(r, "Whist", "/", nil),
			Error:      "Den klub findes ikke.",
		}
		renderTemplate(w, "layout", data, "templates/layout.html", "templates/home.html")
		return
	}
	http.Redirect(w, r, "/c/"+code, http.StatusSeeOther)
}

// handleClubs renders the /clubs search page.
func (a *App) handleClubs(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.FormValue("q"))
	var matches []db.Club
	searched := false
	if q != "" {
		var err error
		matches, err = a.store.SearchClubs(q)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		searched = true
	}
	data := clubsData{
		layoutData: a.newLayout(r, "Whist — klubber", "/clubs", nil),
		Query:      q,
		Matches:    matches,
		Searched:   searched,
		Recents:    readRecentClubs(r),
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/clubs.html")
}
