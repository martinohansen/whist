package main

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/martinohansen/whist/internal/db"
)

const (
	recentCookieName = "whist_recent"
	recentMaxAge     = 365 * 24 * time.Hour
	recentMax        = 10
)

// RecentClub is the trimmed view stored in the cookie.
type RecentClub struct {
	ID    string `json:"i"`
	Name  string `json:"n"`
	Emoji string `json:"e,omitempty"`
}

func readRecentClubs(r *http.Request) []RecentClub {
	c, err := r.Cookie(recentCookieName)
	if err != nil {
		return nil
	}
	raw, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var out []RecentClub
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil
	}
	return out
}

func writeRecentClubs(w http.ResponseWriter, list []RecentClub) {
	raw, err := json.Marshal(list)
	if err != nil {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     recentCookieName,
		Value:    base64.RawURLEncoding.EncodeToString(raw),
		Path:     "/",
		MaxAge:   int(recentMaxAge.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// touchRecent prepends/refreshes the cookie entry for c.
func touchRecent(w http.ResponseWriter, r *http.Request, c db.Club) {
	list := readRecentClubs(r)
	pruned := make([]RecentClub, 0, len(list)+1)
	pruned = append(pruned, RecentClub{ID: c.ID, Name: c.Name, Emoji: c.Emoji})
	for _, e := range list {
		if e.ID == c.ID {
			continue
		}
		pruned = append(pruned, e)
		if len(pruned) >= recentMax {
			break
		}
	}
	writeRecentClubs(w, pruned)
}
