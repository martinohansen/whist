package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/martinohansen/whist/internal/db"
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
	if !strings.Contains(rec.Body.String(), "Opret klub") {
		t.Errorf("home missing 'Opret klub'")
	}
}

func TestCreateClubAndVisitAllPages(t *testing.T) {
	app, _ := newTestApp(t)
	h := app.routes()

	id := createClub(t, h, "Testklub")

	for _, path := range []string{"", "games", "new", "settings"} {
		p := "/c/" + id
		if path != "" {
			p += "/" + path
		}
		rec := get(t, h, p)
		assertStatus(t, rec, http.StatusOK)
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

func TestPasswordSearchOnlyMatchesProtected(t *testing.T) {
	app, store := newTestApp(t)
	h := app.routes()

	idOpen := createClub(t, h, "Aabne Klub")
	idLocked := createClub(t, h, "Laaste Klub")
	if err := store.SetClubPassword(idLocked, "hemmelig"); err != nil {
		t.Fatal(err)
	}

	rec := get(t, h, "/clubs?q=klub")
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
