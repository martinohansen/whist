package main

import (
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/martinohansen/whist/internal/db"
)

const (
	settlementTypePoints     = "points"
	settlementTypeCommonDebt = "common_debt"
)

type settlementRow struct {
	PlayerID    int
	PlayerName  string
	PlayerEmoji string
	Points      int
	AmountCents int
}

type settlementPreview struct {
	Type            string
	AmountCents     int
	FromGameID      int
	FirstGameID     int
	FirstGameDate   string
	ThroughGameID   int
	ThroughGameDate string
	Rows            []settlementRow
}

type settlementsData struct {
	layoutData
	Type                   string
	AmountInput            string
	HasAmount              bool
	ThroughGameID          int
	AvailableGames         []db.Game
	CommonDebtEqualPercent int
	Preview                settlementPreview
	History                []db.Settlement
	Totals                 []settlementTotalRow
	TotalAmountCents       int
	Error                  string
	Success                string
}

type settlementTotalRow struct {
	PlayerID    int
	PlayerName  string
	PlayerEmoji string
	AmountCents int
}

var (
	errSettlementNoGames      = errors.New("no unsettled games")
	errSettlementNoAmount     = errors.New("no amount")
	errSettlementBadType      = errors.New("bad settlement type")
	errSettlementBadThrough   = errors.New("bad through game")
	errSettlementNeedsSides   = errors.New("point settlement needs positive and negative points")
	errSettlementNeedsDebtors = errors.New("settlement needs negative points")
)

func (a *App) handleSettlements(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.renderSettlements(w, r, club, "", "")
}

func (a *App) handleBookSettlement(w http.ResponseWriter, r *http.Request, club db.Club) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	typ := normalizeSettlementType(r.FormValue("type"))
	amountCents, ok := parseAmountCents(r.FormValue("amount"))
	if !ok {
		a.renderSettlements(w, r, club, "Beløbet skal være et positivt tal med højst to decimaler.", "")
		return
	}
	throughGameID, ok := parseOptionalPositiveInt(r.FormValue("through_game_id"))
	if !ok {
		a.renderSettlements(w, r, club, "Vælg et gyldigt slutspil.", "")
		return
	}
	preview, err := a.loadSettlementPreview(club.ID, typ, amountCents, throughGameID, club.CommonDebtEqualPercent)
	if err != nil {
		a.renderSettlements(w, r, club, settlementErrorMessage(err), "")
		return
	}
	settlement := db.Settlement{
		Type:          preview.Type,
		AmountCents:   preview.AmountCents,
		FromGameID:    preview.FromGameID,
		ThroughGameID: preview.ThroughGameID,
		Rows:          settlementRowsForDB(preview.Rows),
	}
	if _, err := a.store.AddSettlement(club.ID, settlement); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, clubPathForRequest(r, &club, "settlements"), http.StatusSeeOther)
}

func (a *App) renderSettlements(w http.ResponseWriter, r *http.Request, club db.Club, errMsg, okMsg string) {
	latest, hasLatest, err := a.store.LatestSettlement(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	_, hasTypeParam := formValue(r, "type")
	typ := normalizeSettlementType(r.FormValue("type"))
	if !hasTypeParam {
		typ = normalizeSettlementType(club.DefaultSettlementType)
	}
	if typ == "" {
		typ = settlementTypeCommonDebt
	}
	amountInput, hasAmountParam := formValue(r, "amount")
	amountInput = strings.TrimSpace(amountInput)
	if !hasAmountParam {
		amountInput = formatAmountInput(club.DefaultSettlementAmountCents)
	}
	amountCents, hasAmount := parseAmountCents(amountInput)
	if amountInput != "" && !hasAmount && errMsg == "" {
		errMsg = "Beløbet skal være et positivt tal med højst to decimaler."
	}

	fromGameID := 0
	if hasLatest {
		fromGameID = latest.ThroughGameID
	}
	availableGames, err := a.store.SettlementGamesSince(club.ID, fromGameID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	throughGameID, throughOK := parseOptionalPositiveInt(r.FormValue("through_game_id"))
	if !throughOK && errMsg == "" {
		errMsg = "Vælg et gyldigt slutspil."
	}
	if throughGameID == 0 && len(availableGames) > 0 {
		throughGameID = availableGames[len(availableGames)-1].ID
	}

	var preview settlementPreview
	if amountInput == "" || hasAmount {
		amount := amountCents
		if amountInput == "" {
			amount = 0
		}
		var err error
		preview, err = a.loadSettlementPreview(club.ID, typ, amount, throughGameID, club.CommonDebtEqualPercent)
		if err != nil {
			if errMsg == "" && !errors.Is(err, errSettlementNoAmount) {
				errMsg = settlementErrorMessage(err)
			}
			if errors.Is(err, errSettlementNoAmount) {
				preview, _ = a.loadSettlementRows(club.ID, typ, throughGameID)
			}
		}
	}
	history, err := a.store.ListSettlements(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	ctx, err := a.loadSeasonContext(r, club)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	history = filterSettlementsBySeason(history, seasonFilter(ctx.Selected))
	totals, totalAmountCents := settlementTotals(history)
	data := settlementsData{
		layoutData:             a.newLayout(r, club.Name+" — Afregn", clubPath(&club, "settlements"), &club),
		Type:                   typ,
		AmountInput:            amountInput,
		HasAmount:              hasAmount,
		ThroughGameID:          throughGameID,
		AvailableGames:         availableGames,
		CommonDebtEqualPercent: club.CommonDebtEqualPercent,
		Preview:                preview,
		History:                history,
		Totals:                 totals,
		TotalAmountCents:       totalAmountCents,
		Error:                  errMsg,
		Success:                okMsg,
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/settlements.html")
}

func (a *App) loadSettlementRows(clubID, typ string, throughGameID int) (settlementPreview, error) {
	latest, ok, err := a.store.LatestSettlement(clubID)
	if err != nil {
		return settlementPreview{}, err
	}
	fromGameID := 0
	if ok {
		fromGameID = latest.ThroughGameID
	}
	games, err := a.store.SettlementGamesSince(clubID, fromGameID)
	if err != nil {
		return settlementPreview{}, err
	}
	if len(games) == 0 {
		return settlementPreview{}, errSettlementNoGames
	}
	if throughGameID == 0 {
		throughGameID = games[len(games)-1].ID
	}
	if !gameIDInList(games, throughGameID) {
		return settlementPreview{}, errSettlementBadThrough
	}
	points, err := a.store.SettlementPointsBetween(clubID, fromGameID, throughGameID)
	if err != nil {
		return settlementPreview{}, err
	}
	firstGame := games[0]
	throughGame := games[len(games)-1]
	for _, game := range games {
		if game.ID <= throughGameID {
			throughGame = game
		}
		if game.ID > fromGameID {
			firstGame = game
			break
		}
	}
	rows := make([]settlementRow, 0, len(points))
	for _, point := range points {
		rows = append(rows, settlementRow{
			PlayerID:    point.PlayerID,
			PlayerName:  point.PlayerName,
			PlayerEmoji: point.PlayerEmoji,
			Points:      point.Points,
		})
	}
	return settlementPreview{
		Type:            typ,
		FromGameID:      fromGameID,
		FirstGameID:     firstGame.ID,
		FirstGameDate:   firstGame.PlayedAt.Format(dateLayout),
		ThroughGameID:   throughGameID,
		ThroughGameDate: throughGame.PlayedAt.Format(dateLayout),
		Rows:            rows,
	}, nil
}

func (a *App) loadSettlementPreview(clubID, typ string, amountCents, throughGameID, commonDebtEqualPercent int) (settlementPreview, error) {
	if typ != settlementTypePoints && typ != settlementTypeCommonDebt {
		return settlementPreview{}, errSettlementBadType
	}
	if amountCents <= 0 {
		return settlementPreview{}, errSettlementNoAmount
	}
	preview, err := a.loadSettlementRows(clubID, typ, throughGameID)
	if err != nil {
		return settlementPreview{}, err
	}
	preview.AmountCents = amountCents
	rows, err := calculateSettlementRows(typ, amountCents, commonDebtEqualPercent, preview.Rows)
	if err != nil {
		return settlementPreview{}, err
	}
	preview.Rows = rows
	return preview, nil
}

func calculateSettlementRows(typ string, amountCents, commonDebtEqualPercent int, rows []settlementRow) ([]settlementRow, error) {
	if typ != settlementTypePoints && typ != settlementTypeCommonDebt {
		return nil, errSettlementBadType
	}
	if amountCents <= 0 {
		return nil, errSettlementNoAmount
	}
	out := append([]settlementRow(nil), rows...)
	var positives, negatives []weightedSettlementRow
	for i, row := range out {
		switch {
		case row.Points > 0:
			positives = append(positives, weightedSettlementRow{index: i, playerID: row.PlayerID, weight: row.Points})
		case row.Points < 0:
			negatives = append(negatives, weightedSettlementRow{index: i, playerID: row.PlayerID, weight: -row.Points})
		}
	}
	if typ == settlementTypeCommonDebt {
		if commonDebtEqualPercent < 0 || commonDebtEqualPercent > 100 {
			return nil, errSettlementBadType
		}
		commonDebtAmounts := calculateCommonDebtAmounts(amountCents, commonDebtEqualPercent, out)
		for index, cents := range commonDebtAmounts {
			out[index].AmountCents = -cents
		}
		return out, nil
	}
	if len(negatives) == 0 {
		return nil, errSettlementNeedsDebtors
	}
	negativeAmounts := allocateCents(amountCents, negatives)
	for index, cents := range negativeAmounts {
		out[index].AmountCents = -cents
	}
	if len(positives) == 0 {
		return nil, errSettlementNeedsSides
	}
	positiveAmounts := allocateCents(amountCents, positives)
	for index, cents := range positiveAmounts {
		out[index].AmountCents = cents
	}
	return out, nil
}

func calculateCommonDebtAmounts(amountCents, equalPercent int, rows []settlementRow) map[int]int {
	equalTotal := amountCents * equalPercent / 100
	gapTotal := amountCents - equalTotal
	participants := make([]weightedSettlementRow, 0, len(rows))
	bestPoints := rows[0].Points
	for _, row := range rows[1:] {
		if row.Points > bestPoints {
			bestPoints = row.Points
		}
	}
	gaps := make([]weightedSettlementRow, 0, len(rows))
	totalGap := 0
	for i, row := range rows {
		participants = append(participants, weightedSettlementRow{index: i, playerID: row.PlayerID, weight: 1})
		gap := bestPoints - row.Points
		totalGap += gap
		gaps = append(gaps, weightedSettlementRow{index: i, playerID: row.PlayerID, weight: gap})
	}
	out := allocateCents(equalTotal, participants)
	if gapTotal == 0 {
		return out
	}
	if totalGap == 0 {
		gaps = participants
	}
	gapAmounts := allocateCents(gapTotal, gaps)
	for index, cents := range gapAmounts {
		out[index] += cents
	}
	return out
}

type weightedSettlementRow struct {
	index    int
	playerID int
	weight   int
}

func allocateCents(total int, rows []weightedSettlementRow) map[int]int {
	sumWeights := 0
	for _, row := range rows {
		sumWeights += row.weight
	}
	type remainder struct {
		index     int
		playerID  int
		remainder int
	}
	out := make(map[int]int, len(rows))
	remainders := make([]remainder, 0, len(rows))
	allocated := 0
	for _, row := range rows {
		product := total * row.weight
		cents := product / sumWeights
		out[row.index] = cents
		allocated += cents
		remainders = append(remainders, remainder{
			index:     row.index,
			playerID:  row.playerID,
			remainder: product % sumWeights,
		})
	}
	sort.SliceStable(remainders, func(i, j int) bool {
		if remainders[i].remainder != remainders[j].remainder {
			return remainders[i].remainder > remainders[j].remainder
		}
		return remainders[i].playerID < remainders[j].playerID
	})
	for i := 0; i < total-allocated; i++ {
		out[remainders[i].index]++
	}
	return out
}

func normalizeSettlementType(raw string) string {
	switch strings.TrimSpace(raw) {
	case "", settlementTypePoints:
		return settlementTypePoints
	case settlementTypeCommonDebt:
		return settlementTypeCommonDebt
	default:
		return strings.TrimSpace(raw)
	}
}

func parseAmountCents(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	raw = strings.ReplaceAll(raw, ".", ",")
	parts := strings.Split(raw, ",")
	if len(parts) > 2 || parts[0] == "" {
		return 0, false
	}
	kroner, err := strconv.Atoi(parts[0])
	if err != nil || kroner < 0 {
		return 0, false
	}
	øre := 0
	if len(parts) == 2 {
		if len(parts[1]) == 0 || len(parts[1]) > 2 {
			return 0, false
		}
		for len(parts[1]) < 2 {
			parts[1] += "0"
		}
		øre, err = strconv.Atoi(parts[1])
		if err != nil {
			return 0, false
		}
	}
	cents := kroner*100 + øre
	return cents, cents > 0
}

func parseOptionalPositiveInt(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	n, err := strconv.Atoi(raw)
	return n, err == nil && n > 0
}

func formValue(r *http.Request, key string) (string, bool) {
	if values, ok := r.URL.Query()[key]; ok {
		if len(values) == 0 {
			return "", true
		}
		return values[0], true
	}
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err == nil {
			if values, ok := r.Form[key]; ok {
				if len(values) == 0 {
					return "", true
				}
				return values[0], true
			}
		}
	}
	return "", false
}

func formatAmountInput(cents int) string {
	return strconv.Itoa(cents/100) + "," + fmt.Sprintf("%02d", cents%100)
}

func gameIDInList(games []db.Game, id int) bool {
	for _, game := range games {
		if game.ID == id {
			return true
		}
	}
	return false
}

func filterSettlementsBySeason(settlements []db.Settlement, filter db.LeaderboardFilter) []db.Settlement {
	if filter.After.IsZero() && filter.Until.IsZero() {
		return settlements
	}
	out := settlements[:0]
	for _, settlement := range settlements {
		if settlement.ThroughGamePlayedAt.IsZero() {
			continue
		}
		if !filter.After.IsZero() && settlement.ThroughGamePlayedAt.Before(filter.After) {
			continue
		}
		if !filter.Until.IsZero() && settlement.ThroughGamePlayedAt.After(filter.Until) {
			continue
		}
		out = append(out, settlement)
	}
	return out
}

func settlementTotals(settlements []db.Settlement) ([]settlementTotalRow, int) {
	type acc struct {
		row   settlementTotalRow
		order int
	}
	byPlayer := map[int]*acc{}
	totalAmountCents := 0
	order := 0
	for _, settlement := range settlements {
		totalAmountCents += settlement.AmountCents
		for _, row := range settlement.Rows {
			current, ok := byPlayer[row.PlayerID]
			if !ok {
				current = &acc{
					row: settlementTotalRow{
						PlayerID:    row.PlayerID,
						PlayerName:  row.PlayerName,
						PlayerEmoji: row.PlayerEmoji,
					},
					order: order,
				}
				order++
				byPlayer[row.PlayerID] = current
			}
			current.row.AmountCents += row.AmountCents
		}
	}
	out := make([]settlementTotalRow, 0, len(byPlayer))
	for _, current := range byPlayer {
		out = append(out, current.row)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].AmountCents != out[j].AmountCents {
			return out[i].AmountCents > out[j].AmountCents
		}
		return out[i].PlayerName < out[j].PlayerName
	})
	return out, totalAmountCents
}

func settlementRowsForDB(rows []settlementRow) []db.SettlementRow {
	out := make([]db.SettlementRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, db.SettlementRow{
			PlayerID:    row.PlayerID,
			PlayerName:  row.PlayerName,
			PlayerEmoji: row.PlayerEmoji,
			Points:      row.Points,
			AmountCents: row.AmountCents,
		})
	}
	return out
}

func settlementErrorMessage(err error) string {
	switch {
	case errors.Is(err, errSettlementNoGames):
		return "Der er ingen endnu ikke afregnede spil."
	case errors.Is(err, errSettlementBadType):
		return "Vælg en gyldig afregningstype."
	case errors.Is(err, errSettlementBadThrough):
		return "Vælg et endnu ikke afregnet slutspil."
	case errors.Is(err, errSettlementNoAmount):
		return "Beløbet skal være større end 0."
	case errors.Is(err, errSettlementNeedsSides):
		return "Pointafregning kræver både positive og negative point."
	case errors.Is(err, errSettlementNeedsDebtors):
		return "Afregning kræver mindst én spiller med negative point."
	default:
		return "Kunne ikke beregne afregning."
	}
}
