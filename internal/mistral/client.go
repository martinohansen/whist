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
	"strings"
	"time"

	"github.com/martinohansen/whist/internal/db"
)

const (
	apiBase     = "https://api.mistral.ai/v1"
	ocrModel    = "mistral-ocr-latest"
	chatModel   = "mistral-large-latest"
	defaultMime = "image/jpeg"
)

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
	system := buildSystemPrompt(meldings, players)
	slog.DebugContext(ctx, "mistral.extract request",
		"model", chatModel,
		"meldings", len(meldings),
		"players", len(players),
		"markdown_bytes", len(markdown),
		"system_prompt", system,
		"user_markdown", markdown,
	)
	body := map[string]any{
		"model": chatModel,
		"messages": []map[string]any{
			{"role": "system", "content": system},
			{"role": "user", "content": markdown},
		},
		"response_format": map[string]any{
			"type": "json_schema",
			"json_schema": map[string]any{
				"name":   "draft_games",
				"strict": true,
				"schema": draftSchema(),
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
		return nil, err
	}
	if len(resp.Choices) == 0 {
		return nil, fmt.Errorf("mistral: no choices in response")
	}
	raw := resp.Choices[0].Message.Content
	slog.DebugContext(ctx, "mistral.extract response", "raw_content", raw)
	var wrapper struct {
		Games []DraftGame `json:"games"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return nil, fmt.Errorf("mistral: parse draft games: %w", err)
	}
	slog.DebugContext(ctx, "mistral.extract parsed", "games", len(wrapper.Games))
	return wrapper.Games, nil
}

func buildSystemPrompt(meldings []db.Melding, players []db.Player) string {
	var b strings.Builder
	b.WriteString(`Du er en assistent der konverterer en håndskrevet whist-scoreseddel til struktureret JSON.

Inputtet er markdown udtrukket af OCR fra et foto. Sedlen kan være skrevet på mange måder: tabeller, bullets, prosa, forkortelser, initialer, kolonner — der findes ikke ét fast format. Læs sedlen og udled hvad hvert spil siger.

WHIST-DOMÆNE (det skal du forudsætte):
 - Et spil har én melding, én melder, eventuelt én eller flere makkere, og resten af deltagerne er modspil.
 - Normal melding (et tal-bid): melder + makker spiller sammen mod modspil. Papiret skriver typisk hvor mange stik melde-siden vandt. Sum af stik over alle spillere = 13.
 - Nolo (Sol, Ren sol, og lignende): melder spiller alene eller med en valgfri makker mod resten. Papiret skriver typisk hvor mange stik melder (og makker, hvis nogen) fik. Sum af stik = 13.

KRITISK — BID vs STIK:
 - "Bid" er hvad melde-siden meldte; "stik" er hvad de faktisk vandt. De er to forskellige tal.
 - Et spil har næsten altid to tal: et bid og et stik. De kan stå adskilt af "→", "->", "-", "/", ":", "=", komma, mellemrum, "fik", "→ fik", osv. Næsten universelt: det FØRSTE tal er bid, det ANDET er stik.
 - Bid er typisk meldingens navn (7-13 for normale, ord for nolo). Hvis du har valgt en melding "9" så er "9" bid'et — fortolk IKKE 9-tallet også som stik.
 - For normal melding skal melder+makker tricks summere til STIK-tallet (det andet tal), ikke til bid'et. Modspil-summen er 13 - stik.

MELDING-MATCHING:
 - Tal-bid (7, 8, 9, …) matches mod meldingen med samme .Bid-værdi i klubbens liste.
 - "sol", "solo", "Sol" (uden "ren") matches mod meldingen "Sol".
 - "ren sol", "ren solo" matches mod meldingen "Ren sol".
 - Hvis du er i tvivl mellem Sol og Ren sol, vælg Sol; vælg kun Ren sol når ordet "ren" eksplicit står skrevet.

UDLEDNING fra sedlen:
 - Identificér for hvert spil: melding, hvem der er melder, hvem der evt. er makker, hvad det skrevne stik-tal henviser til (hold-total for normal melding; individuelle tal for nolo).
 - For nolo med makker viser papiret typisk to stik-tal, ét pr. melde-side-spiller (separator: "/", ",", "+", "og", "=N, =N" osv.). Det er ALDRIG en brøk eller et decimaltal — det er to selvstændige heltal i samme rækkefølge som spillerne er nævnt. Eksempel: "K, M sol 1,3" → Katrine fik 1 stik, Martin fik 3 stik (ikke 1+2 og ikke 1.3).
 - Forfattere bruger ofte initialer, forbogstavskombinationer eller fornavne. Resolv dem til fulde navne fra spillerlisten — første unikke prefix-match er normalt det rigtige (fx "M" eller "Ma" → "Martin"). Hvis to spillere starter med samme bogstav, brug længere kontekst eller spring spillet over.
 - Hvis sedlen kun har én skriftlig stik-værdi for normal melding, er det hold-totalen for melde-siden. Modspillets total er da 13 minus dette.

OUTPUT-SKEMA pr. spil:
 - "melding_name": præcis ét navn fra klubbens meldingsliste (case-sensitive).
 - "players": ét element pr. spiller der deltog. Klubben har som regel et fast antal spillere ved bordet (4); hvis sedlen tydeligt viser sit-outs, så medtag kun de spillende.
   * "name": fuldt navn fra spillerlisten (ikke initial).
   * "role": "melder", "makker" eller "modspil".
   * "tricks": antal stik. Sum over et spil = 13.
     - For normal melding pinner papiret kun hold-totalen; fordel den rimeligt mellem melder/makker (typisk lige eller koncentreret hos melder hvis konteksten antyder det) og resten ligeligt mellem modspillerne, så summerne stemmer.
     - For nolo med eksplicitte individuelle tal: brug dem direkte; fordel resten af 13 mellem modspillerne.
 - "played_at": "YYYY-MM-DD" hvis tydelig dato findes på sedlen, ellers tom streng.
 - "note": korte ekstra noter fra sedlen (fx "ærgerligt", "✓"), ellers tom streng.

Returnér KUN spil hvor du har høj tillid til både melding og rollefordeling. Spring overskrifter, blanke linjer og ulæselige rækker over. Antag ikke spil der ikke står på sedlen.

`)
	if len(meldings) > 0 {
		b.WriteString("Klubbens meldinger (vælg ÉT navn herfra til melding_name):\n")
		for _, m := range meldings {
			fmt.Fprintf(&b, " - %s (type=%s, bid=%d, points=%d)\n", m.Name, m.Type, m.Bid, m.Points)
		}
		b.WriteString("\n")
	}
	if len(players) > 0 {
		b.WriteString("Klubbens spillere (vælg ÉT navn herfra til hver player.name):\n")
		for _, p := range players {
			b.WriteString(" - ")
			b.WriteString(p.Name)
			b.WriteString("\n")
		}
	}
	return b.String()
}

func draftSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"games": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"played_at":    map[string]any{"type": "string", "description": "YYYY-MM-DD eller tom"},
						"melding_name": map[string]any{"type": "string"},
						"note":         map[string]any{"type": "string"},
						"players": map[string]any{
							"type":     "array",
							"minItems": 4,
							"maxItems": 4,
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
				},
			},
		},
		"required":             []string{"games"},
		"additionalProperties": false,
	}
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
