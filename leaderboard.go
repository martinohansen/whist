package main

import (
	"net/http"
	"net/url"
	"sort"
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
	SortKey        string
	SortDir        string
	SortLinks      map[string]string
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
	sortKey, sortDir := leaderboardSort(r)
	sortLeaderboardRows(rows, sortKey, sortDir)

	data := leaderboardData{
		layoutData:     a.newLayout(r, club.Name, clubPath(&club, ""), &club),
		Rows:           rows,
		GameCount:      len(gamesInSeason),
		Seasons:        ctx.Seasons,
		Selected:       ctx.Selected,
		SeasonID:       ctx.SeasonID,
		SeasonExplicit: ctx.SeasonExplicit,
		SortKey:        sortKey,
		SortDir:        sortDir,
		SortLinks:      leaderboardSortLinks(r, clubPath(&club, ""), sortKey, sortDir),
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/leaderboard.html")
}

func leaderboardSort(r *http.Request) (string, string) {
	key := r.URL.Query().Get("sort")
	switch key {
	case "games", "wins", "losses", "meldings", "points", "pps":
	default:
		key = "points"
	}
	dir := r.URL.Query().Get("dir")
	if dir != "asc" {
		dir = "desc"
	}
	return key, dir
}

func sortLeaderboardRows(rows []leaderboardRow, key, dir string) {
	sort.SliceStable(rows, func(i, j int) bool {
		left, right := leaderboardSortValue(rows[i], key), leaderboardSortValue(rows[j], key)
		if left != right {
			if dir == "asc" {
				return left < right
			}
			return left > right
		}
		if rows[i].Rank != rows[j].Rank {
			return rows[i].Rank < rows[j].Rank
		}
		return rows[i].Player.Name < rows[j].Player.Name
	})
}

func leaderboardSortValue(row leaderboardRow, key string) float64 {
	switch key {
	case "games":
		return float64(row.Games)
	case "wins":
		return float64(row.Wins)
	case "losses":
		return float64(row.Losses)
	case "meldings":
		return float64(row.Meldings)
	case "pps":
		return row.PPS
	default:
		return float64(row.Points)
	}
}

func leaderboardSortLinks(r *http.Request, path, currentKey, currentDir string) map[string]string {
	out := make(map[string]string, 6)
	for _, key := range []string{"games", "wins", "losses", "meldings", "points", "pps"} {
		q := cloneQuery(r.URL.Query())
		q.Set("sort", key)
		if key == currentKey && currentDir == "desc" {
			q.Set("dir", "asc")
		} else {
			q.Set("dir", "desc")
		}
		out[key] = path + "?" + q.Encode()
	}
	return out
}

func cloneQuery(in url.Values) url.Values {
	out := make(url.Values, len(in))
	for k, values := range in {
		out[k] = append([]string(nil), values...)
	}
	return out
}
