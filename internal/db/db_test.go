package db

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/martinohansen/whist/internal/game"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "whist.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSeasonOverlapRejectsIntersectingRanges(t *testing.T) {
	store := newTestStore(t)
	club, err := store.CreateClub("Season club")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.AddSeason(club.ID, "Spring", "2026-01-01", "2026-03-31"); err != nil {
		t.Fatal(err)
	}
	if err := store.AddSeason(club.ID, "Overlap", "2026-03-01", "2026-04-30"); !errors.Is(err, ErrSeasonOverlap) {
		t.Fatalf("AddSeason overlap error=%v want %v", err, ErrSeasonOverlap)
	}
	if err := store.AddSeason(club.ID, "Summer", "2026-04-01", "2026-06-30"); err != nil {
		t.Fatalf("AddSeason adjacent range: %v", err)
	}
}

func TestApproveDraftsCreatesValidGamesAndLeavesInvalidPending(t *testing.T) {
	store := newTestStore(t)
	club, err := store.CreateClub("Draft club")
	if err != nil {
		t.Fatal(err)
	}
	var players []Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		p, err := store.AddPlayer(club.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, p)
	}
	meldings, err := store.ListMeldings(club.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := meldings[0]
	playedAt := time.Date(2026, time.May, 11, 0, 0, 0, 0, time.UTC)

	if err := store.AddDrafts(club.ID, "batch", []Draft{
		{
			PlayedAt:  playedAt,
			MeldingID: m.ID,
			Scores: []DraftScore{
				{Position: 0, PlayerID: players[0].ID, Role: game.RoleMelder, Tricks: 4},
				{Position: 1, PlayerID: players[1].ID, Role: game.RoleMakker, Tricks: 3},
				{Position: 2, PlayerID: players[2].ID, Role: game.RoleModspil, Tricks: 3},
				{Position: 3, PlayerID: players[3].ID, Role: game.RoleModspil, Tricks: 3},
			},
		},
		{
			PlayedAt:  playedAt,
			MeldingID: m.ID,
			Scores: []DraftScore{
				{Position: 0, PlayerID: players[0].ID, Role: game.RoleMelder, Tricks: 4},
				{Position: 1, PlayerID: players[1].ID, Role: game.RoleMakker, Tricks: 3},
				{Position: 2, PlayerID: players[2].ID, Role: game.RoleModspil, Tricks: 2},
				{Position: 3, PlayerID: players[3].ID, Role: game.RoleModspil, Tricks: 2},
			},
		},
	}); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListPendingDrafts(club.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending drafts=%d want 2", len(pending))
	}

	created, skipped, err := store.ApproveDrafts(club.ID)
	if err != nil {
		t.Fatal(err)
	}
	if created != 1 {
		t.Fatalf("created=%d want 1", created)
	}
	if len(skipped) != 1 || skipped[0] != pending[1].ID {
		t.Fatalf("skipped=%v want [%d]", skipped, pending[1].ID)
	}
	games, err := store.ListGames(club.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 1 {
		t.Fatalf("games=%d want 1", len(games))
	}
	pending, err = store.ListPendingDrafts(club.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 || pending[0].ID != skipped[0] {
		t.Fatalf("pending after approval=%v want skipped draft %d", pending, skipped[0])
	}
}

func TestLeaderboardFilteredOnlyCountsGamesInsideBounds(t *testing.T) {
	store := newTestStore(t)
	club, err := store.CreateClub("Leaderboard club")
	if err != nil {
		t.Fatal(err)
	}
	var players []Player
	for _, name := range []string{"Anna", "Bo", "Carl", "Dorthe"} {
		p, err := store.AddPlayer(club.ID, name)
		if err != nil {
			t.Fatal(err)
		}
		players = append(players, p)
	}
	meldings, err := store.ListMeldings(club.ID)
	if err != nil {
		t.Fatal(err)
	}
	entries := []game.PlayerEntry{
		{PlayerID: players[0].ID, Role: game.RoleMelder, Tricks: 4},
		{PlayerID: players[1].ID, Role: game.RoleMakker, Tricks: 3},
		{PlayerID: players[2].ID, Role: game.RoleModspil, Tricks: 3},
		{PlayerID: players[3].ID, Role: game.RoleModspil, Tricks: 3},
	}
	if _, err := store.AddGame(club.ID, time.Date(2026, time.January, 10, 0, 0, 0, 0, time.UTC), meldings[0], entries, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := store.AddGame(club.ID, time.Date(2026, time.March, 10, 0, 0, 0, 0, time.UTC), meldings[0], entries, ""); err != nil {
		t.Fatal(err)
	}

	rows, err := store.LeaderboardFiltered(club.ID, LeaderboardFilter{
		After: time.Date(2026, time.March, 1, 0, 0, 0, 0, time.UTC),
		Until: time.Date(2026, time.March, 31, 23, 59, 59, 0, time.UTC),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rows[0].Games, 1; got != want {
		t.Fatalf("filtered games=%d want %d", got, want)
	}
}
