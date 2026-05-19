package mistral

import (
	"strings"
	"testing"

	"github.com/martinohansen/whist/internal/db"
)

func TestValidateParsedNote(t *testing.T) {
	players := []db.Player{
		{Name: "Anne"},
		{Name: "Bo"},
		{Name: "Carl"},
		{Name: "Dina"},
	}
	meldings := []db.Melding{
		{Name: "8", Type: db.MeldingTypeNormal, Bid: 8},
		{Name: "Sol", Type: db.MeldingTypeNolo, Bid: 2},
	}

	tests := []struct {
		name    string
		note    string
		parsed  parsedNote
		wantErr string
	}{
		{
			name: "unknown player",
			note: "Anne/Bo 8 -> 9",
			parsed: parsedWithRows(gameRow("Anne/Bo 8 -> 9", DraftGame{
				MeldingName: "8",
				Players: []DraftPlayer{
					{Name: "Eli", Role: "melder", Tricks: 5},
					{Name: "Bo", Role: "makker", Tricks: 4},
					{Name: "Carl", Role: "modspil", Tricks: 2},
					{Name: "Dina", Role: "modspil", Tricks: 2},
				},
			})),
			wantErr: `unknown player name "Eli"`,
		},
		{
			name: "combined player name",
			note: "Anne/Bo 8 -> 9",
			parsed: parsedWithRows(gameRow("Anne/Bo 8 -> 9", DraftGame{
				MeldingName: "8",
				Players: []DraftPlayer{
					{Name: "Anne/Bo", Role: "melder", Tricks: 5},
					{Name: "Carl", Role: "makker", Tricks: 4},
					{Name: "Anne", Role: "modspil", Tricks: 2},
					{Name: "Dina", Role: "modspil", Tricks: 2},
				},
			})),
			wantErr: "combined or unknown player name",
		},
		{
			name: "duplicate player",
			note: "Anne/Bo 8 -> 9",
			parsed: parsedWithRows(gameRow("Anne/Bo 8 -> 9", DraftGame{
				MeldingName: "8",
				Players: []DraftPlayer{
					{Name: "Anne", Role: "melder", Tricks: 5},
					{Name: "Anne", Role: "makker", Tricks: 4},
					{Name: "Carl", Role: "modspil", Tricks: 2},
					{Name: "Dina", Role: "modspil", Tricks: 2},
				},
			})),
			wantErr: `duplicate player "Anne"`,
		},
		{
			name: "trick total not summing to 13",
			note: "bad trick row",
			parsed: parsedWithRows(gameRow("bad trick row", DraftGame{
				MeldingName: "8",
				Players: []DraftPlayer{
					{Name: "Anne", Role: "melder", Tricks: 4},
					{Name: "Bo", Role: "makker", Tricks: 4},
					{Name: "Carl", Role: "modspil", Tricks: 2},
					{Name: "Dina", Role: "modspil", Tricks: 2},
				},
			})),
			wantErr: "tricks sum to 12, want 13",
		},
		{
			name: "invalid role count for normal melding",
			note: "Anne/Bo 8 -> 9",
			parsed: parsedWithRows(gameRow("Anne/Bo 8 -> 9", DraftGame{
				MeldingName: "8",
				Players: []DraftPlayer{
					{Name: "Anne", Role: "melder", Tricks: 5},
					{Name: "Bo", Role: "modspil", Tricks: 4},
					{Name: "Carl", Role: "modspil", Tricks: 2},
					{Name: "Dina", Role: "modspil", Tricks: 2},
				},
			})),
			wantErr: "makker count is 0, want 1 for normal melding",
		},
		{
			name: "invalid role count for nolo",
			note: "Anne Sol 1",
			parsed: parsedWithRows(gameRow("Anne Sol 1", DraftGame{
				MeldingName: "Sol",
				Players: []DraftPlayer{
					{Name: "Anne", Role: "melder", Tricks: 1},
					{Name: "Bo", Role: "makker", Tricks: 3},
					{Name: "Carl", Role: "makker", Tricks: 4},
					{Name: "Dina", Role: "modspil", Tricks: 5},
				},
			})),
			wantErr: "makker count is 2, want 0 or 1 for nolo melding",
		},
		{
			name:    "missing row accounting",
			note:    "Anne/Bo 8 -> 9\nCarl/Dina 8 -> 7",
			parsed:  parsedWithRows(gameRow("Anne/Bo 8 -> 9", validNormalGame())),
			wantErr: `source row "Carl/Dina 8 -> 7" is not accounted`,
		},
		{
			name: "valid ambiguous row omitted safely",
			note: "unreadable game row",
			parsed: parsedWithRows(parsedRow{
				SourceRow: "unreadable game row",
				Decision:  "ambiguous",
				Reason:    "cannot resolve players",
				Game:      DraftGame{},
			}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			games, errs := validateParsedNote(tt.note, tt.parsed, meldings, players)
			gotErr := strings.Join(errs, "\n")
			if tt.wantErr == "" {
				if gotErr != "" {
					t.Fatalf("validateParsedNote errors:\n%s", gotErr)
				}
				if len(games) != 0 {
					t.Fatalf("games = %d, want 0", len(games))
				}
				return
			}
			if !strings.Contains(gotErr, tt.wantErr) {
				t.Fatalf("validateParsedNote errors:\n%s\nwant substring %q", gotErr, tt.wantErr)
			}
		})
	}
}

func parsedWithRows(rows ...parsedRow) parsedNote {
	return parsedNote{
		Summary:             "test parse",
		NotationAssumptions: []string{},
		PlayerAliases:       []parsedPlayerAlias{},
		Rows:                rows,
	}
}

func gameRow(source string, game DraftGame) parsedRow {
	return parsedRow{
		SourceRow: source,
		Decision:  "game",
		Reason:    "resolved test row",
		Game:      game,
	}
}

func validNormalGame() DraftGame {
	return DraftGame{
		MeldingName: "8",
		Players: []DraftPlayer{
			{Name: "Anne", Role: "melder", Tricks: 5},
			{Name: "Bo", Role: "makker", Tricks: 4},
			{Name: "Carl", Role: "modspil", Tricks: 2},
			{Name: "Dina", Role: "modspil", Tricks: 2},
		},
	}
}
