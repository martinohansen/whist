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

func hasIssue(issues []ValidationIssue, want ValidationIssue) bool {
	for _, issue := range issues {
		if issue == want {
			return true
		}
	}
	return false
}
