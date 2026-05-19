// Package mistral wraps the subset of the Mistral AI HTTP API we need:
// OCR (image → markdown) and a chat completion call that returns a JSON
// list of draft whist games extracted from the OCR text.
package mistral

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/martinohansen/whist/internal/db"
)

const (
	apiBase     = "https://api.mistral.ai/v1"
	ocrModel    = "mistral-ocr-latest"
	chatModel   = "mistral-large-latest"
	defaultMime = "image/jpeg"
)

var integerPattern = regexp.MustCompile(`\d+`)
var signedResultPattern = regexp.MustCompile(`(^|[^0-9])([+-][0-9]+)`)

// Client is a thin Mistral HTTP client.
type Client struct {
	apiKey string
	http   *http.Client
	base   string
}

// New returns a client. Pass an empty apiKey to disable the feature; callers
// should check Enabled() before using it.
func New(apiKey string) *Client {
	return &Client{
		apiKey: apiKey,
		http:   &http.Client{Timeout: 90 * time.Second},
		base:   apiBase,
	}
}

func (c *Client) Enabled() bool { return c != nil && c.apiKey != "" }

// DraftGame is the LLM-extracted structure for a single game on the paper.
type DraftGame struct {
	PlayedAt    string        `json:"played_at,omitempty"`
	MeldingName string        `json:"melding_name"`
	Note        string        `json:"note,omitempty"`
	Players     []DraftPlayer `json:"players"`
}

type DraftPlayer struct {
	Name   string `json:"name"`
	Role   string `json:"role"`
	Tricks int    `json:"tricks"`
}

type parsedNote struct {
	Summary             string              `json:"summary"`
	NotationAssumptions []string            `json:"notation_assumptions"`
	PlayerAliases       []parsedPlayerAlias `json:"player_aliases"`
	Rows                []parsedRow         `json:"rows"`
}

type parsedPlayerAlias struct {
	Alias string `json:"alias"`
	Name  string `json:"name"`
}

type parsedRow struct {
	SourceRow string    `json:"source_row"`
	Decision  string    `json:"decision"`
	Reason    string    `json:"reason"`
	Game      DraftGame `json:"game"`
}

type playerColumnRoleTable struct {
	PlayersByColumn map[int]string
}

// OCR sends a single image to Mistral's OCR endpoint and returns the
// concatenated markdown from all returned pages.
func (c *Client) OCR(ctx context.Context, img []byte, mime string) (string, error) {
	if mime == "" {
		mime = defaultMime
	}
	slog.DebugContext(ctx, "mistral.ocr request", "bytes", len(img), "mime", mime, "model", ocrModel)
	body := map[string]any{
		"model": ocrModel,
		"document": map[string]any{
			"type":      "image_url",
			"image_url": "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(img),
		},
	}
	var resp struct {
		Pages []struct {
			Markdown string `json:"markdown"`
		} `json:"pages"`
	}
	if err := c.postJSON(ctx, "/ocr", body, &resp); err != nil {
		return "", err
	}
	parts := make([]string, 0, len(resp.Pages))
	for _, p := range resp.Pages {
		if p.Markdown != "" {
			parts = append(parts, p.Markdown)
		}
	}
	out := strings.Join(parts, "\n\n")
	slog.DebugContext(ctx, "mistral.ocr response",
		"pages", len(resp.Pages),
		"markdown_bytes", len(out),
		"markdown", out,
	)
	return out, nil
}

// Extract turns a markdown blob (typically from OCR) into a list of draft
// games. Existing meldings and players are passed so the LLM picks matching
// names verbatim.
func (c *Client) Extract(ctx context.Context, markdown string, meldings []db.Melding, players []db.Player) ([]DraftGame, error) {
	parsed, err := c.parseNote(ctx, markdown, meldings, players)
	if err != nil {
		return nil, err
	}
	games, validationErrors := validateParsedNote(markdown, parsed, meldings, players)
	if len(validationErrors) == 0 {
		slog.DebugContext(ctx, "mistral.extract validated", "games", len(games))
		return games, nil
	}

	slog.DebugContext(ctx, "mistral.extract validation failed",
		"errors", validationErrors,
		"parsed", parsed,
	)
	repaired, err := c.repairNote(ctx, markdown, parsed, validationErrors, meldings, players)
	if err != nil {
		return nil, err
	}
	games, validationErrors = validateParsedNote(markdown, repaired, meldings, players)
	if len(validationErrors) != 0 {
		return nil, fmt.Errorf("mistral: validated extraction failed after repair: %s", validationSummary(validationErrors))
	}
	slog.DebugContext(ctx, "mistral.extract repair validated", "games", len(games))
	return games, nil
}

func (c *Client) parseNote(ctx context.Context, markdown string, meldings []db.Melding, players []db.Player) (parsedNote, error) {
	system := buildSystemPrompt(meldings, players)
	user := buildParseUserContent(markdown)
	slog.DebugContext(ctx, "mistral.parse request",
		"model", chatModel,
		"meldings", len(meldings),
		"players", len(players),
		"markdown_bytes", len(markdown),
		"system_prompt", system,
		"user_content", user,
	)
	body := map[string]any{
		"model":       chatModel,
		"temperature": 0,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "parsed_whist_note",
				"strict": true,
				"schema": parsedNoteSchema(),
			},
		},
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.postJSON(ctx, "/chat/completions", body, &resp); err != nil {
		return parsedNote{}, err
	}
	if len(resp.Choices) == 0 {
		return parsedNote{}, fmt.Errorf("mistral: no choices in parse response")
	}
	raw := resp.Choices[0].Message.Content
	slog.DebugContext(ctx, "mistral.parse response", "raw_content", raw)
	var parsed parsedNote
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return parsedNote{}, fmt.Errorf("mistral: parse structured note: %w", err)
	}
	slog.DebugContext(ctx, "mistral.parse parsed", "rows", len(parsed.Rows))
	return parsed, nil
}

func (c *Client) repairNote(ctx context.Context, markdown string, parsed parsedNote, validationErrors []string, meldings []db.Melding, players []db.Player) (parsedNote, error) {
	system := buildRepairPrompt(meldings, players)
	previous, err := json.Marshal(parsed)
	if err != nil {
		return parsedNote{}, err
	}
	user := fmt.Sprintf("Original markdown:\n%s\n\nMeaningful source rows that must be copied exactly when used as source_row:\n%s\n\nPrevious structured parse:\n%s\n\nValidation errors to fix:\n%s",
		markdown,
		sourceRowsForPrompt(markdown),
		string(previous),
		strings.Join(validationErrors, "\n"),
	)
	slog.DebugContext(ctx, "mistral.repair request",
		"model", chatModel,
		"meldings", len(meldings),
		"players", len(players),
		"markdown_bytes", len(markdown),
		"validation_errors", validationErrors,
		"system_prompt", system,
	)
	body := map[string]any{
		"model":       chatModel,
		"temperature": 0,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": user},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "repaired_whist_note",
				"strict": true,
				"schema": parsedNoteSchema(),
			},
		},
	}
	var resp struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := c.postJSON(ctx, "/chat/completions", body, &resp); err != nil {
		return parsedNote{}, err
	}
	if len(resp.Choices) == 0 {
		return parsedNote{}, fmt.Errorf("mistral: no choices in repair response")
	}
	raw := resp.Choices[0].Message.Content
	slog.DebugContext(ctx, "mistral.repair response", "raw_content", raw)
	var repaired parsedNote
	if err := json.Unmarshal([]byte(raw), &repaired); err != nil {
		return parsedNote{}, fmt.Errorf("mistral: parse repaired note: %w", err)
	}
	return repaired, nil
}

func buildParseUserContent(markdown string) string {
	return fmt.Sprintf("Original markdown:\n%s\n\nMeaningful source rows that must be accounted for as game, skip, or ambiguous. Copy these exactly when they are used as source_row:\n%s",
		markdown,
		sourceRowsForPrompt(markdown),
	)
}

func sourceRowsForPrompt(markdown string) string {
	rows := meaningfulSourceRows(markdown)
	if len(rows) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for _, row := range rows {
		fmt.Fprintf(&b, " - %s\n", row)
	}
	return strings.TrimRight(b.String(), "\n")
}

func buildSystemPrompt(meldings []db.Melding, players []db.Player) string {
	var b strings.Builder
	b.WriteString(`You convert a handwritten whist score note into structured JSON.

Read the whole note before resolving individual rows. Infer the note-local notation: aliases, separators, role markers, repeated row patterns, field order, and the meaning of numeric positions. Apply one consistent note-local interpretation wherever the note supports it.

Use only the supplied club player list and club melding list as source of truth. Player names and melding names in game rows must be copied exactly from those lists. Do not invent names, combine names, or turn a pair of players into one player.

Aliases are note-local and literal. If two player aliases share a prefix, use the alias token that is actually written; do not expand a short written token into a longer alias. For compact player text without separators, split the written player part into a one-to-one sequence of supported aliases that exactly covers the written characters.

Whist invariants:
 - A game has four unique players.
 - Roles are melder, optional makker, and modspil.
 - Normal meldings have exactly one melder, one makker, and two modspil.
 - Nolo meldings have exactly one melder, zero or one makker, and the remaining players as modspil.
 - Tricks are integers from 0 to 13 and sum to 13 for the game.
 - The melding/bid and the result/trick value are different facts. When a row has both, choose the melding from the first melding/bid token and choose tricks from the later result/trick token.
 - For a normal melding, a single result usually means the melder+makker team trick total. Assign melder+makker tricks so their sum equals that result, not the bid.
 - For a normal melding with a signed result, compute the team trick total as the melding bid plus the signed delta; equality means the team trick total equals the bid.
 - For a repeated row pattern with two numeric positions, infer once which position is the melding/bid and which is the result/trick value, then apply that consistently to every row with that pattern.
 - For a nolo, a numeric or textual melding token identifies the nolo. Later trick values are outcomes and must not rematch the row to a different nolo.
 - For numeric nolo notation, match the written melding token to the nolo whose club-list bid equals that token. Do not choose a nolo because its bid equals a later trick/result value.
 - For textual nolo notation, the club-list bid is only the melding threshold. Do not use that bid as a player's trick count unless the note writes it as a result value.
 - For textual nolo notation, match the written text to the club melding name that is actually written. Do not upgrade a shorter written nolo name to a longer club nolo name unless the extra words are present in the source row.
 - For a nolo row that names two nolo-side players, the first is melder and the second is makker unless the note explicitly says otherwise.
 - For a nolo with separate player trick values, keep those values with the written players in order.
 - For numeric nolo notation with two later trick values, the nolo numeric token is only the melding. The later values are the melder and makker tricks in written player order.

Notation guidance:
 - Notes often repeat one compact pattern for many games. Infer the field order from the whole note and apply it consistently.
 - Separators such as arrows, dashes, slashes, colons, equals signs, commas, spaces, or adjacency are notation. They do not let you ignore a later result value.
 - In a normal-melding row with two numeric groups after the player part, the first numeric group is the melding/bid and the later numeric group is the melder+makker team trick total.
 - In compact normal rows written as player-part plus number/number or number-number, read that as bid/result for all rows using that pattern, even when the result is lower than the bid.
 - If the note gives only a normal team's total tricks and no individual split, put that total on the melder and 0 on the makker. The important invariant is that melder+makker equals the written team total.
 - If a table has one column per player and row cells contain repeated role markers, infer those marker meanings from the whole table. A marker belongs to the player column where it appears; blank or dash cells are not melding-side players.
 - Do not mark a terse row ambiguous when it follows a repeated pattern already resolved elsewhere on the same note.

Return a structured intermediate parse, not just games:
 - summary: brief note-level summary.
 - notation_assumptions: note-local assumptions used for aliases, markers, separators, and numeric positions.
 - player_aliases: only aliases supported by the note and resolved to one supplied player.
 - rows: one decision for every meaningful non-empty source row or row group.

Each row decision must be one of:
 - game: row is a valid game; fill game.
 - skip: row is a heading, legend, prose with no game, table header, or unreadable non-game row.
 - ambiguous: row may contain a game but cannot be resolved confidently from the whole note and club data.

For each row, source_row must quote the original row text or row group closely enough that each meaningful source line is accounted for. For multi-line game blocks, use the full block as source_row.

Mark genuinely ambiguous rows as ambiguous instead of guessing. Do not omit a meaningful row silently. Do not add games not present in the note.

`)
	writeClubContext(&b, meldings, players)
	return b.String()
}

func buildRepairPrompt(meldings []db.Melding, players []db.Player) string {
	var b strings.Builder
	b.WriteString(`Repair a structured whist note parse so it passes deterministic validation.

Use the original markdown and the validation errors. Keep valid row decisions when possible, but correct any invalid games and add missing row decisions. If a row cannot be resolved confidently after reading the whole note, mark it ambiguous. Return the full corrected structured parse.

Rules for applying validation errors:
 - Copy source_row values from the provided meaningful source-row list; do not rewrite or normalize them.
 - If an error says the source row implies normal meld-side tricks N, change the game so melder+makker tricks sum to N and modspil tricks sum to 13-N.
 - If an error says the source row implies nolo melder or makker tricks N, use those exact trick values for those roles.
 - If an error says the source row implies a different nolo melding, change melding_name to that exact supplied club melding name.
 - If a source row writes a shorter nolo name, do not change it to a longer nolo name unless the longer name's extra words are present in source_row.
 - If an error says a leading or second player token implies a role, change the role assignment to match the token rather than changing the source_row.

`)
	writeClubContext(&b, meldings, players)
	return b.String()
}

func writeClubContext(b *strings.Builder, meldings []db.Melding, players []db.Player) {
	if len(meldings) > 0 {
		b.WriteString("Club meldings (choose EXACTLY ONE name from this list for melding_name):\n")
		for _, m := range meldings {
			fmt.Fprintf(b, " - %s (type=%s, bid=%d, points=%d)\n", m.Name, m.Type, m.Bid, m.Points)
		}
		writeNoloNumericNotation(b, meldings)
	}
	if len(players) > 0 {
		b.WriteString("Club players (choose EXACTLY ONE name from this list for each player.name):\n")
		for _, p := range players {
			b.WriteString(" - ")
			b.WriteString(p.Name)
			b.WriteString("\n")
		}
	}
}

func writeNoloNumericNotation(b *strings.Builder, meldings []db.Melding) {
	byBid := map[int][]db.Melding{}
	for _, melding := range meldings {
		if melding.Type == db.MeldingTypeNolo {
			byBid[melding.Bid] = append(byBid[melding.Bid], melding)
		}
	}
	if len(byBid) == 0 {
		b.WriteString("\n")
		return
	}
	bids := make([]int, 0, len(byBid))
	for bid := range byBid {
		bids = append(bids, bid)
	}
	sort.Ints(bids)
	b.WriteString("Nolo numeric notation from the club list; when this number is the written melding token, choose this nolo:\n")
	for _, bid := range bids {
		names := make([]string, 0, len(byBid[bid]))
		for _, melding := range byBid[bid] {
			names = append(names, melding.Name)
		}
		sort.Strings(names)
		fmt.Fprintf(b, " - %d -> %s\n", bid, strings.Join(names, " or "))
	}
	b.WriteString("Later trick/result values do not change this nolo choice.\n\n")
}

func parsedNoteSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"summary": map[string]any{"type": "string"},
			"notation_assumptions": map[string]any{
				"type":  "array",
				"items": map[string]any{"type": "string"},
			},
			"player_aliases": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"alias": map[string]any{"type": "string"},
						"name":  map[string]any{"type": "string"},
					},
					"required":             []string{"alias", "name"},
					"additionalProperties": false,
				},
			},
			"rows": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"source_row": map[string]any{"type": "string"},
						"decision":   map[string]any{"type": "string", "enum": []string{"game", "skip", "ambiguous"}},
						"reason":     map[string]any{"type": "string"},
						"game":       draftGameSchema(0, 4),
					},
					"required":             []string{"source_row", "decision", "reason", "game"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"summary", "notation_assumptions", "player_aliases", "rows"},
		"additionalProperties": false,
	}
}

func draftGameSchema(minPlayers, maxPlayers int) map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"played_at":    map[string]any{"type": "string", "description": "YYYY-MM-DD or empty"},
			"melding_name": map[string]any{"type": "string"},
			"note":         map[string]any{"type": "string"},
			"players": map[string]any{
				"type":     "array",
				"minItems": minPlayers,
				"maxItems": maxPlayers,
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"name":   map[string]any{"type": "string"},
						"role":   map[string]any{"type": "string", "enum": []string{"melder", "makker", "modspil"}},
						"tricks": map[string]any{"type": "integer", "minimum": 0, "maximum": 13},
					},
					"required":             []string{"name", "role", "tricks"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"melding_name", "players", "played_at", "note"},
		"additionalProperties": false,
	}
}

func validateParsedNote(markdown string, parsed parsedNote, meldings []db.Melding, players []db.Player) ([]DraftGame, []string) {
	var errors []string
	meldingByName := map[string]db.Melding{}
	for _, melding := range meldings {
		meldingByName[melding.Name] = melding
	}
	playerNames := map[string]bool{}
	for _, player := range players {
		playerNames[player.Name] = true
	}
	playerAliases := playerAliasMap(parsed.PlayerAliases, playerNames)
	for _, player := range players {
		playerAliases[player.Name] = player.Name
	}
	roleTable := inferPlayerColumnRoleTable(markdown, players)

	sourceRows := meaningfulSourceRows(markdown)
	accountedRows := make([]string, 0, len(parsed.Rows))
	seenAccountedRows := map[string]bool{}
	games := make([]DraftGame, 0, len(parsed.Rows))
	for i, row := range parsed.Rows {
		rowRef := fmt.Sprintf("row[%d]", i+1)
		source := strings.TrimSpace(row.SourceRow)
		switch row.Decision {
		case "game", "skip", "ambiguous":
		default:
			errors = append(errors, fmt.Sprintf("%s: decision %q must be game, skip, or ambiguous", rowRef, row.Decision))
		}
		if source == "" {
			errors = append(errors, fmt.Sprintf("%s: source_row is empty", rowRef))
		} else {
			normalizedSource := normalizeSource(source)
			if seenAccountedRows[normalizedSource] {
				errors = append(errors, fmt.Sprintf("%s: duplicate source_row %q", rowRef, source))
			}
			seenAccountedRows[normalizedSource] = true
			accountedRows = append(accountedRows, normalizedSource)
		}
		if row.Decision != "game" {
			continue
		}
		game := canonicalizeClearSourceFacts(row.SourceRow, row.Game, meldingByName, roleTable)
		if source != "" && !sourceRowCoversMeaningfulLine(source, sourceRows) {
			errors = append(errors, fmt.Sprintf("%s: game source_row %q does not cover a complete source row", rowRef, source))
		}
		if gameErrors := validateDraftGame(rowRef, row.SourceRow, game, meldingByName, playerNames, playerAliases); len(gameErrors) != 0 {
			errors = append(errors, gameErrors...)
			continue
		}
		games = append(games, game)
	}

	for _, sourceRow := range sourceRows {
		normalized := normalizeSource(sourceRow)
		found := false
		for _, accounted := range accountedRows {
			if accounted == normalized || strings.Contains(accounted, normalized) {
				found = true
				break
			}
		}
		if !found {
			errors = append(errors, fmt.Sprintf("source row %q is not accounted for as game, skip, or ambiguous", sourceRow))
		}
	}

	return games, errors
}

func sourceRowCoversMeaningfulLine(source string, sourceRows []string) bool {
	normalizedSource := normalizeSource(source)
	for _, sourceRow := range sourceRows {
		normalizedRow := normalizeSource(sourceRow)
		if normalizedSource == normalizedRow || strings.Contains(normalizedSource, normalizedRow) {
			return true
		}
	}
	return false
}

func canonicalizeClearSourceFacts(sourceRow string, game DraftGame, meldingByName map[string]db.Melding, roleTable playerColumnRoleTable) DraftGame {
	game.Players = normalizePlayerColumnRoles(sourceRow, game.Players, roleTable)
	textual := textualNoloMatches(sourceRow, meldingByName)
	if len(textual) == 1 {
		if meldingByName[textual[0]].Type == db.MeldingTypeNolo {
			game.MeldingName = textual[0]
		}
	}
	current, ok := meldingByName[game.MeldingName]
	if !ok {
		return game
	}
	switch current.Type {
	case db.MeldingTypeNormal:
		if expected, found := normalMeldSideTricks(sourceRow, current); found {
			game.Players = normalizeTeamTricks(game.Players, expected)
		}
		return game
	case db.MeldingTypeNolo:
		nums := intsInString(sourceRow)
		if len(textual) == 0 && len(nums) >= 2 {
			var matches []string
			for name, melding := range meldingByName {
				if melding.Type == db.MeldingTypeNolo && melding.Bid == nums[0] {
					matches = append(matches, name)
				}
			}
			if len(matches) == 1 {
				game.MeldingName = matches[0]
			}
		}
		game.Players = normalizeNoloTricks(sourceRow, game.Players)
		return game
	default:
		return game
	}
}

func inferPlayerColumnRoleTable(markdown string, players []db.Player) playerColumnRoleTable {
	playerNames := map[string]bool{}
	for _, player := range players {
		playerNames[player.Name] = true
	}
	for _, line := range strings.Split(markdown, "\n") {
		if !strings.Contains(line, "|") {
			continue
		}
		cells := splitTableCells(line)
		playersByColumn := map[int]string{}
		for i, cell := range cells {
			if playerNames[cell] {
				playersByColumn[i] = cell
			}
		}
		if len(playersByColumn) >= 2 {
			return playerColumnRoleTable{PlayersByColumn: playersByColumn}
		}
	}
	return playerColumnRoleTable{}
}

func normalizePlayerColumnRoles(sourceRow string, players []DraftPlayer, roleTable playerColumnRoleTable) []DraftPlayer {
	if len(roleTable.PlayersByColumn) == 0 || !strings.Contains(sourceRow, "|") {
		return players
	}
	cells := splitTableCells(sourceRow)
	var melder, makker string
	for i, cell := range cells {
		name, ok := roleTable.PlayersByColumn[i]
		if !ok {
			continue
		}
		switch strings.ToUpper(strings.TrimSpace(cell)) {
		case "M":
			melder = name
		case "X":
			makker = name
		}
	}
	if melder == "" {
		return players
	}
	out := append([]DraftPlayer(nil), players...)
	for i := range out {
		switch out[i].Name {
		case melder:
			out[i].Role = "melder"
		case makker:
			out[i].Role = "makker"
		default:
			out[i].Role = "modspil"
		}
	}
	return out
}

func splitTableCells(line string) []string {
	parts := strings.Split(strings.Trim(line, "|"), "|")
	cells := make([]string, 0, len(parts))
	for _, part := range parts {
		cells = append(cells, strings.TrimSpace(part))
	}
	return cells
}

func normalizeTeamTricks(players []DraftPlayer, meldSide int) []DraftPlayer {
	out := append([]DraftPlayer(nil), players...)
	modspilLeft := 13 - meldSide
	for i := range out {
		switch out[i].Role {
		case "melder":
			out[i].Tricks = meldSide
		case "makker":
			out[i].Tricks = 0
		case "modspil":
			out[i].Tricks = modspilLeft
			modspilLeft = 0
		}
	}
	return out
}

func normalizeNoloTricks(sourceRow string, players []DraftPlayer) []DraftPlayer {
	nums := intsInString(sourceRow)
	if len(nums) == 0 {
		return players
	}
	out := append([]DraftPlayer(nil), players...)
	makkerCount := 0
	for _, player := range out {
		if player.Role == "makker" {
			makkerCount++
		}
	}
	meldSide := nums[len(nums)-1]
	if makkerCount > 0 && len(nums) >= 2 {
		meldSide = nums[len(nums)-2] + nums[len(nums)-1]
	}
	modspilLeft := 13 - meldSide
	for i := range out {
		switch out[i].Role {
		case "melder":
			if makkerCount > 0 && len(nums) >= 2 {
				out[i].Tricks = nums[len(nums)-2]
			} else {
				out[i].Tricks = nums[len(nums)-1]
			}
		case "makker":
			out[i].Tricks = nums[len(nums)-1]
		case "modspil":
			out[i].Tricks = modspilLeft
			modspilLeft = 0
		}
	}
	return out
}

func playerAliasMap(aliases []parsedPlayerAlias, playerNames map[string]bool) map[string]string {
	out := map[string]string{}
	for _, alias := range aliases {
		if alias.Alias == "" || !playerNames[alias.Name] {
			continue
		}
		out[alias.Alias] = alias.Name
	}
	return out
}

func validateDraftGame(rowRef, sourceRow string, game DraftGame, meldingByName map[string]db.Melding, playerNames map[string]bool, playerAliases map[string]string) []string {
	var errors []string
	melding, ok := meldingByName[game.MeldingName]
	if !ok {
		errors = append(errors, fmt.Sprintf("%s: unknown melding_name %q", rowRef, game.MeldingName))
	}
	if len(game.Players) != 4 {
		errors = append(errors, fmt.Sprintf("%s: has %d players, want 4", rowRef, len(game.Players)))
	}

	seen := map[string]bool{}
	roleCounts := map[string]int{}
	trickSum := 0
	for _, player := range game.Players {
		if !playerNames[player.Name] {
			if looksCombinedPlayerName(player.Name, playerNames) {
				errors = append(errors, fmt.Sprintf("%s: combined or unknown player name %q; use one exact club player per entry", rowRef, player.Name))
			} else {
				errors = append(errors, fmt.Sprintf("%s: unknown player name %q", rowRef, player.Name))
			}
		}
		if seen[player.Name] {
			errors = append(errors, fmt.Sprintf("%s: duplicate player %q", rowRef, player.Name))
		}
		seen[player.Name] = true
		switch player.Role {
		case "melder", "makker", "modspil":
			roleCounts[player.Role]++
		default:
			errors = append(errors, fmt.Sprintf("%s: player %q has invalid role %q", rowRef, player.Name, player.Role))
		}
		if player.Tricks < 0 || player.Tricks > 13 {
			errors = append(errors, fmt.Sprintf("%s: player %q has tricks %d, want 0..13", rowRef, player.Name, player.Tricks))
		}
		trickSum += player.Tricks
	}
	if trickSum != 13 {
		errors = append(errors, fmt.Sprintf("%s: tricks sum to %d, want 13", rowRef, trickSum))
	}
	if roleCounts["melder"] != 1 {
		errors = append(errors, fmt.Sprintf("%s: melder count is %d, want 1", rowRef, roleCounts["melder"]))
	}
	if ok {
		if roleErrors := validateLeadingRoleAliases(rowRef, sourceRow, game.Players, playerAliases); len(roleErrors) != 0 {
			errors = append(errors, roleErrors...)
		}
		switch melding.Type {
		case db.MeldingTypeNormal:
			meldSide := roleTrickSum(game.Players, "melder") + roleTrickSum(game.Players, "makker")
			if roleCounts["makker"] != 1 {
				errors = append(errors, fmt.Sprintf("%s: makker count is %d, want 1 for normal melding %q", rowRef, roleCounts["makker"], game.MeldingName))
			}
			if roleCounts["modspil"] != 2 {
				errors = append(errors, fmt.Sprintf("%s: modspil count is %d, want 2 for normal melding %q", rowRef, roleCounts["modspil"], game.MeldingName))
			}
			if expected, found := normalMeldSideTricks(sourceRow, melding); found && meldSide != expected {
				errors = append(errors, fmt.Sprintf("%s: source row implies normal meld-side tricks %d, got %d", rowRef, expected, meldSide))
			}
		case db.MeldingTypeNolo:
			if noloChoiceErrors := validateNoloMeldingChoice(rowRef, sourceRow, game.MeldingName, meldingByName); len(noloChoiceErrors) != 0 {
				errors = append(errors, noloChoiceErrors...)
			}
			if roleCounts["makker"] > 1 {
				errors = append(errors, fmt.Sprintf("%s: makker count is %d, want 0 or 1 for nolo melding %q", rowRef, roleCounts["makker"], game.MeldingName))
			}
			wantModspil := 3 - roleCounts["makker"]
			if roleCounts["modspil"] != wantModspil {
				errors = append(errors, fmt.Sprintf("%s: modspil count is %d, want %d for nolo melding %q", rowRef, roleCounts["modspil"], wantModspil, game.MeldingName))
			}
			if noloErrors := validateNoloTricks(rowRef, sourceRow, game.Players, roleCounts["makker"]); len(noloErrors) != 0 {
				errors = append(errors, noloErrors...)
			}
		default:
			errors = append(errors, fmt.Sprintf("%s: melding %q has unknown type %q", rowRef, game.MeldingName, melding.Type))
		}
	}

	return errors
}

func validateLeadingRoleAliases(rowRef, sourceRow string, players []DraftPlayer, playerAliases map[string]string) []string {
	token := leadingPlayerToken(sourceRow)
	if token == "" {
		return nil
	}
	aliases, ok := splitAliasToken(token, playerAliases)
	if !ok || len(aliases) == 0 {
		return nil
	}
	var errors []string
	if expectedMelder, ok := playerAliases[aliases[0]]; ok {
		if actualMelder, ok := rolePlayerName(players, "melder"); ok && actualMelder != expectedMelder {
			errors = append(errors, fmt.Sprintf("%s: source row leading player token implies melder %q, got %q", rowRef, expectedMelder, actualMelder))
		}
	}
	if len(aliases) >= 2 {
		if expectedMakker, ok := playerAliases[aliases[1]]; ok {
			if actualMakker, ok := rolePlayerName(players, "makker"); ok && actualMakker != expectedMakker {
				errors = append(errors, fmt.Sprintf("%s: source row second player token implies makker %q, got %q", rowRef, expectedMakker, actualMakker))
			}
		}
	}
	return errors
}

func splitAliasToken(token string, playerAliases map[string]string) ([]string, bool) {
	type candidate struct {
		aliases []string
		ok      bool
	}
	memo := map[string]candidate{}
	var split func(string) candidate
	split = func(rest string) candidate {
		if rest == "" {
			return candidate{ok: true}
		}
		if cached, ok := memo[rest]; ok {
			return cached
		}
		keys := make([]string, 0, len(playerAliases))
		for alias := range playerAliases {
			if strings.HasPrefix(rest, alias) {
				keys = append(keys, alias)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			if len(keys[i]) == len(keys[j]) {
				return keys[i] < keys[j]
			}
			return len(keys[i]) < len(keys[j])
		})
		best := candidate{}
		for _, alias := range keys {
			next := split(strings.TrimPrefix(rest, alias))
			if !next.ok {
				continue
			}
			aliases := append([]string{alias}, next.aliases...)
			if !best.ok || len(aliases) > len(best.aliases) {
				best = candidate{aliases: aliases, ok: true}
			}
		}
		memo[rest] = best
		return best
	}
	out := split(token)
	return out.aliases, out.ok
}

func leadingPlayerToken(sourceRow string) string {
	s := strings.TrimSpace(sourceRow)
	s = strings.TrimLeftFunc(s, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsDigit(r) || strings.ContainsRune("-.)]:#|", r)
	})
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		if !unicode.IsLetter(r) {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func rolePlayerName(players []DraftPlayer, role string) (string, bool) {
	for _, player := range players {
		if player.Role == role {
			return player.Name, true
		}
	}
	return "", false
}

func validateNoloMeldingChoice(rowRef, sourceRow, selected string, meldingByName map[string]db.Melding) []string {
	textual := textualNoloMatches(sourceRow, meldingByName)
	if len(textual) != 0 {
		for _, name := range textual {
			if name == selected {
				return nil
			}
		}
		return []string{fmt.Sprintf("%s: source row text implies nolo melding %q, got %q", rowRef, strings.Join(textual, " or "), selected)}
	}

	nums := intsInString(sourceRow)
	if len(nums) < 2 {
		return nil
	}
	var matches []string
	for name, melding := range meldingByName {
		if melding.Type == db.MeldingTypeNolo && melding.Bid == nums[0] {
			matches = append(matches, name)
		}
	}
	if len(matches) == 0 {
		return nil
	}
	sort.Strings(matches)
	for _, name := range matches {
		if name == selected {
			return nil
		}
	}
	return []string{fmt.Sprintf("%s: first numeric nolo token implies %q, got %q", rowRef, strings.Join(matches, " or "), selected)}
}

func textualNoloMatches(sourceRow string, meldingByName map[string]db.Melding) []string {
	lower := strings.ToLower(sourceRow)
	var matches []string
	longest := 0
	for name, melding := range meldingByName {
		if melding.Type != db.MeldingTypeNolo {
			continue
		}
		needle := strings.ToLower(name)
		if !strings.Contains(lower, needle) {
			continue
		}
		switch {
		case len(needle) > longest:
			matches = []string{name}
			longest = len(needle)
		case len(needle) == longest:
			matches = append(matches, name)
		}
	}
	sort.Strings(matches)
	return matches
}

func roleTrickSum(players []DraftPlayer, role string) int {
	sum := 0
	for _, player := range players {
		if player.Role == role {
			sum += player.Tricks
		}
	}
	return sum
}

func normalMeldSideTricks(sourceRow string, melding db.Melding) (int, bool) {
	if match := signedResultPattern.FindStringSubmatch(sourceRow); len(match) == 3 {
		signed := match[2]
		sign := 1
		if strings.HasPrefix(signed, "-") {
			sign = -1
		}
		n := 0
		for _, r := range strings.TrimLeft(signed, "+-") {
			n = n*10 + int(r-'0')
		}
		return melding.Bid + sign*n, true
	}
	if strings.Contains(sourceRow, "=") {
		return melding.Bid, true
	}
	nums := intsInString(sourceRow)
	if len(nums) < 2 {
		return 0, false
	}
	lower := strings.ToLower(sourceRow)
	if (strings.Contains(lower, "som modspil") || strings.Contains(lower, "as opponent")) && len(nums) >= 3 {
		return nums[len(nums)-2], true
	}
	return nums[len(nums)-1], true
}

func validateNoloTricks(rowRef, sourceRow string, players []DraftPlayer, makkerCount int) []string {
	nums := intsInString(sourceRow)
	if len(nums) == 0 {
		return nil
	}
	var errors []string
	melderTricks, melderOK := roleTricks(players, "melder")
	if makkerCount == 0 {
		expected := nums[len(nums)-1]
		if melderOK && melderTricks != expected {
			errors = append(errors, fmt.Sprintf("%s: source row implies nolo melder tricks %d, got %d", rowRef, expected, melderTricks))
		}
		return errors
	}
	if len(nums) < 2 {
		return nil
	}
	expectedMelder := nums[len(nums)-2]
	expectedMakker := nums[len(nums)-1]
	makkerTricks, makkerOK := roleTricks(players, "makker")
	if melderOK && melderTricks != expectedMelder {
		errors = append(errors, fmt.Sprintf("%s: source row implies nolo melder tricks %d, got %d", rowRef, expectedMelder, melderTricks))
	}
	if makkerOK && makkerTricks != expectedMakker {
		errors = append(errors, fmt.Sprintf("%s: source row implies nolo makker tricks %d, got %d", rowRef, expectedMakker, makkerTricks))
	}
	return errors
}

func roleTricks(players []DraftPlayer, role string) (int, bool) {
	for _, player := range players {
		if player.Role == role {
			return player.Tricks, true
		}
	}
	return 0, false
}

func intsInString(s string) []int {
	matches := integerPattern.FindAllString(s, -1)
	nums := make([]int, 0, len(matches))
	for _, match := range matches {
		n := 0
		for _, r := range match {
			n = n*10 + int(r-'0')
		}
		nums = append(nums, n)
	}
	return nums
}

func looksCombinedPlayerName(name string, playerNames map[string]bool) bool {
	if strings.ContainsAny(name, "/&,+") {
		return true
	}
	matches := 0
	lowerName := strings.ToLower(name)
	for playerName := range playerNames {
		if strings.Contains(lowerName, strings.ToLower(playerName)) {
			matches++
		}
	}
	return matches > 1
}

func meaningfulSourceRows(markdown string) []string {
	var rows []string
	for _, line := range strings.Split(markdown, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || isMarkdownTableSeparator(line) || isLikelyTableHeader(line) {
			continue
		}
		rows = append(rows, line)
	}
	return rows
}

func isMarkdownTableSeparator(line string) bool {
	trimmed := strings.Trim(line, "| ")
	if trimmed == "" {
		return false
	}
	for _, r := range trimmed {
		if r != '-' && r != ':' && r != '|' && r != ' ' {
			return false
		}
	}
	return strings.Contains(trimmed, "-")
}

func isLikelyTableHeader(line string) bool {
	if !strings.Contains(line, "|") {
		return false
	}
	lower := strings.ToLower(line)
	headerTokens := []string{"runde", "spil", "melder", "makker", "melding", "bud", "stik", "resultat", "res", "søgt"}
	matches := 0
	for _, token := range headerTokens {
		if strings.Contains(lower, token) {
			matches++
		}
	}
	return matches >= 2
}

func normalizeSource(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func validationSummary(errors []string) string {
	const maxErrors = 8
	if len(errors) <= maxErrors {
		return strings.Join(errors, "; ")
	}
	return fmt.Sprintf("%s; and %d more", strings.Join(errors[:maxErrors], "; "), len(errors)-maxErrors)
}

func (c *Client) postJSON(ctx context.Context, path string, body any, out any) error {
	buf, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.base+path, bytes.NewReader(buf))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	start := time.Now()
	resp, err := c.http.Do(req)
	if err != nil {
		slog.DebugContext(ctx, "mistral http error", "path", path, "err", err)
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	slog.DebugContext(ctx, "mistral http response",
		"path", path,
		"status", resp.StatusCode,
		"bytes", len(respBody),
		"elapsed_ms", time.Since(start).Milliseconds(),
	)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("mistral %s: %s: %s", path, resp.Status, strings.TrimSpace(string(respBody)))
	}
	return json.Unmarshal(respBody, out)
}
