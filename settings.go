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
	PlayerID    int    `json:"player_id"`
	PlayerName  string `json:"player_name"`
	PlayerEmoji string `json:"player_emoji"`
	Points      int    `json:"points"`
	AmountCents int    `json:"amount_cents"`
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
	exampleRows, err := a.settlementExampleRowsFor(r, club, club.DefaultSettlementType, club.DefaultSettlementAmountCents, club.CommonDebtEqualPercent)
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

func (a *App) handleSaveSettings(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	update, msg, err := a.parseSettingsUpdate(r, club)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if msg != "" {
		a.renderSettings(w, r, club, msg, "")
		return
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

func (a *App) parseSettingsUpdate(r *http.Request, club db.Club) (db.SettingsUpdate, string, error) {
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
			return db.SettingsUpdate{}, "Vælg en gyldig standardafregning.", nil
		}
		update.DefaultSettlementType = defaultSettlementType
	}
	if raw := strings.TrimSpace(r.FormValue("default_settlement_amount")); raw != "" {
		defaultAmountCents, ok := parseAmountCents(raw)
		if !ok {
			return db.SettingsUpdate{}, "Standardbeløb skal være et positivt tal med højst to decimaler.", nil
		}
		update.DefaultSettlementAmountCents = defaultAmountCents
	}
	if raw := strings.TrimSpace(r.FormValue("common_debt_equal_percent")); raw != "" {
		commonDebtEqualPercent, err := strconv.Atoi(raw)
		if err != nil || commonDebtEqualPercent < 0 || commonDebtEqualPercent > 100 {
			return db.SettingsUpdate{}, "Fælles andel skal være et tal fra 0 til 100.", nil
		}
		update.CommonDebtEqualPercent = commonDebtEqualPercent
	}

	meldings, msg, err := parseMeldingUpdates(r)
	if err != nil || msg != "" {
		return db.SettingsUpdate{}, msg, err
	}
	update.Meldings = meldings

	players, msg, err := parsePlayerUpdates(r)
	if err != nil || msg != "" {
		return db.SettingsUpdate{}, msg, err
	}
	update.Players = players

	seasons, msg, err := parseSeasonUpdates(r)
	if err != nil || msg != "" {
		return db.SettingsUpdate{}, msg, err
	}
	update.Seasons = seasons

	switch visibility := r.FormValue("visibility"); visibility {
	case "private":
		if club.PasswordOn {
			clear := ""
			update.Password = &clear
		}
	case "public":
		pw := r.FormValue("password")
		if pw != "" {
			update.Password = &pw
		} else if !club.PasswordOn {
			return db.SettingsUpdate{}, "Offentlige klubber kræver et kodeord.", nil
		}
	}

	return update, "", nil
}

func parseMeldingUpdates(r *http.Request) ([]db.Melding, string, error) {
	idsRaw := r.Form["melding_id"]
	names := r.Form["melding_name"]
	types := r.Form["melding_type"]
	bids := r.Form["melding_bid"]
	points := r.Form["melding_points"]
	if len(names) != len(idsRaw) || len(names) != len(bids) || len(names) != len(points) || len(names) != len(types) {
		return nil, "Meldinger er ufuldstændige.", nil
	}
	var meldings []db.Melding
	for i, n := range names {
		n = strings.TrimSpace(n)
		if n == "" {
			continue
		}
		b, err := strconv.Atoi(strings.TrimSpace(bids[i]))
		if err != nil {
			return nil, "Stik skal være tal.", nil
		}
		p, err := strconv.Atoi(strings.TrimSpace(points[i]))
		if err != nil {
			return nil, "Point skal være tal.", nil
		}
		t := strings.TrimSpace(types[i])
		if t != db.MeldingTypeNolo {
			t = db.MeldingTypeNormal
		}
		mid := 0
		if raw := strings.TrimSpace(idsRaw[i]); raw != "" {
			mid, err = strconv.Atoi(raw)
			if err != nil {
				return nil, "Meldinger er ufuldstændige.", nil
			}
		}
		meldings = append(meldings, db.Melding{ID: mid, Name: n, Bid: b, Points: p, Type: t})
	}
	if len(meldings) == 0 {
		return nil, "Mindst én melding kræves.", nil
	}
	return meldings, "", nil
}

func parsePlayerUpdates(r *http.Request) ([]db.PlayerUpdate, string, error) {
	pids := r.Form["player_id"]
	pnames := r.Form["player_name"]
	pemojis := r.Form["player_emoji"]
	if len(pids) != len(pnames) || len(pids) != len(pemojis) {
		return nil, "Spillere er ufuldstændige.", nil
	}
	var players []db.PlayerUpdate
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
			players = append(players, db.PlayerUpdate{Name: pn, Emoji: pe})
			continue
		}
		if pn == "" {
			return nil, "Spillers navn kan ikke være tomt.", nil
		}
		pid, err := strconv.Atoi(idRaw)
		if err != nil {
			return nil, "Spillere er ufuldstændige.", nil
		}
		players = append(players, db.PlayerUpdate{ID: pid, Name: pn, Emoji: pe})
	}
	return players, "", nil
}

func parseSeasonUpdates(r *http.Request) ([]db.SeasonUpdate, string, error) {
	sids := r.Form["season_id"]
	snames := r.Form["season_name"]
	sstarts := r.Form["season_start"]
	sends := r.Form["season_end"]
	if len(sids) != len(snames) || len(sids) != len(sstarts) || len(sids) != len(sends) {
		return nil, "Perioder er ufuldstændige.", nil
	}
	var seasons []db.SeasonUpdate
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
				return nil, msg, nil
			}
			seasons = append(seasons, db.SeasonUpdate{Name: nm, StartDate: st, EndDate: ed})
			continue
		}
		sid, err := strconv.Atoi(idRaw)
		if err != nil {
			return nil, "Perioder er ufuldstændige.", nil
		}
		if msg := validateSeasonForm(nm, st, ed); msg != "" {
			return nil, msg, nil
		}
		seasons = append(seasons, db.SeasonUpdate{ID: sid, Name: nm, StartDate: st, EndDate: ed})
	}
	return seasons, "", nil
}
