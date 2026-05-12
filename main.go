package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"

	"github.com/carlmjohnson/versioninfo"
	"github.com/martinohansen/whist/internal/db"
)

func main() {
	store, err := db.Open("whist.db")
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer store.Close()

	app := newApp(store)

	port := "8080"
	if portEnv := os.Getenv("WHIST_PORT"); portEnv != "" {
		port = portEnv
	}

	rev := versioninfo.Revision
	if len(rev) >= 7 {
		rev = rev[:7]
	}
	slog.Info("listening on http://localhost:"+port, "version", rev)
	if err := http.ListenAndServe(":"+port, app.routes()); err != nil {
		log.Fatal(err)
	}
}
