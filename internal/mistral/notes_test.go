package mistral

import (
	"context"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/martinohansen/whist/internal/db"
)

//go:embed testdata/notes/*.md
var notesFS embed.FS

// Short aliases for the test rosters — used in the expected-game tables below.
const (
	M  = "Martin"
	H  = "Hanne"
	Mi = "Mikkel"
	K  = "Katrine"

	Anne = "Anne"
	Bo   = "Bo"
	Carl = "Carl"
	Dina = "Dina"
)

const dontCheck = -1

type expectedPlayer struct {
	Name   string
	Role   string // "melder" | "makker" | "modspil"
	Tricks int    // dontCheck (-1) when the paper doesn't pin the value
}

type expectedGame struct {
	Melding     string
	Players     []expectedPlayer // exactly 4, in any order
	MeldSideSum int              // sum of melder+makker tricks; dontCheck to skip
	ModspilSum  int              // sum of modspil tricks; dontCheck to skip
}

// clubFixture bundles a 4-player roster and a melding list. Every test
// fixture references one clubFixture; the LLM is given that club's
// players+meldings when extracting, and the same set is used to build
// expected games. Keeping the roster at exactly four means melder+makker
// uniquely determine the two modspillere, so the paper alone pins the full
// role assignment.
type clubFixture struct {
	players  []db.Player
	meldings []db.Melding
}

func (c *clubFixture) otherTwo(a, b string) []string {
	out := make([]string, 0, 2)
	for _, p := range c.players {
		if p.Name != a && p.Name != b {
			out = append(out, p.Name)
		}
	}
	return out
}

func (c *clubFixture) otherThree(a string) []string {
	out := make([]string, 0, 3)
	for _, p := range c.players {
		if p.Name != a {
			out = append(out, p.Name)
		}
	}
	return out
}

func (c *clubFixture) hasPlayer(name string) bool {
	for _, p := range c.players {
		if p.Name == name {
			return true
		}
	}
	return false
}

func (c *clubFixture) meldingByName(name string) (db.Melding, bool) {
	for _, m := range c.meldings {
		if m.Name == name {
			return m, true
		}
	}
	return db.Melding{}, false
}

// normal builds an expectedGame for a "normal" melding where the paper
// gives the meld-side trick total directly. Per-player tricks aren't asserted;
// the meld-side and modspil sums are.
func (c *clubFixture) normal(melding, melder, makker string, meldSum int) expectedGame {
	mod := c.otherTwo(melder, makker)
	return expectedGame{
		Melding: melding,
		Players: []expectedPlayer{
			{Name: melder, Role: "melder", Tricks: dontCheck},
			{Name: makker, Role: "makker", Tricks: dontCheck},
			{Name: mod[0], Role: "modspil", Tricks: dontCheck},
			{Name: mod[1], Role: "modspil", Tricks: dontCheck},
		},
		MeldSideSum: meldSum,
		ModspilSum:  13 - meldSum,
	}
}

// normalResult builds an expectedGame for a "normal" melding where the paper
// gives only the result relative to the bid ("+1", "-2", "=") rather than an
// explicit trick total. Keeping this separate from normal makes the
// result-delta extraction path visible in the fixture table.
func (c *clubFixture) normalResult(melding, melder, makker string, delta int) expectedGame {
	m, ok := c.meldingByName(melding)
	if !ok {
		panic(fmt.Sprintf("unknown melding %q", melding))
	}
	return c.normal(melding, melder, makker, m.Bid+delta)
}

// solo builds an expectedGame for a nolo played alone. melderTricks is
// pinned; modspil sum is derived but individual tricks aren't.
func (c *clubFixture) solo(melding, melder string, melderTricks int) expectedGame {
	ps := []expectedPlayer{{Name: melder, Role: "melder", Tricks: melderTricks}}
	for _, n := range c.otherThree(melder) {
		ps = append(ps, expectedPlayer{Name: n, Role: "modspil", Tricks: dontCheck})
	}
	return expectedGame{
		Melding:     melding,
		Players:     ps,
		MeldSideSum: melderTricks,
		ModspilSum:  13 - melderTricks,
	}
}

// soloMakker builds an expectedGame for a nolo played with a makker. Both
// melder and makker tricks are pinned; modspil individual tricks aren't.
func (c *clubFixture) soloMakker(melding, melder string, melderTricks int, makker string, makkerTricks int) expectedGame {
	mod := c.otherTwo(melder, makker)
	return expectedGame{
		Melding: melding,
		Players: []expectedPlayer{
			{Name: melder, Role: "melder", Tricks: melderTricks},
			{Name: makker, Role: "makker", Tricks: makkerTricks},
			{Name: mod[0], Role: "modspil", Tricks: dontCheck},
			{Name: mod[1], Role: "modspil", Tricks: dontCheck},
		},
		MeldSideSum: melderTricks + makkerTricks,
		ModspilSum:  13 - melderTricks - makkerTricks,
	}
}

// clubDanish is the standard Danish whist club used by notes 01-10:
// the four-player roster Martin/Hanne/Mikkel/Katrine and the default
// melding list (bids 7-13, Sol, Ren sol).
var clubDanish = &clubFixture{
	players: []db.Player{
		{ID: 1, Name: "Martin"},
		{ID: 2, Name: "Hanne"},
		{ID: 3, Name: "Mikkel"},
		{ID: 4, Name: "Katrine"},
	},
	meldings: []db.Melding{
		{ID: 1, Name: "7", Type: db.MeldingTypeNormal, Bid: 7, Points: 1},
		{ID: 2, Name: "8", Type: db.MeldingTypeNormal, Bid: 8, Points: 2},
		{ID: 3, Name: "9", Type: db.MeldingTypeNormal, Bid: 9, Points: 3},
		{ID: 4, Name: "10", Type: db.MeldingTypeNormal, Bid: 10, Points: 4},
		{ID: 5, Name: "11", Type: db.MeldingTypeNormal, Bid: 11, Points: 6},
		{ID: 6, Name: "12", Type: db.MeldingTypeNormal, Bid: 12, Points: 8},
		{ID: 7, Name: "13", Type: db.MeldingTypeNormal, Bid: 13, Points: 16},
		{ID: 8, Name: "Sol", Type: db.MeldingTypeNolo, Bid: 2, Points: 1},
		{ID: 9, Name: "Ren sol", Type: db.MeldingTypeNolo, Bid: 1, Points: 3},
	},
}

// clubEsmakker is an "esmakker whist" club used by notes 11-17. The melder
// nominates a card (typically an es) and whoever holds it becomes the
// makker — so meldings carry a suit (spar/hjerter/klør/ruder). Bidding is
// 7-13 in each suit; nolos work the same as in clubDanish.
var clubEsmakker = func() *clubFixture {
	suits := []struct{ name string }{
		{"spar"}, {"hjerter"}, {"klør"}, {"ruder"},
	}
	bids := []struct{ n, pts int }{
		{7, 1}, {8, 2}, {9, 3}, {10, 4}, {11, 6}, {12, 8}, {13, 16},
	}
	var meldings []db.Melding
	id := 1
	for _, b := range bids {
		for _, s := range suits {
			meldings = append(meldings, db.Melding{
				ID:     id,
				Name:   fmt.Sprintf("%d %s", b.n, s.name),
				Type:   db.MeldingTypeNormal,
				Bid:    b.n,
				Points: b.pts,
			})
			id++
		}
	}
	meldings = append(meldings,
		db.Melding{ID: id, Name: "Sol", Type: db.MeldingTypeNolo, Bid: 2, Points: 1},
		db.Melding{ID: id + 1, Name: "Ren sol", Type: db.MeldingTypeNolo, Bid: 1, Points: 3},
	)
	return &clubFixture{
		players: []db.Player{
			{ID: 1, Name: "Anne"},
			{ID: 2, Name: "Bo"},
			{ID: 3, Name: "Carl"},
			{ID: 4, Name: "Dina"},
		},
		meldings: meldings,
	}
}()

type noteCase struct {
	file     string
	club     *clubFixture
	expected []expectedGame
}

// Ground truth for every fixture. Each entry encodes:
//   - melding name (matched against the club's melding list),
//   - role for every player (the paper fully determines this in a 4-player roster),
//   - tricks where the paper specifies them (otherwise dontCheck),
//   - team sums (meld side + modspil) where the paper specifies them.
var noteCases = []noteCase{
	{
		file: "note-01.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.solo("Sol", M, 1),
			clubDanish.normal("10", H, Mi, 9),
			clubDanish.normal("9", K, H, 9),
			clubDanish.soloMakker("Sol", K, 1, M, 3),
			clubDanish.normal("8", Mi, K, 8),
			clubDanish.normal("9", M, Mi, 11),
		},
	},
	{
		file: "note-02.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.normal("10", Mi, K, 8),
			clubDanish.solo("Sol", M, 1),
			clubDanish.normal("11", H, Mi, 11),
			clubDanish.normal("13", K, M, 13),
			clubDanish.soloMakker("Sol", K, 1, M, 3),
		},
	},
	{
		file: "note-03.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.normal("9", Mi, K, 7),
			clubDanish.solo("Sol", M, 1),
			clubDanish.soloMakker("Sol", K, 1, M, 3),
			clubDanish.normal("10", H, Mi, 11),
			clubDanish.normal("13", H, M, 12),
			clubDanish.normal("8", Mi, H, 8),
			clubDanish.normal("9", K, Mi, 10),
		},
	},
	{
		file: "note-04.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.normal("9", Mi, K, 7),
			clubDanish.solo("Sol", M, 1),
			clubDanish.soloMakker("Sol", K, 1, M, 3),
			clubDanish.normal("10", H, Mi, 11),
			clubDanish.normal("13", K, Mi, 12),
			clubDanish.normal("8", Mi, M, 8),
			clubDanish.normal("9", H, K, 10),
			clubDanish.normal("11", M, K, 13),
			clubDanish.solo("Ren sol", Mi, 0),
		},
	},
	{
		file: "note-05.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.normal("9", Mi, K, 7),
			clubDanish.solo("Sol", M, 1),
			clubDanish.soloMakker("Sol", K, 1, M, 3),
			clubDanish.normal("10", H, Mi, 11),
			clubDanish.normal("13", H, Mi, 13),
			clubDanish.normal("8", Mi, K, 9),
		},
	},
	{
		file: "note-06.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.normal("10", Mi, K, 8),
			clubDanish.solo("Ren sol", H, 0),
			clubDanish.normal("9", K, H, 11),
			clubDanish.soloMakker("Sol", Mi, 1, M, 2),
		},
	},
	{
		file: "note-07.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.normal("9", Mi, K, 7),
			clubDanish.solo("Sol", M, 1),
			clubDanish.soloMakker("Sol", K, 1, M, 3),
			clubDanish.normal("10", H, Mi, 9),
			clubDanish.normal("13", H, M, 13),
		},
	},
	{
		file: "note-08.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.normal("9", Mi, K, 8),
			clubDanish.solo("Sol", M, 1),
			clubDanish.soloMakker("Sol", K, 1, M, 3),
			clubDanish.normal("10", H, Mi, 11),
			clubDanish.normal("13", K, Mi, 12),
			clubDanish.normal("8", Mi, M, 8),
			clubDanish.normal("9", H, K, 10),
			clubDanish.normal("11", M, K, 13),
			clubDanish.solo("Ren sol", Mi, 0),
			clubDanish.normal("8", H, K, 7),
			clubDanish.normal("9", M, H, 10),
		},
	},
	{
		file: "note-09.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.normal("9", Mi, K, 7),
			clubDanish.solo("Sol", M, 1),
			clubDanish.soloMakker("Sol", K, 1, M, 3),
			clubDanish.normal("10", H, Mi, 11),
			clubDanish.normal("13", K, Mi, 12),
			clubDanish.normal("8", Mi, H, 8),
		},
	},
	{
		file: "note-10.md",
		club: clubDanish,
		expected: []expectedGame{
			clubDanish.normal("8", M, H, 9),
			clubDanish.normal("9", Mi, K, 8),
			clubDanish.solo("Sol", M, 1),
			clubDanish.soloMakker("Sol", K, 1, M, 3),
			clubDanish.normal("10", H, Mi, 11),
			clubDanish.normal("13", K, Mi, 12),
			clubDanish.normal("8", Mi, M, 8),
			clubDanish.normal("9", H, K, 10),
			clubDanish.solo("Ren sol", Mi, 0),
		},
	},
	{
		file: "note-11.md",
		club: clubEsmakker,
		expected: []expectedGame{
			clubEsmakker.normalResult("8 spar", Anne, Carl, +1),
			clubEsmakker.normalResult("9 hjerter", Bo, Dina, -2),
			clubEsmakker.normalResult("7 klør", Carl, Anne, 0),
		},
	},
	{
		file: "note-12.md",
		club: clubEsmakker,
		expected: []expectedGame{
			clubEsmakker.normalResult("8 spar", Anne, Carl, +1),
			clubEsmakker.normalResult("9 hjerter", Bo, Dina, -2),
			clubEsmakker.normalResult("7 klør", Carl, Anne, 0),
			clubEsmakker.normalResult("10 ruder", Dina, Bo, +1),
		},
	},
	{
		file: "note-13.md",
		club: clubEsmakker,
		expected: []expectedGame{
			clubEsmakker.normalResult("8 spar", Anne, Carl, +1),
			clubEsmakker.normalResult("9 hjerter", Bo, Dina, -2),
		},
	},
	{
		file: "note-14.md",
		club: clubEsmakker,
		expected: []expectedGame{
			clubEsmakker.normal("8 spar", Anne, Carl, 9),
			clubEsmakker.normal("9 hjerter", Bo, Dina, 7),
			clubEsmakker.normal("7 klør", Carl, Anne, 7),
		},
	},
	{
		file: "note-15.md",
		club: clubEsmakker,
		expected: []expectedGame{
			clubEsmakker.normalResult("8 spar", Anne, Carl, +1),
			clubEsmakker.normalResult("9 hjerter", Bo, Dina, -2),
			clubEsmakker.normalResult("7 klør", Carl, Anne, 0),
		},
	},
	{
		file: "note-16.md",
		club: clubEsmakker,
		expected: []expectedGame{
			clubEsmakker.normalResult("8 spar", Anne, Carl, +1),
			clubEsmakker.normalResult("9 hjerter", Bo, Dina, -2),
			clubEsmakker.normalResult("7 klør", Carl, Anne, 0),
		},
	},
	{
		file: "note-17.md",
		club: clubEsmakker,
		expected: []expectedGame{
			clubEsmakker.normal("8 spar", Anne, Carl, 9),
			clubEsmakker.normal("9 hjerter", Bo, Dina, 7),
			clubEsmakker.normal("7 klør", Carl, Anne, 7),
			clubEsmakker.normal("10 ruder", Dina, Bo, 11),
		},
	},
}

func TestNoteCasesAreWellFormed(t *testing.T) {
	noteFiles, err := fs.Glob(notesFS, "testdata/notes/*.md")
	if err != nil {
		t.Fatal(err)
	}

	caseFiles := map[string]struct{}{}
	for _, tc := range noteCases {
		fullPath := path.Join("testdata/notes", tc.file)
		if _, ok := caseFiles[fullPath]; ok {
			t.Errorf("duplicate note case for %s", tc.file)
		}
		caseFiles[fullPath] = struct{}{}

		t.Run(tc.file, func(t *testing.T) {
			if tc.club == nil {
				t.Fatal("club is nil")
			}
			if got := len(tc.club.players); got != 4 {
				t.Fatalf("club has %d players, want 4", got)
			}
			if len(tc.expected) == 0 {
				t.Fatal("expected games is empty")
			}

			for i, e := range tc.expected {
				m, ok := tc.club.meldingByName(e.Melding)
				if !ok {
					t.Errorf("expected[%d]: unknown melding %q", i+1, e.Melding)
					continue
				}
				assertExpectedGameWellFormed(t, i+1, tc.club, m, e)
			}
		})
	}

	if got, want := len(caseFiles), len(noteFiles); got != want {
		t.Errorf("note case count = %d, embedded note file count = %d", got, want)
	}
	for _, file := range noteFiles {
		if _, ok := caseFiles[file]; !ok {
			t.Errorf("missing note case for %s", path.Base(file))
		}
	}
}

func assertExpectedGameWellFormed(t *testing.T, expIdx int, club *clubFixture, melding db.Melding, e expectedGame) {
	t.Helper()
	if got := len(e.Players); got != 4 {
		t.Errorf("expected[%d]: %d players, want 4", expIdx, got)
		return
	}

	seenPlayers := map[string]bool{}
	roleCounts := map[string]int{}
	for _, p := range e.Players {
		if seenPlayers[p.Name] {
			t.Errorf("expected[%d]: duplicate player %q", expIdx, p.Name)
		}
		seenPlayers[p.Name] = true
		if !club.hasPlayer(p.Name) {
			t.Errorf("expected[%d]: unknown player %q", expIdx, p.Name)
		}
		switch p.Role {
		case "melder", "makker", "modspil":
			roleCounts[p.Role]++
		default:
			t.Errorf("expected[%d]: unknown role %q for %s", expIdx, p.Role, p.Name)
		}
		if p.Tricks != dontCheck && (p.Tricks < 0 || p.Tricks > 13) {
			t.Errorf("expected[%d]: %s tricks = %d, want 0..13 or dontCheck", expIdx, p.Name, p.Tricks)
		}
	}

	if roleCounts["melder"] != 1 {
		t.Errorf("expected[%d]: melder count = %d, want 1", expIdx, roleCounts["melder"])
	}
	switch melding.Type {
	case db.MeldingTypeNormal:
		if roleCounts["makker"] != 1 {
			t.Errorf("expected[%d]: makker count = %d, want 1 for normal melding", expIdx, roleCounts["makker"])
		}
		if roleCounts["modspil"] != 2 {
			t.Errorf("expected[%d]: modspil count = %d, want 2 for normal melding", expIdx, roleCounts["modspil"])
		}
	case db.MeldingTypeNolo:
		if roleCounts["makker"] > 1 {
			t.Errorf("expected[%d]: makker count = %d, want at most 1 for nolo", expIdx, roleCounts["makker"])
		}
		if got, want := roleCounts["modspil"], 3-roleCounts["makker"]; got != want {
			t.Errorf("expected[%d]: modspil count = %d, want %d for nolo", expIdx, got, want)
		}
	default:
		t.Errorf("expected[%d]: unknown melding type %q", expIdx, melding.Type)
	}

	if e.MeldSideSum != dontCheck && (e.MeldSideSum < 0 || e.MeldSideSum > 13) {
		t.Errorf("expected[%d]: meld-side sum = %d, want 0..13 or dontCheck", expIdx, e.MeldSideSum)
	}
	if e.ModspilSum != dontCheck && (e.ModspilSum < 0 || e.ModspilSum > 13) {
		t.Errorf("expected[%d]: modspil sum = %d, want 0..13 or dontCheck", expIdx, e.ModspilSum)
	}
	if e.MeldSideSum != dontCheck && e.ModspilSum != dontCheck && e.MeldSideSum+e.ModspilSum != 13 {
		t.Errorf("expected[%d]: trick sums = %d + %d, want 13", expIdx, e.MeldSideSum, e.ModspilSum)
	}
}

// gameSignature canonicalises a game as "melding|name=role,name=role,...".
// Used to match an expected game to an actual one regardless of player order.
// Two games with the same signature have the same melding and identical role
// assignments — which uniquely identifies a game in a 4-player roster.
func gameSignature(melding string, roles map[string]string) string {
	names := make([]string, 0, len(roles))
	for n := range roles {
		names = append(names, n)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, n := range names {
		parts = append(parts, n+"="+roles[n])
	}
	return melding + "|" + strings.Join(parts, ",")
}

func actualSignature(g DraftGame) string {
	roles := map[string]string{}
	for _, p := range g.Players {
		roles[p.Name] = p.Role
	}
	return gameSignature(g.MeldingName, roles)
}

func expectedSignature(e expectedGame) string {
	roles := map[string]string{}
	for _, p := range e.Players {
		roles[p.Name] = p.Role
	}
	return gameSignature(e.Melding, roles)
}

func describeExpected(e expectedGame) string {
	parts := make([]string, 0, 4)
	for _, p := range e.Players {
		t := "?"
		if p.Tricks != dontCheck {
			t = strconv.Itoa(p.Tricks)
		}
		parts = append(parts, fmt.Sprintf("%s(%s=%s)", p.Name, p.Role, t))
	}
	return e.Melding + " · " + strings.Join(parts, ", ")
}

func describeActual(g DraftGame) string {
	parts := make([]string, 0, 4)
	for _, p := range g.Players {
		parts = append(parts, fmt.Sprintf("%s(%s=%d)", p.Name, p.Role, p.Tricks))
	}
	return g.MeldingName + " · " + strings.Join(parts, ", ")
}

// TestExtractFixtures hits the live Mistral API for each fixture and asserts
// the LLM produced the expected melding + role assignment + tricks for every
// game. Off by default — it's slow (minutes) and akin to an integration
// test. Opt in with WHIST_MISTRAL_INTEGRATION=1 and a valid MISTRAL_API_KEY:
//
//	WHIST_MISTRAL_INTEGRATION=1 MISTRAL_API_KEY=... go test ./internal/mistral -run TestExtractFixtures -v
func TestExtractFixtures(t *testing.T) {
	if os.Getenv("WHIST_MISTRAL_INTEGRATION") == "" {
		t.Skip("WHIST_MISTRAL_INTEGRATION not set; skipping live Mistral integration test")
	}
	apiKey := os.Getenv("MISTRAL_API_KEY")
	if apiKey == "" {
		t.Skip("MISTRAL_API_KEY not set; skipping fixture integration test")
	}
	c := New(apiKey)

	for _, tc := range noteCases {
		t.Run(tc.file, func(t *testing.T) {
			b, err := notesFS.ReadFile(path.Join("testdata/notes", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()

			games, err := c.Extract(ctx, string(b), tc.club.meldings, tc.club.players)
			if err != nil {
				// Swallow Mistral rate limits as warnings — on the free tier
				// only a handful of calls per minute are allowed, and a paid
				// account makes this go away. Don't let it mark the suite red.
				if strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "rate limit") {
					t.Logf("WARN: %s rate-limited by Mistral, skipping: %v", tc.file, err)
					t.Skip("rate limited")
				}
				t.Fatalf("Extract: %v", err)
			}

			t.Logf("decoded %d game(s) (expected %d):", len(games), len(tc.expected))
			for i, g := range games {
				t.Logf("  actual[%d]  %s", i+1, describeActual(g))
			}
			for i, e := range tc.expected {
				t.Logf("  expect[%d] %s  meldSum=%d modSum=%d",
					i+1, describeExpected(e), e.MeldSideSum, e.ModspilSum)
			}

			if got, want := len(games), len(tc.expected); got != want {
				t.Errorf("game count = %d, want %d", got, want)
			}

			// Build signature → []actual index, so we match each expected to
			// an unconsumed actual game with the identical melding+role
			// fingerprint. In a 4-player roster this is unique enough to
			// pair every game without ambiguity.
			actualBySig := map[string][]int{}
			for i, g := range games {
				sig := actualSignature(g)
				actualBySig[sig] = append(actualBySig[sig], i)
			}
			consumed := make([]bool, len(games))

			for ei, e := range tc.expected {
				sig := expectedSignature(e)
				matched := -1
				for _, idx := range actualBySig[sig] {
					if !consumed[idx] {
						matched = idx
						break
					}
				}
				if matched == -1 {
					t.Errorf("expected[%d] %s — no actual game matches signature %s",
						ei+1, describeExpected(e), sig)
					continue
				}
				consumed[matched] = true
				assertGameMatch(t, ei+1, e, games[matched])
			}

			for i, used := range consumed {
				if !used && i < len(games) {
					t.Errorf("unmatched actual[%d]: %s", i+1, describeActual(games[i]))
				}
			}
		})
	}
}

func assertGameMatch(t *testing.T, expIdx int, e expectedGame, g DraftGame) {
	t.Helper()
	if g.MeldingName != e.Melding {
		t.Errorf("expected[%d]: melding = %q, want %q", expIdx, g.MeldingName, e.Melding)
	}
	if len(g.Players) != 4 {
		t.Errorf("expected[%d]: %d players, want 4", expIdx, len(g.Players))
		return
	}
	actualByName := map[string]DraftPlayer{}
	for _, p := range g.Players {
		actualByName[p.Name] = p
	}
	meldSum, modSum := 0, 0
	for _, ep := range e.Players {
		ap, ok := actualByName[ep.Name]
		if !ok {
			t.Errorf("expected[%d]: player %q missing from actual", expIdx, ep.Name)
			continue
		}
		if ap.Role != ep.Role {
			t.Errorf("expected[%d]: %s role = %q, want %q", expIdx, ep.Name, ap.Role, ep.Role)
		}
		if ep.Tricks != dontCheck && ap.Tricks != ep.Tricks {
			t.Errorf("expected[%d]: %s tricks = %d, want %d", expIdx, ep.Name, ap.Tricks, ep.Tricks)
		}
		switch ap.Role {
		case "melder", "makker":
			meldSum += ap.Tricks
		case "modspil":
			modSum += ap.Tricks
		}
	}
	if e.MeldSideSum != dontCheck && meldSum != e.MeldSideSum {
		t.Errorf("expected[%d]: meld-side tricks sum = %d, want %d", expIdx, meldSum, e.MeldSideSum)
	}
	if e.ModspilSum != dontCheck && modSum != e.ModspilSum {
		t.Errorf("expected[%d]: modspil tricks sum = %d, want %d", expIdx, modSum, e.ModspilSum)
	}
}
