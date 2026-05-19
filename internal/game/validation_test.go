package game

import "testing"

func TestValidateEntriesRejectsTooManyNormalPlayers(t *testing.T) {
	got := ValidateEntries(TypeNormal, []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 4},
		{PlayerID: 2, Role: RoleMakker, Tricks: 3},
		{PlayerID: 3, Role: RoleModspil, Tricks: 2},
		{PlayerID: 4, Role: RoleModspil, Tricks: 2},
		{PlayerID: 5, Role: RoleModspil, Tricks: 2},
	})
	if !hasIssue(got, IssuePlayerCount) {
		t.Fatalf("issues=%v; want %q", got, IssuePlayerCount)
	}
	if !hasIssue(got, IssueModspilCount) {
		t.Fatalf("issues=%v; want %q", got, IssueModspilCount)
	}
}

func TestValidateEntriesAcceptsSupportedNoloShape(t *testing.T) {
	got := ValidateEntries(TypeNolo, []PlayerEntry{
		{PlayerID: 1, Role: RoleMelder, Tricks: 2},
		{PlayerID: 2, Role: RoleMakker, Tricks: 3},
		{PlayerID: 3, Role: RoleModspil, Tricks: 4},
		{PlayerID: 4, Role: RoleModspil, Tricks: 4},
	})
	if len(got) != 0 {
		t.Fatalf("issues=%v; want none", got)
	}
}

func TestDefaultTricksNormal(t *testing.T) {
	// Bid 9: melder gets 5 (ceil), makker 4, modspil split 2/2.
	got := DefaultTricks(TypeNormal, 9, []string{RoleMelder, RoleMakker, RoleModspil, RoleModspil})
	want := []int{5, 4, 2, 2}
	if !equalInts(got, want) {
		t.Fatalf("bid=9 normal got=%v want=%v", got, want)
	}
	// Bid 8 (even): melder 4, makker 4, modspil 3/2 (rem=5, modEach=2, extra=1 → first modspil 3, second 2).
	got = DefaultTricks(TypeNormal, 8, []string{RoleMelder, RoleMakker, RoleModspil, RoleModspil})
	want = []int{4, 4, 3, 2}
	if !equalInts(got, want) {
		t.Fatalf("bid=8 normal got=%v want=%v", got, want)
	}
	// Sum invariant: every normal default must total 13.
	for bid := 7; bid <= 13; bid++ {
		got := DefaultTricks(TypeNormal, bid, []string{RoleMelder, RoleMakker, RoleModspil, RoleModspil})
		sum := 0
		for _, n := range got {
			sum += n
		}
		if sum != 13 {
			t.Fatalf("bid=%d sum=%d want 13: %v", bid, sum, got)
		}
	}
}

func TestDefaultTricksNoloRespectsPositionOrder(t *testing.T) {
	// Sol, bid=2: melder + 2 makker (meldSeats=3, meldTotal=6) vs 1 modspil (7).
	got := DefaultTricks(TypeNolo, 2, []string{RoleMelder, RoleMakker, RoleMakker, RoleModspil})
	want := []int{2, 2, 2, 7}
	if !equalInts(got, want) {
		t.Fatalf("nolo bid=2 (1+2v1) got=%v want=%v", got, want)
	}
	// Classic Sol shape: 1 melder vs 3 modspil. meldTotal=2, modTotal=11
	// (modEach=3, extra=2 → first two modspil get 4, third 3).
	got = DefaultTricks(TypeNolo, 2, []string{RoleMelder, RoleModspil, RoleModspil, RoleModspil})
	want = []int{2, 4, 4, 3}
	if !equalInts(got, want) {
		t.Fatalf("nolo bid=2 (1v3) got=%v want=%v", got, want)
	}
}

func TestDefaultTricksReturnsZerosWhenNotFourActive(t *testing.T) {
	got := DefaultTricks(TypeNormal, 9, []string{RoleMelder, RoleMakker, "", ""})
	for i, n := range got {
		if n != 0 {
			t.Fatalf("pos=%d got=%d want=0 (only 2 active)", i, n)
		}
	}
}

func equalInts(a, b []int) bool {
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

func hasIssue(issues []ValidationIssue, want ValidationIssue) bool {
	for _, issue := range issues {
		if issue == want {
			return true
		}
	}
	return false
}
