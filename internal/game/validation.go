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

// DefaultTricks distributes 13 tricks across the given role positions using
// the heuristics the new-game form previously did client-side. roles is
// positional (cards in display order); the returned slice has the same
// length. Empty or unknown roles map to 0. Only computes a distribution
// when exactly four positions are active; otherwise returns all zeros.
//
// Normal: melder gets ceil(bid/2), makker floor(bid/2). The first modspil
// in position order gets ceil((13-bid)/2), the second floor((13-bid)/2).
// Nolo: each meld-side seat aims for `bid` tricks, capped at 13 in total;
// the remainder is split among modspil. Order-of-position determines who
// gets the +1 when the split is uneven.
func DefaultTricks(meldingType string, bid int, roles []string) []int {
	out := make([]int, len(roles))
	var meldCount, makkerCount, modspilCount int
	for _, r := range roles {
		switch r {
		case RoleMelder:
			meldCount++
		case RoleMakker:
			makkerCount++
		case RoleModspil:
			modspilCount++
		}
	}
	if meldCount+makkerCount+modspilCount != 4 {
		return out
	}

	if meldingType == TypeNolo {
		meldSeats := meldCount + makkerCount
		meldTotal := min(meldSeats*bid, 13)
		modTotal := 13 - meldTotal
		meldEach, meldExtra := 0, 0
		if meldSeats > 0 {
			meldEach = meldTotal / meldSeats
			meldExtra = meldTotal - meldEach*meldSeats
		}
		modEach, modExtra := 0, 0
		if modspilCount > 0 {
			modEach = modTotal / modspilCount
			modExtra = modTotal - modEach*modspilCount
		}
		for i, r := range roles {
			switch r {
			case RoleMelder, RoleMakker:
				if meldExtra > 0 {
					out[i] = meldEach + 1
					meldExtra--
				} else {
					out[i] = meldEach
				}
			case RoleModspil:
				if modExtra > 0 {
					out[i] = modEach + 1
					modExtra--
				} else {
					out[i] = modEach
				}
			}
		}
		return out
	}

	meldEach := bid / 2
	meldExtra := bid - meldEach*2
	rem := 13 - bid
	modEach := rem / 2
	modExtra := rem - modEach*2
	modGiven := 0
	for i, r := range roles {
		switch r {
		case RoleMelder:
			out[i] = meldEach + meldExtra
		case RoleMakker:
			out[i] = meldEach
		case RoleModspil:
			if modGiven == 0 {
				out[i] = modEach + modExtra
				modGiven++
			} else {
				out[i] = modEach
			}
		}
	}
	return out
}

// IssueMessage maps a ValidationIssue to the Danish user-facing string used
// by both the manual entry form (new.go) and the import-review flow
// (import.go). meldingType lets nolo-specific wording diverge where the
// rules call for it.
func IssueMessage(meldingType string, issue ValidationIssue) string {
	switch issue {
	case IssuePlayerCount:
		return "Skal have 4 spillere."
	case IssueMelderCount:
		return "Præcis én melder."
	case IssueMakkerCount:
		return "Præcis én makker."
	case IssueModspilCount:
		if meldingType == TypeNolo {
			return "Skal have tre andre spillere."
		}
		return "Præcis to modspil."
	case IssueTrickRange:
		return "Stik skal være 0–13."
	case IssueTrickSum:
		return "Stik skal være 13 i alt."
	}
	return ""
}
