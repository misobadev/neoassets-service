package services

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Translator calls the NeoAssets translate Cloudflare Worker (Workers AI
// m2m100-1.2b) to turn an English description into all supported languages.
// A nil Translator means translations are skipped (worker not configured).
type Translator struct {
	url   string
	token string
	http  *http.Client
}

// NewTranslator builds a Translator. When url or token is empty it returns nil
// so callers can skip translation gracefully.
func NewTranslator(url, token string) *Translator {
	if url == "" || token == "" {
		return nil
	}
	return &Translator{
		url:   url,
		token: token,
		http:  &http.Client{Timeout: 60 * time.Second},
	}
}

// Translate sends the English text to the worker and returns the translations
// keyed by language code (e.g. "es", "de", ...). The English text itself is not
// included in the response.
func (t *Translator) Translate(ctx context.Context, text string) (map[string]string, error) {
	if t == nil {
		return nil, fmt.Errorf("translator not configured")
	}
	body, err := json.Marshal(map[string]any{"text": text})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.token)

	resp, err := t.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("translate worker request: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("translate worker returned %d: %s", resp.StatusCode, string(data))
	}
	var out struct {
		Translations map[string]string `json:"translations"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("translate worker bad response: %w", err)
	}
	return out.Translations, nil
}