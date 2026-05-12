package main

import (
	"net/http"
	"time"

	"github.com/martinohansen/whist/internal/db"
)

type leaderboardRow struct {
	Rank     int
	Player   db.Player
	Games    int
	Wins     int
	Losses   int
	Meldings int
	Points   int
	PPS      float64
	// Delta: positive means moved up, negative means moved down, zero unchanged.
	Delta int
}

type leaderboardData struct {
	layoutData
	Rows           []leaderboardRow
	GameCount      int
	Seasons        []db.Season
	Selected       *db.Season
	SeasonID       int
	SeasonExplicit bool
}

func (a *App) handleLeaderboard(w http.ResponseWriter, r *http.Request, club db.Club) {
	ctx, err := a.loadSeasonContext(r, club)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	filter := seasonFilter(ctx.Selected)

	current, err := a.store.LeaderboardFiltered(club.ID, filter)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	games, err := a.store.ListGames(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	// Limit games to the season window for "most recent day of play".
	gamesInSeason := games
	if !filter.After.IsZero() || !filter.Until.IsZero() {
		gamesInSeason = gamesInSeason[:0]
		for _, g := range games {
			if !filter.After.IsZero() && g.PlayedAt.Before(filter.After) {
				continue
			}
			if !filter.Until.IsZero() && g.PlayedAt.After(filter.Until) {
				continue
			}
			gamesInSeason = append(gamesInSeason, g)
		}
	}

	// Compute prior ranks: leaderboard as it stood before the most recent
	// day of play. Arrows only show for players who already had a rank
	// before that day — new entrants get no arrow.
	priorRank := map[int]int{}
	if len(gamesInSeason) > 0 {
		lastDay := gamesInSeason[0].PlayedAt.Truncate(24 * time.Hour)
		priorFilter := filter
		priorFilter.Before = lastDay
		prior, err := a.store.LeaderboardFiltered(club.ID, priorFilter)
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		for i, p := range prior {
			if p.Games > 0 {
				priorRank[p.ID] = i + 1
			}
		}
	}

	rows := make([]leaderboardRow, 0, len(current))
	for i, p := range current {
		row := leaderboardRow{
			Rank:     i + 1,
			Player:   p,
			Games:    p.Games,
			Wins:     p.Wins,
			Losses:   p.Losses,
			Meldings: p.Meldings,
			Points:   p.Points,
		}
		if p.Games > 0 {
			row.PPS = float64(p.Points) / float64(p.Games)
		}
		if pr, ok := priorRank[p.ID]; ok {
			row.Delta = pr - row.Rank
		}
		rows = append(rows, row)
	}

	data := leaderboardData{
		layoutData:     a.newLayout(r, club.Name, clubPath(&club, ""), &club),
		Rows:           rows,
		GameCount:      len(gamesInSeason),
		Seasons:        ctx.Seasons,
		Selected:       ctx.Selected,
		SeasonID:       ctx.SeasonID,
		SeasonExplicit: ctx.SeasonExplicit,
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/leaderboard.html")
}
