package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/martinohansen/whist/internal/db"
	"github.com/martinohansen/whist/internal/game"
	"github.com/martinohansen/whist/internal/mistral"
)

// newTestApp returns an App backed by an in-memory sqlite store.
func newTestApp(t *testing.T) (*App, *db.Store) {
	t.Helper()
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return newApp(store, nil), store
}

type enabledImportClient struct{}

func (enabledImportClient) Enabled() bool { return true }

func (enabledImportClient) OCR(_ context.Context, _ []byte, _ string) (string, error) {
	return "", nil
}

func (enabledImportClient) Extract(_ context.Context, _ string, _ []db.Melding, _ []db.Player) ([]mistral.DraftGame, error) {
	return nil, nil
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, h http.Handler, path string, form url.Values) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		body, _ := io.ReadAll(rec.Body)
		t.Fatalf("status %d (want %d): %s", rec.Code, want, body)
	}
}

// createClub posts the create-club form and returns the new club's ID.
func createClub(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	rec := post(t, h, "/clubs/new", url.Values{"name": {name}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("create club: got %d", rec.Code)
	}
	loc := rec.Header().Get("Location")
	id := strings.TrimPrefix(loc, "/c/")
	if !db.ValidID(id) {
		t.Fatalf("bad club id in Location %q", loc)
	}
	return id
}

func TestHomeRenders(t *testing.T) {
	app, _ := newTestApp(t)
	h := app.routes()
	rec := get(t, h, "/")
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, "Opret klub") {
		t.Errorf("home missing 'Opret klub'")
	}
	for _, want := range []string{
		`<meta name="description"`,
		`<link rel="canonical" href="https://example.com/"`,
		`<meta name="robots" content="index,follow"`,
		`"@type": "WebSite"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("home missing SEO marker %q", want)
		}
	}
}

func TestRobotsAndSitemap(t *testing.T) {
	app, _ := newTestApp(t)
	h := app.routes()

	rec := get(t, h, "/robots.txt")
	assertStatus(t, rec, http.StatusOK)
	robots := rec.Body.String()
	for _, want := range []string{
		"Allow: /stats\n",
		"Disallow: /c/\n",
		"Disallow: /go\n",
		"Sitemap: https://example.com/sitemap.xml\n",
	} {
		if !strings.Contains(robots, want) {
			t.Errorf("robots.txt missing %q; body=%s", want, robots)
		}
	}

	rec = get(t, h, "/sitemap.xml")
	assertStatus(t, rec, http.StatusOK)
	sitemap := rec.Body.String()
	for _, want := range []string{
		"<loc>https://example.com/</loc>",
	} {
		if !strings.Contains(sitemap, want) {
			t.Errorf("sitemap missing %q; body=%s", want, sitemap)
		}
	}
	if strings.Contains(robots, "Allow: /clubs\n") {
		t.Errorf("robots.txt still exposes /clubs; body=%s", robots)
	}
	if strings.Contains(sitemap, "<loc>https://example.com/clubs</loc>") {
		t.Errorf("sitemap still exposes /clubs; body=%s", sitemap)
	}
}

func TestCreateClubAndVisitAllPages(t *testing.T) {
	app, _ := newTestApp(t)
	h := app.routes()

	id := createClub(t, h, "Testklub")

	for _, path := range []string{"", "games", "settlements", "new", "settings"} {
		p := "/c/" + id
		if path != "" {
			p += "/" + path
		}
		rec := get(t, h, p)
		assertStatus(t, rec, http.StatusOK)
	}
}

func TestSettlementsPageRendersPreviewValidationTotalsAndHistory(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Testklub")
	if err := store.AddSeason(id, "Maj", "2026-05-01", "2026-05-31"); err != nil {
		t.Fatal(err)
	}
	seasons, err := store.ListSeasons(id)
	if err != nil || len(seasons) != 1 {
		t.Fatalf("list seasons: err=%v len=%d", err, len(seasons))
	}
	var players []db.Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		player, err := store.AddPlayer(id, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, player)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddGame(id, mustDate(t, "2026-05-10"), meldings[0], []game.PlayerEntry{
		{PlayerID: players[0].ID, Role: "melder", Tricks: 4},
		{PlayerID: players[1].ID, Role: "makker", Tricks: 3},
		{PlayerID: players[2].ID, Role: "modspil", Tricks: 3},
		{PlayerID: players[3].ID, Role: "modspil", Tricks: 3},
	}, "i valgt periode"); err != nil {
		t.Fatal(err)
	}

	seasonID := itoa(seasons[0].ID)
	rec := get(t, h, "/c/"+id+"/settlements?season="+seasonID+"&type=points&amount=400,00")
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, want := range []string{
		`>Spil</a>`,
		`>Afregn</a>`,
		`>Klubben</a>`,
		`href="/c/` + id + `/settlements?season=` + seasonID + `"`,
		`Pointafregning`,
		`200,00 kr.`,
		`-200,00 kr.`,
		`Bogfør`,
		`Fra spil #`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settlements page missing %q: %s", want, body)
		}
	}
	if !(strings.Index(body, ">Spil</a>") < strings.Index(body, ">Afregn</a>") &&
		strings.Index(body, ">Afregn</a>") < strings.Index(body, ">Klubben</a>")) {
		t.Fatalf("settlement nav is not between games and settings: %s", body)
	}

	rec = get(t, h, "/c/"+id+"/settlements?type=points&amount=ugyldigt")
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "Beløbet skal være et positivt tal") {
		t.Fatalf("missing amount validation: %s", rec.Body.String())
	}

	rec = post(t, h, "/c/"+id+"/settlements/book", url.Values{
		"type":   {"points"},
		"amount": {"400,00"},
		"season": {seasonID},
	})
	assertStatus(t, rec, http.StatusSeeOther)
	if got, want := rec.Header().Get("Location"), "/c/"+id+"/settlements?season="+seasonID; got != want {
		t.Fatalf("redirect=%q want %q", got, want)
	}

	rec = get(t, h, "/c/"+id+"/settlements")
	assertStatus(t, rec, http.StatusOK)
	body = rec.Body.String()
	for _, want := range []string{
		`Total`,
		`Samlet bogført i den valgte periode: 400,00 kr.`,
		`Historik`,
		`Pointafregning`,
		`fra spil #`,
		`400,00 kr.`,
		`Ingen endnu ikke bogførte spil.`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settlement history missing %q: %s", want, body)
		}
	}
}

func TestBookSettlementRejectsWithoutUnsettledGames(t *testing.T) {
	app, _ := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Testklub")

	rec := post(t, h, "/c/"+id+"/settlements/book", url.Values{
		"type":   {"points"},
		"amount": {"400,00"},
	})
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "Der er ingen endnu ikke afregnede spil.") {
		t.Fatalf("missing no-games validation: %s", rec.Body.String())
	}
}

func TestSettlementsHistoryAndTotalsFollowSelectedSeason(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Testklub")
	if err := store.AddSeason(id, "Januar", "2026-01-01", "2026-01-31"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSeason(id, "Maj", "2026-05-01", "2026-05-31"); err != nil {
		t.Fatal(err)
	}
	seasons, err := store.ListSeasons(id)
	if err != nil {
		t.Fatal(err)
	}
	seasonByName := map[string]int{}
	for _, season := range seasons {
		seasonByName[season.Name] = season.ID
	}
	var players []db.Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		player, err := store.AddPlayer(id, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, player)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	entries := []game.PlayerEntry{
		{PlayerID: players[0].ID, Role: "melder", Tricks: 4},
		{PlayerID: players[1].ID, Role: "makker", Tricks: 3},
		{PlayerID: players[2].ID, Role: "modspil", Tricks: 3},
		{PlayerID: players[3].ID, Role: "modspil", Tricks: 3},
	}
	janID, err := store.AddGame(id, mustDate(t, "2026-01-10"), meldings[0], entries, "")
	if err != nil {
		t.Fatal(err)
	}
	rec := post(t, h, "/c/"+id+"/settlements/book", url.Values{
		"type":            {"points"},
		"amount":          {"100,00"},
		"through_game_id": {itoa(janID)},
	})
	assertStatus(t, rec, http.StatusSeeOther)

	mayID, err := store.AddGame(id, mustDate(t, "2026-05-10"), meldings[0], entries, "")
	if err != nil {
		t.Fatal(err)
	}
	rec = post(t, h, "/c/"+id+"/settlements/book", url.Values{
		"type":            {"points"},
		"amount":          {"300,00"},
		"through_game_id": {itoa(mayID)},
	})
	assertStatus(t, rec, http.StatusSeeOther)

	rec = get(t, h, "/c/"+id+"/settlements?season="+itoa(seasonByName["Januar"]))
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, want := range []string{
		"Samlet bogført i den valgte periode: 100,00 kr.",
		"fra spil #" + itoa(janID) + " 2026-01-10",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("January settlement view missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "fra spil #"+itoa(mayID)+" 2026-05-10") {
		t.Fatalf("January settlement view includes May settlement: %s", body)
	}

	rec = get(t, h, "/c/"+id+"/settlements?season="+itoa(seasonByName["Maj"]))
	assertStatus(t, rec, http.StatusOK)
	body = rec.Body.String()
	for _, want := range []string{
		"Samlet bogført i den valgte periode: 300,00 kr.",
		"fra spil #" + itoa(mayID) + " 2026-05-10",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("May settlement view missing %q: %s", want, body)
		}
	}
	if strings.Contains(body, "fra spil #"+itoa(janID)+" 2026-01-10") {
		t.Fatalf("May settlement view includes January settlement: %s", body)
	}
}

func TestSettlementCutoffLeavesLaterGamesAndUsesClubDefaults(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Testklub")
	var players []db.Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		player, err := store.AddPlayer(id, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, player)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"name":                      {"Testklub"},
		"rules":                     {""},
		"default_settlement_type":   {"points"},
		"default_settlement_amount": {"123,45"},
		"common_debt_equal_percent": {"50"},
		"visibility":                {"private"},
	}
	for _, melding := range meldings {
		form.Add("melding_id", itoa(melding.ID))
		form.Add("melding_name", melding.Name)
		form.Add("melding_type", melding.Type)
		form.Add("melding_bid", itoa(melding.Bid))
		form.Add("melding_points", itoa(melding.Points))
	}
	rec := post(t, h, "/c/"+id+"/settings/save", form)
	assertStatus(t, rec, http.StatusOK)

	entries := []game.PlayerEntry{
		{PlayerID: players[0].ID, Role: "melder", Tricks: 4},
		{PlayerID: players[1].ID, Role: "makker", Tricks: 3},
		{PlayerID: players[2].ID, Role: "modspil", Tricks: 3},
		{PlayerID: players[3].ID, Role: "modspil", Tricks: 3},
	}
	firstID, err := store.AddGame(id, mustDate(t, "2026-05-10"), meldings[0], entries, "")
	if err != nil {
		t.Fatal(err)
	}
	secondID, err := store.AddGame(id, mustDate(t, "2026-05-11"), meldings[0], entries, "")
	if err != nil {
		t.Fatal(err)
	}

	rec = get(t, h, "/c/"+id+"/settlements?type=common_debt&amount=400,00&through_game_id="+itoa(firstID))
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, want := range []string{
		`option value="` + itoa(firstID) + `" selected`,
		`option value="` + itoa(secondID) + `"`,
		`50% deles ligeligt; resten fordeles efter afstand til vinderen.`,
		`Fra spil #` + itoa(firstID) + ` 2026-05-10 til #` + itoa(firstID) + ` 2026-05-10`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("cutoff preview missing %q: %s", want, body)
		}
	}

	rec = post(t, h, "/c/"+id+"/settlements/book", url.Values{
		"type":            {"common_debt"},
		"amount":          {"400,00"},
		"through_game_id": {itoa(firstID)},
	})
	assertStatus(t, rec, http.StatusSeeOther)

	rec = get(t, h, "/c/"+id+"/settlements")
	assertStatus(t, rec, http.StatusOK)
	body = rec.Body.String()
	for _, want := range []string{
		`<option value="points" selected>Pointafregning</option>`,
		`value="123,45"`,
		`option value="` + itoa(secondID) + `" selected`,
		`Fra spil #` + itoa(secondID) + ` 2026-05-11 til #` + itoa(secondID) + ` 2026-05-11`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("remembered defaults missing %q: %s", want, body)
		}
	}
}

func TestSettingsSavePersistsCommonDebtEqualPercent(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Testklub")
	var players []db.Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		player, err := store.AddPlayer(id, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, player)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddGame(id, mustDate(t, "2026-05-10"), meldings[0], []game.PlayerEntry{
		{PlayerID: players[0].ID, Role: "melder", Tricks: 4},
		{PlayerID: players[1].ID, Role: "makker", Tricks: 3},
		{PlayerID: players[2].ID, Role: "modspil", Tricks: 3},
		{PlayerID: players[3].ID, Role: "modspil", Tricks: 3},
	}, ""); err != nil {
		t.Fatal(err)
	}

	rec := get(t, h, "/c/"+id+"/settings")
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, `name="common_debt_equal_percent"`) {
		t.Fatalf("settings missing common debt field: %s", body)
	}
	if !strings.Contains(body, `name="default_settlement_type"`) || !strings.Contains(body, `name="default_settlement_amount"`) {
		t.Fatalf("settings missing settlement defaults: %s", body)
	}
	if !strings.Contains(body, "Resten fordeles efter hvor langt hver spiller er fra periodens vinder.") {
		t.Fatalf("settings missing common debt explanation: %s", body)
	}
	if !strings.Contains(body, "Eksempel med nuværende stilling") {
		t.Fatalf("settings missing common debt example: %s", body)
	}
	if !(strings.Index(body, "Offentlige klubber kræver") < strings.Index(body, "Regler")) {
		t.Fatalf("visibility was not moved under Klub before Regler: %s", body)
	}
	if !(strings.Index(body, `id="vis-private"`) < strings.Index(body, `id="vis-public"`)) {
		t.Fatalf("visibility toggle order was not swapped: %s", body)
	}
	form := url.Values{
		"name":                      {"Testklub"},
		"rules":                     {""},
		"default_settlement_type":   {"points"},
		"default_settlement_amount": {"123,45"},
		"common_debt_equal_percent": {"75"},
		"visibility":                {"private"},
	}
	for _, melding := range meldings {
		form.Add("melding_id", itoa(melding.ID))
		form.Add("melding_name", melding.Name)
		form.Add("melding_type", melding.Type)
		form.Add("melding_bid", itoa(melding.Bid))
		form.Add("melding_points", itoa(melding.Points))
	}
	rec = post(t, h, "/c/"+id+"/settings/save", form)
	assertStatus(t, rec, http.StatusOK)
	club, err := store.GetClub(id)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := club.CommonDebtEqualPercent, 75; got != want {
		t.Fatalf("common debt equal percent=%d want %d", got, want)
	}
	if got, want := club.DefaultSettlementType, "points"; got != want {
		t.Fatalf("default settlement type=%q want %q", got, want)
	}
	if got, want := club.DefaultSettlementAmountCents, 12345; got != want {
		t.Fatalf("default settlement amount=%d want %d", got, want)
	}
	rec = get(t, h, "/c/"+id+"/settings")
	assertStatus(t, rec, http.StatusOK)
	body = rec.Body.String()
	if !strings.Contains(body, `id="common-debt-equal-field"`) ||
		!strings.Contains(body, `is-disabled`) ||
		!strings.Contains(body, `id="common-debt-equal-percent"`) ||
		!strings.Contains(body, `disabled`) {
		t.Fatalf("common debt share was not disabled for point settlement: %s", body)
	}
}

func TestNewGamePageHasNoCancelLink(t *testing.T) {
	app, _ := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Testklub")

	rec := get(t, h, "/c/"+id+"/new")
	assertStatus(t, rec, http.StatusOK)
	if strings.Contains(rec.Body.String(), "Annuller") {
		t.Fatalf("new game page still renders cancel link: %s", rec.Body.String())
	}
}

func TestAddPlayerAndPlayGame(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()

	id := createClub(t, h, "Testklub")

	// Add 4 players.
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		rec := post(t, h, "/c/"+id+"/players/add", url.Values{"name": {name}})
		assertStatus(t, rec, http.StatusSeeOther)
	}

	players, err := store.ListPlayers(id)
	if err != nil || len(players) != 4 {
		t.Fatalf("list players: err=%v len=%d", err, len(players))
	}

	meldings, err := store.ListMeldings(id)
	if err != nil || len(meldings) == 0 {
		t.Fatalf("list meldings: err=%v len=%d", err, len(meldings))
	}
	// Use bid=8 (Position 2 in defaults).
	var m8 db.Melding
	for _, m := range meldings {
		if m.Bid == 8 {
			m8 = m
			break
		}
	}
	if m8.ID == 0 {
		t.Fatalf("no bid=8 melding in defaults")
	}

	form := url.Values{
		"played_at":  {"2026-05-11"},
		"melding_id": {itoa(m8.ID)},
	}
	for i, p := range players {
		pid := itoa(p.ID)
		form.Add("player_id", pid)
		switch i {
		case 0:
			form.Set("role_"+pid, "melder")
			form.Set("tricks_"+pid, "5")
		case 1:
			form.Set("role_"+pid, "makker")
			form.Set("tricks_"+pid, "4")
		case 2, 3:
			form.Set("role_"+pid, "modspil")
			form.Set("tricks_"+pid, "2")
		}
	}
	rec := post(t, h, "/c/"+id+"/games/save", form)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("save game: status=%d body=%s", rec.Code, rec.Body.String())
	}

	games, err := store.ListGames(id)
	if err != nil || len(games) != 1 {
		t.Fatalf("list games: err=%v len=%d", err, len(games))
	}
	// melder+makker over by 1 trick → points × (1+1) = 4 each (points=2 for bid 8)
	for _, sc := range games[0].Scores {
		want := -4
		if sc.Role == "melder" || sc.Role == "makker" {
			want = 4
		}
		if sc.Score != want {
			t.Errorf("player %s role %s: score=%d want=%d", sc.Player.Name, sc.Role, sc.Score, want)
		}
	}
}

func TestGameDetailAllowsEditingAndBrowsingAdjacentGames(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Testklub")

	var players []db.Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		player, err := store.AddPlayer(id, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, player)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	entries := []game.PlayerEntry{
		{PlayerID: players[0].ID, Role: "melder", Tricks: 4},
		{PlayerID: players[1].ID, Role: "makker", Tricks: 3},
		{PlayerID: players[2].ID, Role: "modspil", Tricks: 3},
		{PlayerID: players[3].ID, Role: "modspil", Tricks: 3},
	}
	olderID, err := store.AddGame(id, mustDate(t, "2026-05-10"), meldings[0], entries, "")
	if err != nil {
		t.Fatal(err)
	}
	currentID, err := store.AddGame(id, mustDate(t, "2026-05-11"), meldings[0], entries, "")
	if err != nil {
		t.Fatal(err)
	}
	newerID, err := store.AddGame(id, mustDate(t, "2026-05-12"), meldings[0], entries, "")
	if err != nil {
		t.Fatal(err)
	}

	rec := get(t, h, "/c/"+id+"/games/"+itoa(currentID))
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, want := range []string{
		`<title>Testklub — Spil #` + itoa(currentID) + `</title>`,
		`action="/c/` + id + `/games/` + itoa(currentID) + `/edit"`,
		`href="/c/` + id + `/games/` + itoa(olderID) + `"`,
		`href="/c/` + id + `/games/` + itoa(newerID) + `"`,
		`Spil #` + itoa(currentID) + ` d. 2026-05-11 melding 7 (7)`,
		`<div class="game-header">`,
		`class="badge badge-nav"`,
		`class="game-nav-arrow"`,
		`class="game-id-display">#` + itoa(currentID) + `</span>`,
		`Forrige spil`,
		`Næste spil`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("game detail missing %q: %s", want, body)
		}
	}
	for _, unwanted := range []string{"Spillet 2026-05-11", " -- ", "Tilbage", "Slet spil"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("game detail still renders %q: %s", unwanted, body)
		}
	}
}

func TestEditGameUpdatesStoredGame(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Testklub")

	var players []db.Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		player, err := store.AddPlayer(id, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, player)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	gameID, err := store.AddGame(id, mustDate(t, "2026-05-11"), meldings[0], []game.PlayerEntry{
		{PlayerID: players[0].ID, Role: "melder", Tricks: 4},
		{PlayerID: players[1].ID, Role: "makker", Tricks: 3},
		{PlayerID: players[2].ID, Role: "modspil", Tricks: 3},
		{PlayerID: players[3].ID, Role: "modspil", Tricks: 3},
	}, "før")
	if err != nil {
		t.Fatal(err)
	}

	editRec := get(t, h, "/c/"+id+"/games/"+itoa(gameID)+"/edit")
	assertStatus(t, editRec, http.StatusOK)
	editBody := editRec.Body.String()
	for _, want := range []string{
		`action="/c/` + id + `/games/` + itoa(gameID) + `/update"`,
		`data-role="melder"`,
		`name="tricks_` + itoa(players[0].ID) + `" value="4"`,
		`formaction="/c/` + id + `/games/` + itoa(gameID) + `/delete"`,
	} {
		if !strings.Contains(editBody, want) {
			t.Fatalf("edit page missing %q: %s", want, editBody)
		}
	}
	if strings.Contains(editBody, "Annuller") {
		t.Fatalf("edit page still renders cancel link: %s", editBody)
	}

	form := url.Values{
		"played_at":  {"2026-05-12"},
		"melding_id": {itoa(meldings[1].ID)},
		"note":       {"efter"},
	}
	for i, player := range players {
		pid := itoa(player.ID)
		form.Add("player_id", pid)
		switch i {
		case 0:
			form.Set("role_"+pid, "melder")
			form.Set("tricks_"+pid, "5")
		case 1:
			form.Set("role_"+pid, "makker")
			form.Set("tricks_"+pid, "4")
		default:
			form.Set("role_"+pid, "modspil")
			form.Set("tricks_"+pid, "2")
		}
	}

	rec := post(t, h, "/c/"+id+"/games/"+itoa(gameID)+"/update", form)
	assertStatus(t, rec, http.StatusSeeOther)
	if got, want := rec.Header().Get("Location"), "/c/"+id+"/games/"+itoa(gameID); got != want {
		t.Fatalf("redirect=%q want %q", got, want)
	}
	updated, err := store.GetGame(id, gameID)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := updated.PlayedAt.Format(dateLayout), "2026-05-12"; got != want {
		t.Fatalf("played_at=%q want %q", got, want)
	}
	if got, want := updated.MeldingID, meldings[1].ID; got != want {
		t.Fatalf("melding_id=%d want %d", got, want)
	}
	if got, want := updated.Note, "efter"; got != want {
		t.Fatalf("note=%q want %q", got, want)
	}
	if len(updated.Scores) != 4 {
		t.Fatalf("scores=%d want 4", len(updated.Scores))
	}
}

func TestRecentClubsCookie(t *testing.T) {
	app, _ := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Min Klub")

	// Visit the club, then home with the cookie carried.
	req := httptest.NewRequest(http.MethodGet, "/c/"+id, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)

	var recent *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == recentCookieName {
			recent = c
			break
		}
	}
	if recent == nil {
		t.Fatalf("no %s cookie set", recentCookieName)
	}
	wantMaxAge := int(recentMaxAge.Seconds())
	if recent.MaxAge < wantMaxAge {
		t.Errorf("cookie MaxAge=%d want >= %d", recent.MaxAge, wantMaxAge)
	}

	// Now hit home with that cookie.
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.AddCookie(recent)
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	assertStatus(t, rec2, http.StatusOK)
	if !strings.Contains(rec2.Body.String(), "Min Klub") {
		t.Errorf("home did not render recent club; body=%s", rec2.Body.String())
	}
}

func TestClubsPageRemoved(t *testing.T) {
	app, _ := newTestApp(t)
	h := app.routes()

	rec := get(t, h, "/clubs?q=klub")
	assertStatus(t, rec, http.StatusNotFound)
}

func TestHomeSearchOnlyMatchesProtected(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()

	idOpen := createClub(t, h, "Aabne Klub")
	idLocked := createClub(t, h, "Laaste Klub")
	if err := store.SetClubPassword(idLocked, "hemmelig"); err != nil {
		t.Fatal(err)
	}

	rec := get(t, h, "/?q=klub")
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	if !strings.Contains(body, "Laaste Klub") {
		t.Errorf("locked club missing from search results")
	}
	if strings.Contains(body, "Aabne Klub") {
		t.Errorf("open club leaked into search results")
	}
	_ = idOpen
}

func TestSaveGameAndAddAnotherRedirectsToNewGame(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()

	id := createClub(t, h, "Testklub")
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		rec := post(t, h, "/c/"+id+"/players/add", url.Values{"name": {name}})
		assertStatus(t, rec, http.StatusSeeOther)
	}
	players, err := store.ListPlayers(id)
	if err != nil {
		t.Fatal(err)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"played_at":  {"2026-05-11"},
		"melding_id": {itoa(meldings[0].ID)},
		"after":      {"new"},
	}
	for i, p := range players {
		pid := itoa(p.ID)
		form.Add("player_id", pid)
		switch i {
		case 0:
			form.Set("role_"+pid, "melder")
			form.Set("tricks_"+pid, "4")
		case 1:
			form.Set("role_"+pid, "makker")
			form.Set("tricks_"+pid, "3")
		case 2:
			form.Set("role_"+pid, "modspil")
			form.Set("tricks_"+pid, "3")
		case 3:
			form.Set("role_"+pid, "modspil")
			form.Set("tricks_"+pid, "3")
		}
	}

	rec := post(t, h, "/c/"+id+"/games/save", form)
	assertStatus(t, rec, http.StatusSeeOther)
	if got, want := rec.Header().Get("Location"), "/c/"+id+"/new"; got != want {
		t.Fatalf("redirect=%q want %q", got, want)
	}
}

func TestSaveGameRedirectPreservesSelectedSeason(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()

	id := createClub(t, h, "Testklub")
	if err := store.AddSeason(id, "Forår", "2026-01-01", "2026-06-30"); err != nil {
		t.Fatal(err)
	}
	seasons, err := store.ListSeasons(id)
	if err != nil || len(seasons) != 1 {
		t.Fatalf("list seasons: err=%v len=%d", err, len(seasons))
	}
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		rec := post(t, h, "/c/"+id+"/players/add", url.Values{"name": {name}})
		assertStatus(t, rec, http.StatusSeeOther)
	}
	players, err := store.ListPlayers(id)
	if err != nil {
		t.Fatal(err)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"played_at":  {"2026-05-11"},
		"melding_id": {itoa(meldings[0].ID)},
		"season":     {itoa(seasons[0].ID)},
	}
	for i, p := range players {
		pid := itoa(p.ID)
		form.Add("player_id", pid)
		switch i {
		case 0:
			form.Set("role_"+pid, "melder")
			form.Set("tricks_"+pid, "4")
		case 1:
			form.Set("role_"+pid, "makker")
			form.Set("tricks_"+pid, "3")
		default:
			form.Set("role_"+pid, "modspil")
			form.Set("tricks_"+pid, "3")
		}
	}

	rec := post(t, h, "/c/"+id+"/games/save", form)
	assertStatus(t, rec, http.StatusSeeOther)
	if got, want := rec.Header().Get("Location"), "/c/"+id+"/games?season="+itoa(seasons[0].ID); got != want {
		t.Fatalf("redirect=%q want %q", got, want)
	}
}

func TestSaveGameRejectsMoreThanFourPlayers(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()

	id := createClub(t, h, "Testklub")
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe", "Erik"} {
		rec := post(t, h, "/c/"+id+"/players/add", url.Values{"name": {name}})
		assertStatus(t, rec, http.StatusSeeOther)
	}
	players, err := store.ListPlayers(id)
	if err != nil {
		t.Fatal(err)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"played_at":  {"2026-05-11"},
		"melding_id": {itoa(meldings[0].ID)},
	}
	for i, p := range players {
		pid := itoa(p.ID)
		form.Add("player_id", pid)
		switch i {
		case 0:
			form.Set("role_"+pid, "melder")
			form.Set("tricks_"+pid, "4")
		case 1:
			form.Set("role_"+pid, "makker")
			form.Set("tricks_"+pid, "3")
		default:
			form.Set("role_"+pid, "modspil")
			form.Set("tricks_"+pid, "2")
		}
	}

	rec := post(t, h, "/c/"+id+"/games/save", form)
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "Vælg fire spillere.") {
		t.Fatalf("missing too-many-players error: %s", rec.Body.String())
	}
	games, err := store.ListGames(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 0 {
		t.Fatalf("saved %d games with five players; want 0", len(games))
	}
}

func TestMeldingSettingsPreserveSubmittedOrder(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()

	id := createClub(t, h, "Testklub")
	rec := post(t, h, "/c/"+id+"/settings/save", url.Values{
		"name":           {"Testklub"},
		"rules":          {""},
		"visibility":     {"private"},
		"melding_id":     {"", ""},
		"melding_name":   {"Ren sol", "7"},
		"melding_type":   {"nolo", "normal"},
		"melding_bid":    {"1", "7"},
		"melding_points": {"3", "1"},
		"player_id":      {},
		"player_name":    {},
		"player_emoji":   {},
		"season_id":      {},
		"season_name":    {},
		"season_start":   {},
		"season_end":     {},
	})
	assertStatus(t, rec, http.StatusOK)

	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(meldings) != 2 {
		t.Fatalf("meldings len=%d want 2", len(meldings))
	}
	if meldings[0].Name != "Ren sol" || meldings[1].Name != "7" {
		t.Fatalf("order=%q, %q; want Ren sol, 7", meldings[0].Name, meldings[1].Name)
	}
}

func TestInvalidSettingsDoNotPartiallySave(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()

	id := createClub(t, h, "Original")
	before, err := store.GetClub(id)
	if err != nil {
		t.Fatal(err)
	}

	rec := post(t, h, "/c/"+id+"/settings/save", url.Values{
		"name":           {"Changed"},
		"rules":          {"new rules"},
		"visibility":     {"private"},
		"melding_id":     {"", ""},
		"melding_name":   {"Ren sol", "7"},
		"melding_type":   {"nolo", "normal"},
		"melding_bid":    {"1", "7"},
		"melding_points": {"3", "1"},
		"player_id":      {},
		"player_name":    {},
		"player_emoji":   {},
		"season_id":      {""},
		"season_name":    {"Broken season"},
		"season_start":   {"2026-06-01"},
		"season_end":     {"2026-05-01"},
	})
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "Startdato skal være før slutdato.") {
		t.Fatalf("settings error missing from response: %s", rec.Body.String())
	}

	after, err := store.GetClub(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.Name != before.Name || after.Rules != before.Rules {
		t.Fatalf("club changed after rejected settings save: before=%+v after=%+v", before, after)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := len(meldings), len(db.DefaultMeldings); got != want {
		t.Fatalf("meldings len=%d want %d after rejected settings save", got, want)
	}
}

func TestSettingsSaveKeepsExistingMeldingsWhenGamesReferenceThem(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()
	id := createClub(t, h, "Original")

	var players []db.Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		player, err := store.AddPlayer(id, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, player)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddGame(id, mustDate(t, "2026-05-11"), meldings[0], []game.PlayerEntry{
		{PlayerID: players[0].ID, Role: "melder", Tricks: 4},
		{PlayerID: players[1].ID, Role: "makker", Tricks: 3},
		{PlayerID: players[2].ID, Role: "modspil", Tricks: 3},
		{PlayerID: players[3].ID, Role: "modspil", Tricks: 3},
	}, ""); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"name":         {"Rettet klub"},
		"rules":        {""},
		"visibility":   {"private"},
		"player_id":    {},
		"player_name":  {},
		"player_emoji": {},
		"season_id":    {},
		"season_name":  {},
		"season_start": {},
		"season_end":   {},
	}
	for _, melding := range meldings {
		form.Add("melding_id", itoa(melding.ID))
		form.Add("melding_name", melding.Name)
		form.Add("melding_type", melding.Type)
		form.Add("melding_bid", itoa(melding.Bid))
		form.Add("melding_points", itoa(melding.Points))
	}

	rec := post(t, h, "/c/"+id+"/settings/save", form)
	assertStatus(t, rec, http.StatusOK)
	if !strings.Contains(rec.Body.String(), "Gemt.") {
		t.Fatalf("save did not succeed: %s", rec.Body.String())
	}
	updated, err := store.GetClub(id)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := updated.Name, "Rettet klub"; got != want {
		t.Fatalf("name=%q want %q", got, want)
	}
	after, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := after[0].ID, meldings[0].ID; got != want {
		t.Fatalf("first melding id=%d want preserved id %d", got, want)
	}
}

func TestSortLeaderboardRowsByGames(t *testing.T) {
	rows := []leaderboardRow{
		{Rank: 1, Player: db.Player{Name: "Anna"}, Games: 2, Points: 10},
		{Rank: 2, Player: db.Player{Name: "Bo"}, Games: 4, Points: 8},
		{Rank: 3, Player: db.Player{Name: "Carl"}, Games: 1, Points: 6},
	}
	sortLeaderboardRows(rows, "games", "desc")
	if got, want := []string{rows[0].Player.Name, rows[1].Player.Name, rows[2].Player.Name}, []string{"Bo", "Anna", "Carl"}; !equalStrings(got, want) {
		t.Fatalf("sorted names=%v want %v", got, want)
	}
}

func TestLeaderboardSortLinksPreserveSeason(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/c/club?season=7&sort=points&dir=desc", nil)
	links := leaderboardSortLinks(req, "/c/club", "points", "desc")
	if got, want := links["games"], "/c/club?dir=desc&season=7&sort=games"; got != want {
		t.Fatalf("games link=%q want %q", got, want)
	}
	if got, want := links["points"], "/c/club?dir=asc&season=7&sort=points"; got != want {
		t.Fatalf("points link=%q want %q", got, want)
	}
}

func TestNavigationLinksPreserveSelectedSeason(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()

	id := createClub(t, h, "Testklub")
	if err := store.AddSeason(id, "Forår", "2026-01-01", "2026-06-30"); err != nil {
		t.Fatal(err)
	}
	seasons, err := store.ListSeasons(id)
	if err != nil || len(seasons) != 1 {
		t.Fatalf("list seasons: err=%v len=%d", err, len(seasons))
	}

	rec := get(t, h, "/c/"+id+"?season="+itoa(seasons[0].ID))
	assertStatus(t, rec, http.StatusOK)
	body := rec.Body.String()
	for _, want := range []string{
		`href="/c/` + id + `?season=` + itoa(seasons[0].ID) + `"`,
		`href="/c/` + id + `/games?season=` + itoa(seasons[0].ID) + `"`,
		`href="/c/` + id + `/settings?season=` + itoa(seasons[0].ID) + `"`,
		`href="/c/` + id + `/new?season=` + itoa(seasons[0].ID) + `"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("navigation missing %q: %s", want, body)
		}
	}
}

func TestRoleLabelForMeldingUsesJoinerForNolo(t *testing.T) {
	if got, want := roleLabelForMelding("makker", db.MeldingTypeNolo), "Går med"; got != want {
		t.Fatalf("nolo role label=%q want %q", got, want)
	}
	if got, want := roleLabelForMelding("makker", db.MeldingTypeNormal), "Makker"; got != want {
		t.Fatalf("normal role label=%q want %q", got, want)
	}
}

func TestSaveDraftJSONReturnsServerValidity(t *testing.T) {
	store, err := db.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	app := newApp(store, enabledImportClient{})
	h := app.routes()

	id := createClub(t, h, "Importklub")
	var players []db.Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		p, err := store.AddPlayer(id, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, p)
	}
	meldings, err := store.ListMeldings(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddDrafts(id, "batch", []db.Draft{{MeldingID: meldings[0].ID}}); err != nil {
		t.Fatal(err)
	}
	drafts, err := store.ListPendingDrafts(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(drafts) != 1 {
		t.Fatalf("drafts=%d want 1", len(drafts))
	}

	form := url.Values{
		"played_at":  {"2026-05-11"},
		"melding_id": {itoa(meldings[0].ID)},
	}
	for i, p := range players {
		form.Set("player_id_"+itoa(i), itoa(p.ID))
		form.Set("raw_name_"+itoa(i), p.Name)
		switch i {
		case 0:
			form.Set("role_"+itoa(i), "melder")
			form.Set("tricks_"+itoa(i), "4")
		case 1:
			form.Set("role_"+itoa(i), "makker")
			form.Set("tricks_"+itoa(i), "3")
		default:
			form.Set("role_"+itoa(i), "modspil")
			form.Set("tricks_"+itoa(i), "2")
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/c/"+id+"/import/"+itoa(drafts[0].ID)+"/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assertStatus(t, rec, http.StatusOK)
	var got struct {
		Valid  bool     `json:"valid"`
		Issues []string `json:"issues"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Valid {
		t.Fatalf("valid=true want false; issues=%v", got.Issues)
	}
	if !containsString(got.Issues, "Stik skal være 13 i alt") {
		t.Fatalf("issues=%v; want trick-sum issue", got.Issues)
	}
}

func TestClubRouteRateLimit(t *testing.T) {
	app, _ := newTestApp(t)
	app.clubLimiter = newRateLimiter(2, time.Minute)
	h := app.routes()

	for i := 0; i < 2; i++ {
		rec := get(t, h, "/c/00000000000000000000000000")
		assertStatus(t, rec, http.StatusNotFound)
	}
	rec := get(t, h, "/c/00000000000000000000000000")
	assertStatus(t, rec, http.StatusTooManyRequests)
	if got, want := rec.Body.String(), "429 Too Many Requests\n"; got != want {
		t.Fatalf("rate limit body=%q want %q", got, want)
	}
	if got := rec.Header().Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After=%q want empty", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func itoa(i int) string {
	// avoid pulling strconv into the test surface area
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = digits[i%10]
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}

func mustDate(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse(dateLayout, value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
