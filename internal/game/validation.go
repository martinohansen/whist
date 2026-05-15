package game

// ValidationIssue identifies why a set of game entries is not a playable game.
type ValidationIssue string

const (
	IssuePlayerCount  ValidationIssue = "player_count"
	IssueMelderCount  ValidationIssue = "melder_count"
	IssueMakkerCount  ValidationIssue = "makker_count"
	IssueModspilCount ValidationIssue = "modspil_count"
	IssueTrickRange   ValidationIssue = "trick_range"
	IssueTrickSum     ValidationIssue = "trick_sum"
)

// ValidateEntries checks the shared role/trick invariants for a game.
func ValidateEntries(meldingType string, entries []PlayerEntry) []ValidationIssue {
	var issues []ValidationIssue
	if len(entries) != 4 {
		issues = append(issues, IssuePlayerCount)
	}

	var melder, makker, modspil, sum int
	for _, e := range entries {
		switch e.Role {
		case RoleMelder:
			melder++
		case RoleMakker:
			makker++
		case RoleModspil:
			modspil++
		}
		if e.Tricks < 0 || e.Tricks > 13 {
			issues = append(issues, IssueTrickRange)
		}
		sum += e.Tricks
	}

	if melder != 1 {
		issues = append(issues, IssueMelderCount)
	}
	if meldingType == TypeNolo {
		if makker+modspil != 3 {
			issues = append(issues, IssueModspilCount)
		}
		return issues
	}
	if makker != 1 {
		issues = append(issues, IssueMakkerCount)
	}
	if modspil != 2 {
		issues = append(issues, IssueModspilCount)
	}
	if sum != 13 {
		issues = append(issues, IssueTrickSum)
	}
	return issues
}
