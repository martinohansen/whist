package main

import (
	"log"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/carlmjohnson/versioninfo"
	"github.com/martinohansen/whist/internal/db"
	"github.com/martinohansen/whist/internal/mistral"
)

func main() {
	level := slog.LevelInfo
	switch strings.ToLower(os.Getenv("WHIST_LOG_LEVEL")) {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))

	store, err := db.Open("whist.db")
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	defer store.Close()

	mc := mistral.New(os.Getenv("MISTRAL_API_KEY"))
	if !mc.Enabled() {
		slog.Warn("MISTRAL_API_KEY not set — paper-import flow disabled")
	}
	app := newApp(store, mc)

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
