package main

import (
	"encoding/json"
	"fmt"
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/carlmjohnson/versioninfo"
	"github.com/martinohansen/whist/internal/db"
)

func renderTemplate(w http.ResponseWriter, tplName string, data any, files ...string) {
	if err := render(tplName, w, data, files...); err != nil {
		http.Error(w, "render error", http.StatusInternalServerError)
	}
}

func render(tplName string, w http.ResponseWriter, data any, files ...string) error {
	for i, f := range files {
		files[i] = filepath.Clean(f)
	}
	funcs := template.FuncMap{
		"add":      func(a, b int) int { return a + b },
		"subtract": func(a, b int) int { return a - b },
		"pps": func(f float64) string {
			// One decimal, Danish convention (comma).
			s := fmt.Sprintf("%.1f", f)
			for i := 0; i < len(s); i++ {
				if s[i] == '.' {
					return s[:i] + "," + s[i+1:]
				}
			}
			return s
		},
		"roleIcon": func(role string) string {
			switch role {
			case "melder":
				return "👑"
			case "makker":
				return "🤝"
			case "modspil":
				return "⚔️"
			}
			return ""
		},
		"roleLabel":           roleLabel,
		"roleLabelForMelding": roleLabelForMelding,
		"version": func() string {
			rev := versioninfo.Revision
			if len(rev) < 7 {
				return rev
			}
			return rev[:7]
		},
		"json": func(v any) template.JS {
			b, _ := json.Marshal(v)
			return template.JS(b)
		},
		"gameTricks": func(g db.Game) string {
			if g.MeldingType == db.MeldingTypeNolo {
				parts := make([]string, 0, len(g.Scores))
				order := []string{"melder", "makker"}
				for _, role := range order {
					for _, s := range g.Scores {
						if s.Role == role {
							parts = append(parts, strconv.Itoa(s.Tricks))
						}
					}
				}
				return strings.Join(parts, ", ")
			}
			sum := 0
			for _, s := range g.Scores {
				if s.Role == "melder" || s.Role == "makker" {
					sum += s.Tricks
				}
			}
			return strconv.Itoa(sum)
		},
	}
	tpl, err := template.New(filepath.Base(files[0])).Funcs(funcs).ParseFS(templateFS, files...)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	return tpl.ExecuteTemplate(w, tplName, data)
}

func roleLabel(role string) string {
	switch role {
	case "melder":
		return "Melder"
	case "makker":
		return "Makker"
	case "modspil":
		return "Modspil"
	}
	return role
}

func roleLabelForMelding(role, meldingType string) string {
	if role == "makker" && meldingType == db.MeldingTypeNolo {
		return "Går med"
	}
	return roleLabel(role)
}
