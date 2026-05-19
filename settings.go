package main

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/martinohansen/whist/internal/db"
)

type settingsData struct {
	layoutData
	Meldings              []db.Melding
	Seasons               []db.Season
	Players               []db.Player
	SettlementExampleRows []settlementExampleRow
	EmojiPool             []string
	PasswordEnabled       bool
	Error                 string
	Success               string
}

type settlementExampleRow struct {
	PlayerID    int
	PlayerName  string
	PlayerEmoji string
	Points      int
	AmountCents int
}

func (a *App) handleSettings(w http.ResponseWriter, r *http.Request, club db.Club) {
	a.renderSettings(w, r, club, "", "")
}

func (a *App) renderSettings(w http.ResponseWriter, r *http.Request, club db.Club, errMsg, okMsg string) {
	meldings, err := a.store.ListMeldings(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	seasons, err := a.store.ListSeasons(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	players, err := a.store.ListPlayers(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	exampleRows, err := a.settlementExampleRows(r, club)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	data := settingsData{
		layoutData:            a.newLayout(r, club.Name+" — Klubben", clubPath(&club, "settings"), &club),
		Meldings:              meldings,
		Seasons:               seasons,
		Players:               players,
		SettlementExampleRows: exampleRows,
		EmojiPool:             db.EmojiPool(),
		PasswordEnabled:       club.PasswordOn,
		Error:                 errMsg,
		Success:               okMsg,
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/settings.html")
}

func (a *App) settlementExampleRows(r *http.Request, club db.Club) ([]settlementExampleRow, error) {
	ctx, err := a.loadSeasonContext(r, club)
	if err != nil {
		return nil, err
	}
	players, err := a.store.LeaderboardFiltered(club.ID, seasonFilter(ctx.Selected))
	if err != nil {
		return nil, err
	}
	settlementRows := make([]settlementRow, 0, len(players))
	for _, player := range players {
		if player.Games == 0 {
			continue
		}
		settlementRows = append(settlementRows, settlementRow{
			PlayerID:    player.ID,
			PlayerName:  player.Name,
			PlayerEmoji: player.Emoji,
			Points:      player.Points,
		})
	}
	if len(settlementRows) == 0 {
		return nil, nil
	}
	calculated, err := calculateSettlementRows(club.DefaultSettlementType, club.DefaultSettlementAmountCents, club.CommonDebtEqualPercent, settlementRows)
	if err != nil {
		calculated = settlementRows
	}
	out := make([]settlementExampleRow, 0, len(calculated))
	for _, row := range calculated {
		out = append(out, settlementExampleRow{
			PlayerID:    row.PlayerID,
			PlayerName:  row.PlayerName,
			PlayerEmoji: row.PlayerEmoji,
			Points:      row.Points,
			AmountCents: row.AmountCents,
		})
	}
	return out, nil
}

func (a *App) handleSaveSettings(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	// Club identity. Emoji can be picked from the supported pool; fall back
	// to a name-derived emoji if the submitted value is empty or unknown.
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = club.Name
	}
	emoji := strings.TrimSpace(r.FormValue("emoji"))
	if emoji == "" || !db.IsEmojiInPool(emoji) {
		emoji = db.Emoji(name)
	}
	update := db.SettingsUpdate{
		Name:                         name,
		Emoji:                        emoji,
		Rules:                        r.FormValue("rules"),
		DefaultSettlementType:        club.DefaultSettlementType,
		DefaultSettlementAmountCents: club.DefaultSettlementAmountCents,
		CommonDebtEqualPercent:       club.CommonDebtEqualPercent,
	}
	if raw := strings.TrimSpace(r.FormValue("default_settlement_type")); raw != "" {
		defaultSettlementType := normalizeSettlementType(raw)
		if defaultSettlementType != settlementTypePoints && defaultSettlementType != settlementTypeCommonDebt {
			a.renderSettings(w, r, club, "Vælg en gyldig standardafregning.", "")
			return
		}
		update.DefaultSettlementType = defaultSettlementType
	}

	if raw := strings.TrimSpace(r.FormValue("default_settlement_amount")); raw != "" {
		defaultAmountCents, ok := parseAmountCents(raw)
		if !ok {
			a.renderSettings(w, r, club, "Standardbeløb skal være et positivt tal med højst to decimaler.", "")
			return
		}
		update.DefaultSettlementAmountCents = defaultAmountCents
	}

	if raw := strings.TrimSpace(r.FormValue("common_debt_equal_percent")); raw != "" {
		commonDebtEqualPercent, err := strconv.Atoi(raw)
		if err != nil || commonDebtEqualPercent < 0 || commonDebtEqualPercent > 100 {
			a.renderSettings(w, r, club, "Fælles andel skal være et tal fra 0 til 100.", "")
			return
		}
		update.CommonDebtEqualPercent = commonDebtEqualPercent
	}

	// Meldings: parallel arrays. Existing rows keep their id so games can keep
	// pointing at the same melding when settings are edited.
	idsRaw := r.Form["melding_id"]
	names := r.Form["melding_name"]
	types := r.Form["melding_type"]
	bids := r.Form["melding_bid"]
	points := r.Form["melding_points"]
	if len(names) != len(idsRaw) || len(names) != len(bids) || len(names) != len(points) || len(names) != len(types) {
		a.renderSettings(w, r, club, "Meldinger er ufuldstændige.", "")
		return
	}
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		b, err := strconv.Atoi(strings.TrimSpace(bids[i]))
		if err != nil {
			a.renderSettings(w, r, club, "Stik skal være tal.", "")
			return
		}
		p, err := strconv.Atoi(strings.TrimSpace(points[i]))
		if err != nil {
			a.renderSettings(w, r, club, "Point skal være tal.", "")
			return
		}
		t := strings.TrimSpace(types[i])
		if t != db.MeldingTypeNolo {
			t = db.MeldingTypeNormal
		}
		mid := 0
		if raw := strings.TrimSpace(idsRaw[i]); raw != "" {
			mid, err = strconv.Atoi(raw)
			if err != nil {
				a.renderSettings(w, r, club, "Meldinger er ufuldstændige.", "")
				return
			}
		}
		update.Meldings = append(update.Meldings, db.Melding{ID: mid, Name: n, Bid: b, Points: p, Type: t})
	}
	if len(update.Meldings) == 0 {
		a.renderSettings(w, r, club, "Mindst én melding kræves.", "")
		return
	}

	// Players: parallel arrays player_id[], player_name[], player_emoji[].
	// Empty id means a new player.
	pids := r.Form["player_id"]
	pnames := r.Form["player_name"]
	pemojis := r.Form["player_emoji"]
	if len(pids) != len(pnames) || len(pids) != len(pemojis) {
		a.renderSettings(w, r, club, "Spillere er ufuldstændige.", "")
		return
	}
	for i := range pids {
		pn := strings.TrimSpace(pnames[i])
		pe := strings.TrimSpace(pemojis[i])
		if pe == "" || !db.IsEmojiInPool(pe) {
			pe = db.Emoji(pn)
		}
		idRaw := strings.TrimSpace(pids[i])
		if idRaw == "" {
			if pn == "" {
				continue
			}
			update.Players = append(update.Players, db.PlayerUpdate{Name: pn, Emoji: pe})
			continue
		}
		if pn == "" {
			a.renderSettings(w, r, club, "Spillers navn kan ikke være tomt.", "")
			return
		}
		pid, err := strconv.Atoi(idRaw)
		if err != nil {
			continue
		}
		update.Players = append(update.Players, db.PlayerUpdate{ID: pid, Name: pn, Emoji: pe})
	}

	// Seasons: parallel arrays season_id[], season_name[], season_start[], season_end[].
	sids := r.Form["season_id"]
	snames := r.Form["season_name"]
	sstarts := r.Form["season_start"]
	sends := r.Form["season_end"]
	if len(sids) != len(snames) || len(sids) != len(sstarts) || len(sids) != len(sends) {
		a.renderSettings(w, r, club, "Perioder er ufuldstændige.", "")
		return
	}
	for i := range sids {
		nm := strings.TrimSpace(snames[i])
		st := strings.TrimSpace(sstarts[i])
		ed := strings.TrimSpace(sends[i])
		idRaw := strings.TrimSpace(sids[i])
		if idRaw == "" {
			if nm == "" && st == "" && ed == "" {
				continue
			}
			if msg := validateSeasonForm(nm, st, ed); msg != "" {
				a.renderSettings(w, r, club, msg, "")
				return
			}
			update.Seasons = append(update.Seasons, db.SeasonUpdate{Name: nm, StartDate: st, EndDate: ed})
			continue
		}
		sid, err := strconv.Atoi(idRaw)
		if err != nil {
			continue
		}
		if msg := validateSeasonForm(nm, st, ed); msg != "" {
			a.renderSettings(w, r, club, msg, "")
			return
		}
		update.Seasons = append(update.Seasons, db.SeasonUpdate{ID: sid, Name: nm, StartDate: st, EndDate: ed})
	}

	// Visibility/password handling.
	visibility := r.FormValue("visibility")
	pw := r.FormValue("password")
	switch visibility {
	case "private":
		if club.PasswordOn {
			clear := ""
			update.Password = &clear
		}
	case "public":
		if pw != "" {
			update.Password = &pw
		} else if !club.PasswordOn {
			a.renderSettings(w, r, club, "Offentlige klubber kræver et kodeord.", "")
			return
		}
	}

	if err := a.store.UpdateSettings(club.ID, update); err != nil {
		if errors.Is(err, db.ErrSeasonOverlap) || errors.Is(err, db.ErrSeasonNotFound) {
			a.renderSettings(w, r, club, seasonErrMessage(err), "")
			return
		}
		a.renderSettings(w, r, club, "Kunne ikke gemme klub.", "")
		return
	}
	if update.Password != nil && *update.Password != "" {
		hash, _ := a.store.ClubPasswordHash(club.ID)
		setUnlockCookie(w, club.ID, hash)
	}

	updated, _ := a.store.GetClub(club.ID)
	a.renderSettings(w, r, updated, "", "Gemt.")
}
