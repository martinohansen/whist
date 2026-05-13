package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/martinohansen/whist/internal/db"
	"github.com/martinohansen/whist/internal/mistral"
)

// importEnabled reports whether the Mistral key is configured. When false,
// import routes 404 to avoid exposing dead UI.
func (a *App) importEnabled() bool {
	return a.mistral != nil && a.mistral.Enabled()
}

type importReviewData struct {
	layoutData
	Players  []db.Player
	Meldings []db.Melding
	Drafts   []draftView
	BulkDate string
	Error    string
	Success  string
}

// draftView is a draft enriched with derived fields for the template.
type draftView struct {
	db.Draft
	PlayedAtStr string // ISO date for <input type=date>
	Valid       bool
	Issues      []string
}

func (a *App) handleAnalyzeImport(w http.ResponseWriter, r *http.Request, club db.Club) {
	if !a.importEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseMultipartForm(20 << 20); err != nil {
		a.renderImportError(w, r, club, "Billedet er for stort eller ugyldigt.")
		return
	}
	files := r.MultipartForm.File["image"]
	if len(files) == 0 {
		a.renderImportError(w, r, club, "Vælg mindst ét billede.")
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()

	var pages []string
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			a.renderImportError(w, r, club, "Kunne ikke læse fil.")
			return
		}
		buf, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			a.renderImportError(w, r, club, "Kunne ikke læse fil.")
			return
		}
		mime := fh.Header.Get("Content-Type")
		md, err := a.mistral.OCR(ctx, buf, mime)
		if err != nil {
			slog.Error("mistral ocr", "err", err, "file", fh.Filename)
			a.renderImportError(w, r, club, "OCR fejlede.")
			return
		}
		pages = append(pages, md)
	}
	markdown := strings.Join(pages, "\n\n---\n\n")

	meldings, err := a.store.ListMeldings(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	players, err := a.store.ListPlayers(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	games, err := a.mistral.Extract(ctx, markdown, meldings, players)
	if err != nil {
		slog.Error("mistral extract", "err", err)
		a.renderImportError(w, r, club, "Kunne ikke analysere indhold.")
		return
	}
	if len(games) == 0 {
		a.renderImportError(w, r, club, "Ingen spil fundet på billedet.")
		return
	}

	drafts := buildDrafts(games, meldings, players)
	batchID, _ := randomBatchID()
	if err := a.store.AddDrafts(club.ID, batchID, drafts); err != nil {
		slog.Error("add drafts", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, clubPath(&club, "import/review"), http.StatusSeeOther)
}

func (a *App) handleReviewImport(w http.ResponseWriter, r *http.Request, club db.Club) {
	if !a.importEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.renderReview(w, r, club, "", "")
}

func (a *App) renderReview(w http.ResponseWriter, r *http.Request, club db.Club, errMsg, successMsg string) {
	drafts, err := a.store.ListPendingDrafts(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	meldings, err := a.store.ListMeldings(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	players, err := a.store.ListPlayers(club.ID)
	if err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}

	meldingByID := make(map[int]db.Melding, len(meldings))
	for _, m := range meldings {
		meldingByID[m.ID] = m
	}
	views := make([]draftView, 0, len(drafts))
	for _, d := range drafts {
		dv := draftView{Draft: d}
		if !d.PlayedAt.IsZero() {
			dv.PlayedAtStr = d.PlayedAt.Format(dateLayout)
		}
		dv.Issues = validateDraft(d, meldingByID)
		dv.Valid = len(dv.Issues) == 0
		views = append(views, dv)
	}

	data := importReviewData{
		layoutData: a.newLayout(r, club.Name+" — Gennemgå drafts", clubPath(&club, "import/review"), &club),
		Players:    players,
		Meldings:   meldings,
		Drafts:     views,
		BulkDate:   time.Now().Format(dateLayout),
		Error:      errMsg,
		Success:    successMsg,
	}
	renderTemplate(w, "layout", data, "templates/layout.html", "templates/import_review.html")
}

func (a *App) handleSaveDraft(w http.ResponseWriter, r *http.Request, club db.Club, draftID int) {
	if !a.importEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}

	playedAt, _ := parsePlayedAt(r.FormValue("played_at"))
	meldingID, _ := strconv.Atoi(strings.TrimSpace(r.FormValue("melding_id")))
	note := strings.TrimSpace(r.FormValue("note"))

	// Up to 4 positions, each carries: player_id (int or 0), role, tricks.
	var scores []db.DraftScore
	for i := range 4 {
		idx := strconv.Itoa(i)
		pidStr := strings.TrimSpace(r.FormValue("player_id_" + idx))
		role := strings.TrimSpace(r.FormValue("role_" + idx))
		tricksStr := strings.TrimSpace(r.FormValue("tricks_" + idx))
		rawName := strings.TrimSpace(r.FormValue("raw_name_" + idx))
		if pidStr == "" && role == "" && tricksStr == "" && rawName == "" {
			continue
		}
		pid, _ := strconv.Atoi(pidStr)
		tricks, _ := strconv.Atoi(tricksStr)
		scores = append(scores, db.DraftScore{
			Position: i,
			PlayerID: pid,
			RawName:  rawName,
			Role:     role,
			Tricks:   tricks,
		})
	}

	if err := a.store.UpdateDraft(club.ID, draftID, playedAt, meldingID, note, scores); err != nil {
		if errors.Is(err, db.ErrNotFound) {
			http.NotFound(w, r)
			return
		}
		slog.Error("update draft", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, clubPath(&club, "import/review"), http.StatusSeeOther)
}

func (a *App) handleDeleteDraft(w http.ResponseWriter, r *http.Request, club db.Club, draftID int) {
	if !a.importEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.store.DeleteDraft(club.ID, draftID); err != nil && !errors.Is(err, db.ErrNotFound) {
		slog.Error("delete draft", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, clubPath(&club, "import/review"), http.StatusSeeOther)
}

func (a *App) handleApproveDrafts(w http.ResponseWriter, r *http.Request, club db.Club) {
	if !a.importEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	created, skipped, err := a.store.ApproveDrafts(club.ID)
	if err != nil {
		slog.Error("approve drafts", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	if len(skipped) > 0 {
		a.renderReview(w, r, club,
			"Nogle drafts kunne ikke godkendes (manglende felter eller ugyldige stik).", "")
		return
	}
	if created == 0 {
		http.Redirect(w, r, clubPath(&club, "import/review"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, clubPath(&club, "games"), http.StatusSeeOther)
}

// renderImportError redirects back to the new-game page with the error
// surfaced as a query param. handleNewGame reads it and renders the message.
func (a *App) renderImportError(w http.ResponseWriter, r *http.Request, club db.Club, msg string) {
	v := url.Values{}
	v.Set("import_error", msg)
	http.Redirect(w, r, clubPath(&club, "new")+"?"+v.Encode(), http.StatusSeeOther)
}

func (a *App) handleRejectDrafts(w http.ResponseWriter, r *http.Request, club db.Club) {
	if !a.importEnabled() {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := a.store.RejectPendingDrafts(club.ID); err != nil {
		slog.Error("reject drafts", "err", err)
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, clubPath(&club, "new"), http.StatusSeeOther)
}

// buildDrafts maps Mistral output to db.Draft rows, resolving melding/player
// names to IDs case-insensitively. Unresolved names keep RawName set with
// PlayerID=0; the review UI prompts the user to pick.
func buildDrafts(games []mistral.DraftGame, meldings []db.Melding, players []db.Player) []db.Draft {
	meldByName := make(map[string]db.Melding, len(meldings))
	for _, m := range meldings {
		meldByName[strings.ToLower(strings.TrimSpace(m.Name))] = m
	}
	playerByName := make(map[string]db.Player, len(players))
	for _, p := range players {
		playerByName[strings.ToLower(strings.TrimSpace(p.Name))] = p
	}

	out := make([]db.Draft, 0, len(games))
	for _, g := range games {
		d := db.Draft{
			MeldingName: g.MeldingName,
			Note:        g.Note,
			Status:      db.DraftStatusPending,
		}
		if g.PlayedAt != "" {
			if t, err := time.Parse(dateLayout, g.PlayedAt); err == nil {
				d.PlayedAt = t
			}
		}
		if d.PlayedAt.IsZero() {
			d.PlayedAt = time.Now()
		}
		if m, ok := meldByName[strings.ToLower(strings.TrimSpace(g.MeldingName))]; ok {
			d.MeldingID = m.ID
		}
		for i, p := range g.Players {
			if i >= 4 {
				break
			}
			sc := db.DraftScore{
				Position: i,
				RawName:  p.Name,
				Role:     normalizeRole(p.Role),
				Tricks:   p.Tricks,
			}
			if pl, ok := playerByName[strings.ToLower(strings.TrimSpace(p.Name))]; ok {
				sc.PlayerID = pl.ID
			}
			d.Scores = append(d.Scores, sc)
		}
		out = append(out, d)
	}
	return out
}

func normalizeRole(r string) string {
	switch strings.ToLower(strings.TrimSpace(r)) {
	case "melder":
		return "melder"
	case "makker":
		return "makker"
	case "modspil", "modspiller":
		return "modspil"
	}
	return ""
}

// validateDraft returns a list of human-readable issues. Empty list means
// the draft is ready for approval.
func validateDraft(d db.Draft, meldings map[int]db.Melding) []string {
	var issues []string
	if d.MeldingID == 0 {
		issues = append(issues, "Vælg en melding")
	}
	if len(d.Scores) != 4 {
		issues = append(issues, "Skal have 4 spillere")
	}
	var melder, makker, modspil int
	var sum int
	for _, sc := range d.Scores {
		if sc.PlayerID == 0 {
			issues = append(issues, "Vælg spiller for "+sc.RawName)
		}
		switch sc.Role {
		case "melder":
			melder++
		case "makker":
			makker++
		case "modspil":
			modspil++
		default:
			issues = append(issues, "Mangler rolle")
		}
		sum += sc.Tricks
	}
	m, hasMelding := meldings[d.MeldingID]
	if d.MeldingID != 0 && !hasMelding {
		issues = append(issues, "Ukendt melding")
	}
	if melder != 1 {
		issues = append(issues, "Præcis én melder")
	}
	if hasMelding && m.Type == db.MeldingTypeNolo {
		if makker+modspil != 3 {
			issues = append(issues, "Skal have tre makkere/modspil")
		}
	} else if d.MeldingID != 0 {
		if makker != 1 {
			issues = append(issues, "Præcis én makker")
		}
		if modspil != 2 {
			issues = append(issues, "Præcis to modspil")
		}
		if sum != 13 {
			issues = append(issues, "Stik skal være 13 i alt")
		}
	}
	return issues
}

func randomBatchID() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
