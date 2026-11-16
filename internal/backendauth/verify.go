package backendauth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/config"
)

// ErrNoCredits means the key was accepted but the account cannot use paid
// models: the key is valid.
var ErrNoCredits = errors.New("key is valid but the account has no credits for paid models")

// noCreditsCode is the error.code Nous returns for that case.
const noCreditsCode = "insufficient_credits_for_paid_model"

// HTTPClient performs the verification request; tests replace it.
var HTTPClient = &http.Client{Timeout: 30 * time.Second}

var errorTypeRe = regexp.MustCompile(`^[a-z_]{1,64}$`)

// Verify makes one paid, one-token POST /v1/messages with key against b's
// configured model. Failures are mapped to short messages; the response body
// is never echoed beyond the Anthropic-style error.type.
func Verify(ctx context.Context, b config.BackendConfig, key string) error {
	body, err := json.Marshal(map[string]any{
		"model":      b.Model,
		"max_tokens": 1,
		"messages":   []map[string]string{{"role": "user", "content": "hi"}},
	})
	if err != nil {
		return err
	}
	url := strings.TrimRight(b.BaseURL, "/") + "/v1/messages"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building verify request: %w", err)
	}
	SetUserAgent(req)
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("verify request to %s failed: %w", b.BaseURL, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	var e struct {
		Error struct {
			Type string `json:"type"`
			Code any    `json:"code"`
		} `json:"error"`
	}
	isJSON := json.Unmarshal(raw, &e) == nil
	if isJSON && e.Error.Code == any(noCreditsCode) &&
		(resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound) {
		return fmt.Errorf("%w; add credits or use a :free model", ErrNoCredits)
	}
	switch resp.StatusCode {
	case http.StatusUnauthorized:
		return fmt.Errorf("key invalid or out of credits (HTTP 401)")
	case http.StatusNotFound:
		return fmt.Errorf("model not found: %s (HTTP 404)", b.Model)
	case http.StatusForbidden:
		if !isJSON {
			return fmt.Errorf("request blocked by the provider edge (WAF), HTTP 403; this does not mean the key is invalid")
		}
	}
	typ := ""
	if isJSON && errorTypeRe.MatchString(e.Error.Type) {
		typ = ": " + e.Error.Type
	}
	return fmt.Errorf("unexpected HTTP %d from backend%s", resp.StatusCode, typ)
}
