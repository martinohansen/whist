package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/martinohansen/whist/internal/db"
)

// seasonContext describes the active season as derived from the request.
type seasonContext struct {
	Seasons        []db.Season // all seasons for the club, newest first
	Selected       *db.Season  // currently active season, nil for "all time"
	SeasonID       int         // ID of the selected season, 0 for "all time"
	SeasonExplicit bool        // user supplied ?season=... explicitly
}

// loadSeasonContext resolves which season the request is asking for.
//   - ?season=N → that season (or "all time" if N == 0 / not found)
//   - no param → today's season, else latest, else nil
func (a *App) loadSeasonContext(r *http.Request, club db.Club) (seasonContext, error) {
	seasons, err := a.store.ListSeasons(club.ID)
	if err != nil {
		return seasonContext{}, err
	}
	ctx := seasonContext{Seasons: seasons}

	raw, explicit := seasonParam(r)
	if explicit {
		ctx.SeasonExplicit = true
		if id, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && id > 0 {
			s, ok, err := a.store.SeasonByID(club.ID, id)
			if err != nil {
				return ctx, err
			}
			if ok {
				ctx.Selected = &s
				ctx.SeasonID = s.ID
			}
		}
		return ctx, nil
	}

	today := time.Now().Format(dateLayout)
	if s, ok, err := a.store.SeasonForDate(club.ID, today); err != nil {
		return ctx, err
	} else if ok {
		ctx.Selected = &s
		ctx.SeasonID = s.ID
		return ctx, nil
	}
	if s, ok, err := a.store.LatestSeason(club.ID); err != nil {
		return ctx, err
	} else if ok {
		ctx.Selected = &s
		ctx.SeasonID = s.ID
	}
	return ctx, nil
}

func seasonParam(r *http.Request) (string, bool) {
	if values, ok := r.URL.Query()["season"]; ok {
		if len(values) == 0 {
			return "", true
		}
		return values[0], true
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err == nil {
			if values, ok := r.Form["season"]; ok {
				if len(values) == 0 {
					return "", true
				}
				return values[0], true
			}
		}
	}
	return "", false
}

// seasonFilter turns a season into a LeaderboardFilter (After/Until are
// inclusive day bounds). Nil season → empty filter (all time).
func seasonFilter(s *db.Season) db.LeaderboardFilter {
	if s == nil {
		return db.LeaderboardFilter{}
	}
	f := db.LeaderboardFilter{}
	if t, err := time.Parse(dateLayout, s.StartDate); err == nil {
		f.After = t
	}
	if s.EndDate != "" {
		if t, err := time.Parse(dateLayout, s.EndDate); err == nil {
			// Include the entire end day.
			f.Until = t.Add(24*time.Hour - time.Second)
		}
	}
	return f
}

func validateSeasonForm(name, start, end string) string {
	if name == "" {
		return "Navn er påkrævet."
	}
	if start == "" {
		return "Startdato er påkrævet."
	}
	startD, err := time.Parse(dateLayout, start)
	if err != nil {
		return "Ugyldig startdato."
	}
	if end != "" {
		endD, err := time.Parse(dateLayout, end)
		if err != nil {
			return "Ugyldig slutdato."
		}
		if startD.After(endD) {
			return "Startdato skal være før slutdato."
		}
	}
	return ""
}

func seasonErrMessage(err error) string {
	if errors.Is(err, db.ErrSeasonOverlap) {
		return "Sæsonen overlapper en eksisterende sæson."
	}
	if errors.Is(err, db.ErrSeasonNotFound) {
		return "Sæsonen findes ikke."
	}
	return "Kunne ikke gemme sæson."
}

func (a *App) handleDeleteSeason(w http.ResponseWriter, r *http.Request, club db.Club, id int) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.store.DeleteSeason(club.ID, id); err != nil && !errors.Is(err, db.ErrSeasonNotFound) {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, clubPath(&club, "settings"), http.StatusSeeOther)
}
