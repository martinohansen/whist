package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
)

// serverSecret signs unlock cookies. Regenerated each process start; users
// re-enter the club password on server restart. Fine for casual use.
var serverSecret = func() []byte {
	var b [32]byte
	_, _ = rand.Read(b[:])
	return b[:]
}()

func unlockCookieName(clubID string) string {
	return "wu_" + clubID
}

// unlockToken derives a stable cookie value for a (club, password-hash) pair.
// If the password changes, the existing cookie no longer verifies.
func unlockToken(clubID, passwordHash string) string {
	mac := hmac.New(sha256.New, serverSecret)
	mac.Write([]byte(clubID))
	mac.Write([]byte{0})
	mac.Write([]byte(passwordHash))
	return hex.EncodeToString(mac.Sum(nil))
}

func setUnlockCookie(w http.ResponseWriter, clubID, passwordHash string) {
	http.SetCookie(w, &http.Cookie{
		Name:     unlockCookieName(clubID),
		Value:    unlockToken(clubID, passwordHash),
		Path:     "/c/" + clubID,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   60 * 60 * 24 * 30, // 30 days
	})
}

func hasUnlockCookie(r *http.Request, clubID, passwordHash string) bool {
	c, err := r.Cookie(unlockCookieName(clubID))
	if err != nil {
		return false
	}
	return c.Value == unlockToken(clubID, passwordHash)
}
