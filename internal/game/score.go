// Package game holds pure game-rule logic with no dependency on storage.
package game

// Roles. The melder and their makker form the "melding side"; modspil are
// the opponents. In nolo, makkers can optionally join the melder (or not) —
// any of {1v3, 2v2, 3v1, 4v0} configurations is allowed.
const (
	RoleMelder  = "melder"
	RoleMakker  = "makker"
	RoleModspil = "modspil"
)

// Melding types.
const (
	TypeNormal = "normal"
	TypeNolo   = "nolo"
)

// PlayerEntry is a single player's input for a game.
type PlayerEntry struct {
	PlayerID int
	Role     string
	Tricks   int
}

// ComputeScores returns the per-player point delta for a single game.
//
// Normal (melder + makker vs. two modspil), bid is the trick target:
//
//	Success (meld_tricks ≥ bid): each meld-side player earns
//	  points × (over + 1), where over = meld_tricks − bid.
//	Failure: each meld-side player loses points × (bid − meld_tricks).
//	Each modspil receives the negation, so the four players sum to zero.
//
// Nolo (melder + optional makkers vs. modspil), bid is the maximum tricks
// allowed. Joiners are NOT a team — each meld-side player has an independent
// bet that *their own* tricks ≤ bid. With o = modspil count, each meld-side
// player M with diff_M = bid − tricks_M earns perOpp_M × o (perOpp_M =
// points×(diff_M+1) on success, points×diff_M on failure). Each modspil
// receives −Σ_M perOpp_M. With one melder and three modspil this reduces
// to the classic +3·perOpp / −perOpp split. Zero-sum across all four.
func ComputeScores(meldingType string, bid, meldingPoints int, entries []PlayerEntry) map[int]int {
	if meldingType == TypeNolo {
		return computeNolo(bid, meldingPoints, entries)
	}
	return computeNormal(bid, meldingPoints, entries)
}

func computeNormal(bid, meldingPoints int, entries []PlayerEntry) map[int]int {
	meldTricks := 0
	for _, e := range entries {
		if e.Role == RoleMelder || e.Role == RoleMakker {
			meldTricks += e.Tricks
		}
	}
	diff := meldTricks - bid
	var meldScore int
	if diff >= 0 {
		meldScore = meldingPoints * (diff + 1)
	} else {
		meldScore = meldingPoints * diff
	}
	out := make(map[int]int, len(entries))
	for _, e := range entries {
		switch e.Role {
		case RoleMelder, RoleMakker:
			out[e.PlayerID] = meldScore
		default:
			out[e.PlayerID] = -meldScore
		}
	}
	return out
}

func computeNolo(bid, meldingPoints int, entries []PlayerEntry) map[int]int {
	modCount := 0
	perOpp := make(map[int]int, len(entries))
	sumPerOpp := 0
	for _, e := range entries {
		switch e.Role {
		case RoleMelder, RoleMakker:
			diff := bid - e.Tricks
			var p int
			if diff >= 0 {
				p = meldingPoints * (diff + 1)
			} else {
				p = meldingPoints * diff
			}
			perOpp[e.PlayerID] = p
			sumPerOpp += p
		case RoleModspil:
			modCount++
		}
	}
	out := make(map[int]int, len(entries))
	for _, e := range entries {
		switch e.Role {
		case RoleMelder, RoleMakker:
			out[e.PlayerID] = modCount * perOpp[e.PlayerID]
		case RoleModspil:
			out[e.PlayerID] = -sumPerOpp
		}
	}
	return out
}
