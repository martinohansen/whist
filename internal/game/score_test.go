package game

import "testing"

func TestComputeScoresExactBid(t *testing.T) {
	// Bid=10, points=4. Meld side gets exactly bid → points × 1.
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 5},
		{PlayerID: 2, Role: RoleMakker, Tricks: 5},
		{PlayerID: 3, Role: RoleModspil, Tricks: 2},
		{PlayerID: 4, Role: RoleModspil, Tricks: 1},
	}
	got := ComputeScores(TypeNormal,10, 4, entries)
	want := map[int]int{1: 4, 2: 4, 3: -4, 4: -4}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("player %d: got %d, want %d", id, got[id], w)
		}
	}
}

func TestComputeScoresOneOver(t *testing.T) {
	// Bid=10, points=4. Meld side gets 11 → points × 2 = 8.
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 6},
		{PlayerID: 2, Role: RoleMakker, Tricks: 5},
		{PlayerID: 3, Role: RoleModspil, Tricks: 1},
		{PlayerID: 4, Role: RoleModspil, Tricks: 1},
	}
	got := ComputeScores(TypeNormal,10, 4, entries)
	want := map[int]int{1: 8, 2: 8, 3: -8, 4: -8}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("player %d: got %d, want %d", id, got[id], w)
		}
	}
}

func TestComputeScoresTwoOver(t *testing.T) {
	// Bid=10, points=4. Meld side gets 12 → points × 3 = 12.
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 7},
		{PlayerID: 2, Role: RoleMakker, Tricks: 5},
		{PlayerID: 3, Role: RoleModspil, Tricks: 1},
		{PlayerID: 4, Role: RoleModspil, Tricks: 0},
	}
	got := ComputeScores(TypeNormal,10, 4, entries)
	want := map[int]int{1: 12, 2: 12, 3: -12, 4: -12}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("player %d: got %d, want %d", id, got[id], w)
		}
	}
}

func TestComputeScoresUnderBid(t *testing.T) {
	// Bid=9, points=3. Meld side gets 7 (2 short) → -6 per player.
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 3},
		{PlayerID: 2, Role: RoleMakker, Tricks: 4},
		{PlayerID: 3, Role: RoleModspil, Tricks: 3},
		{PlayerID: 4, Role: RoleModspil, Tricks: 3},
	}
	got := ComputeScores(TypeNormal,9, 3, entries)
	want := map[int]int{1: -6, 2: -6, 3: 6, 4: 6}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("player %d: got %d, want %d", id, got[id], w)
		}
	}
}

func TestComputeScoresZeroSum(t *testing.T) {
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 5},
		{PlayerID: 2, Role: RoleMakker, Tricks: 4},
		{PlayerID: 3, Role: RoleModspil, Tricks: 2},
		{PlayerID: 4, Role: RoleModspil, Tricks: 2},
	}
	got := ComputeScores(TypeNormal, 8, 2, entries)
	sum := 0
	for _, v := range got {
		sum += v
	}
	if sum != 0 {
		t.Errorf("scores not zero-sum: total=%d", sum)
	}
}

func TestComputeScoresNoloExact(t *testing.T) {
	// Sol (bid=0, points=1). Melder takes 0 tricks → success at level 1.
	// Melder: +3×1×1 = +3. Each modspil: -1.
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 0},
		{PlayerID: 2, Role: RoleModspil, Tricks: 5},
		{PlayerID: 3, Role: RoleModspil, Tricks: 4},
		{PlayerID: 4, Role: RoleModspil, Tricks: 4},
	}
	got := ComputeScores(TypeNolo, 0, 1, entries)
	want := map[int]int{1: 3, 2: -1, 3: -1, 4: -1}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("player %d: got %d, want %d", id, got[id], w)
		}
	}
}

func TestComputeScoresNoloFail(t *testing.T) {
	// Sol bid=0, points=1. Melder took 2 tricks (over by 2) → failure.
	// Melder: -3×1×2 = -6. Each modspil: +2.
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 2},
		{PlayerID: 2, Role: RoleModspil, Tricks: 4},
		{PlayerID: 3, Role: RoleModspil, Tricks: 4},
		{PlayerID: 4, Role: RoleModspil, Tricks: 3},
	}
	got := ComputeScores(TypeNolo, 0, 1, entries)
	want := map[int]int{1: -6, 2: 2, 3: 2, 4: 2}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("player %d: got %d, want %d", id, got[id], w)
		}
	}
}

func TestComputeScoresNoloMakkerSplit(t *testing.T) {
	// Sol (bid=2, points=1) with one makker joining (2v2).
	// Melder=2 tricks (success, perOpp=1*(0+1)=1, score=2*1=+2)
	// Makker=3 tricks (failure, perOpp=1*-1=-1, score=2*-1=-2)
	// Each modspil pays -(1 + -1) = 0.
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 2},
		{PlayerID: 2, Role: RoleMakker, Tricks: 3},
		{PlayerID: 3, Role: RoleModspil, Tricks: 4},
		{PlayerID: 4, Role: RoleModspil, Tricks: 4},
	}
	got := ComputeScores(TypeNolo, 2, 1, entries)
	want := map[int]int{1: 2, 2: -2, 3: 0, 4: 0}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("player %d: got %d, want %d", id, got[id], w)
		}
	}
}

func TestComputeScoresNoloBothWin(t *testing.T) {
	// Sol (bid=2, points=1) with one makker, both meld-side under the limit.
	// Melder=2, Makker=2: both perOpp=1*(0+1)=1, each score=2*1=+2
	// Each modspil pays -(1+1) = -2.
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 2},
		{PlayerID: 2, Role: RoleMakker, Tricks: 2},
		{PlayerID: 3, Role: RoleModspil, Tricks: 5},
		{PlayerID: 4, Role: RoleModspil, Tricks: 4},
	}
	got := ComputeScores(TypeNolo, 2, 1, entries)
	want := map[int]int{1: 2, 2: 2, 3: -2, 4: -2}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("player %d: got %d, want %d", id, got[id], w)
		}
	}
}

func TestComputeScoresNoloZeroSum(t *testing.T) {
	entries := []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 1},
		{PlayerID: 2, Role: RoleModspil, Tricks: 4},
		{PlayerID: 3, Role: RoleModspil, Tricks: 4},
		{PlayerID: 4, Role: RoleModspil, Tricks: 4},
	}
	got := ComputeScores(TypeNolo, 0, 3, entries)
	sum := 0
	for _, v := range got {
		sum += v
	}
	if sum != 0 {
		t.Errorf("nolo scores not zero-sum: total=%d", sum)
	}
}
