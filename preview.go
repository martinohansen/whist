package main

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/martinohansen/whist/internal/db"
	"github.com/martinohansen/whist/internal/game"
)

type gamePreviewResponse struct {
	Valid   bool   `json:"valid"`
	Summary string `json:"summary"`
	Message string `json:"message"`
}

type settlementPreviewResponse struct {
	Error string                 `json:"error,omitempty"`
	Rows  []settlementExampleRow `json:"rows,omitempty"`
}

func (a *App) handlePreviewGame(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	form, msg, err := a.parseGameEntries(r, club)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	resp := gamePreviewResponse{Message: msg, Summary: msg}
	if msg == "" {
		players, err := a.store.PlayersByIDs(club.ID, playerIDsFromEntries(form.Entries))
		if err != nil {
			http.Error(w, "db error", http.StatusInternalServerError)
			return
		}
		playerEmoji := make(map[int]string, len(players))
		for _, p := range players {
			playerEmoji[p.ID] = p.Emoji
		}
		resp.Summary = scoreSummary(form.Melding, form.Entries, playerEmoji)
		resp.Valid = true
	}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		slog.Error("encode game preview", "err", err)
	}
}

func (a *App) handleSettlementExamplePreview(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	typ := normalizeSettlementType(r.FormValue("default_settlement_type"))
	if typ == "" {
		typ = normalizeSettlementType(club.DefaultSettlementType)
	}
	if typ != settlementTypePoints && typ != settlementTypeCommonDebt {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(settlementPreviewResponse{Error: "Vælg en gyldig standardafregning."})
		return
	}
	amountCents, ok := parseAmountCents(strings.TrimSpace(r.FormValue("default_settlement_amount")))
	if !ok {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(settlementPreviewResponse{Error: "Standardbeløb skal være et positivt tal med højst to decimaler."})
		return
	}
	equalPercent := club.CommonDebtEqualPercent
	if raw := strings.TrimSpace(r.FormValue("common_debt_equal_percent")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			equalPercent = n
		} else {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(settlementPreviewResponse{Error: "Fælles andel skal være et tal fra 0 til 100."})
			return
		}
	}
	rows, err := a.settlementExampleRowsFor(r, club, typ, amountCents, equalPercent)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(settlementPreviewResponse{Error: settlementErrorMessage(err)})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(settlementPreviewResponse{Rows: rows}); err != nil {
		slog.Error("encode settlement preview", "err", err)
	}
}

func (a *App) settlementExampleRowsFor(r *http.Request, club db.Club, typ string, amountCents, commonDebtEqualPercent int) ([]settlementExampleRow, error) {
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
	calculated, err := calculateSettlementRows(typ, amountCents, commonDebtEqualPercent, settlementRows)
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

func playerIDsFromEntries(entries []game.PlayerEntry) []int {
	ids := make([]int, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.PlayerID)
	}
	return ids
}

func scoreSummary(melding db.Melding, entries []game.PlayerEntry, labels map[int]string) string {
	issues := game.ValidateEntries(melding.Type, entries)
	if msg := gameEntryMessage(melding.Type, issues); msg != "" {
		return msg
	}
	scores := game.ComputeScores(melding.Type, melding.Bid, melding.Points, entries)
	parts := make([]string, 0, len(entries))
	for _, entry := range entries {
		label := labels[entry.PlayerID]
		if label == "" {
			label = strconv.Itoa(entry.PlayerID)
		}
		parts = append(parts, label+" "+formatScore(scores[entry.PlayerID]))
	}
	return strings.Join(parts, " · ")
}

func formatScore(n int) string {
	if n >= 0 {
		return fmt.Sprintf("+%d", n)
	}
	return strconv.Itoa(n)
}

func draftSummary(d db.Draft, m db.Melding, players []db.Player) string {
	labels := make(map[int]string, len(players))
	for _, p := range players {
		labels[p.ID] = p.Emoji
	}
	entries := make([]game.PlayerEntry, 0, len(d.Scores))
	for _, sc := range d.Scores {
		entries = append(entries, game.PlayerEntry{
			PlayerID: sc.PlayerID,
			Role:     sc.Role,
			Tricks:   sc.Tricks,
		})
	}
	return scoreSummary(m, entries, labels)
}
