// Package statusline ingests the JSON payload Claude Code pushes on stdin to
// a configured `statusLine` command on every assistant message (debounced
// ~300ms). Unlike hooks, statusLine scripts consume no API tokens -- this
// package exists to let agentctl treat that payload as a data source instead
// of deriving the same numbers by polling.
package statusline

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
)

// Payload is the decoded shape of a Claude Code statusLine payload.
//
// Every field is a pointer (or a pointer to a nested struct) ON PURPOSE.
// Absence is semantically different from zero in this payload: expired
// rate_limits windows are dropped entirely rather than zeroed, `pr` is
// removed the moment a PR merges, and prompt_cache.hit_ratio is null rather
// than 0. A value-typed struct cannot distinguish "0% used" from "this
// window no longer exists" -- do not flatten these fields to their zero
// values anywhere downstream of Decode.
//
// Unknown keys (top-level or nested) are ignored, which is encoding/json's
// default behavior for a struct target -- DisallowUnknownFields is
// deliberately not used. The payload gains fields on every Claude Code
// release; a strict decoder would break on each one.
type Payload struct {
	SessionID      *string `json:"session_id"`
	SessionName    *string `json:"session_name"`
	TranscriptPath *string `json:"transcript_path"`
	Cwd            *string `json:"cwd"`
	Version        *string `json:"version"`

	Model         *Model         `json:"model"`
	Workspace     *Workspace     `json:"workspace"`
	Cost          *Cost          `json:"cost"`
	ContextWindow *ContextWindow `json:"context_window"`
	PromptCache   *PromptCache   `json:"prompt_cache"`
	RateLimits    *RateLimits    `json:"rate_limits"`
	Agent         *Agent         `json:"agent"`
	PR            *PR            `json:"pr"`
	Effort        *Effort        `json:"effort"`
	FastMode      *bool          `json:"fast_mode"`
	Thinking      *Thinking      `json:"thinking"`
	Vim           *Vim           `json:"vim"`

	// NOTE: there is deliberately no `Branch`/`Worktree` field here. A
	// top-level `worktree.{branch,path,original_branch}` block exists ONLY
	// inside a Claude Code *worktree session* (see /docs/en/worktrees), not
	// for a directory created by `git worktree add` (which is how
	// agentctl's .worktrees/<slug>/ dirs are made). agentctl's worktrees get
	// only workspace.git_worktree, a bare name string with no branch
	// semantics. windowstate.Branch / WorktreeDir stay owned by
	// dispatch.Launch.
}

// Model is the "model" object of a statusLine payload.
type Model struct {
	ID          *string `json:"id"`
	DisplayName *string `json:"display_name"`
}

// Workspace is the "workspace" object of a statusLine payload.
type Workspace struct {
	CurrentDir  *string  `json:"current_dir"`
	ProjectDir  *string  `json:"project_dir"`
	GitWorktree *string  `json:"git_worktree"`
	AddedDirs   []string `json:"added_dirs"`
}

// Cost is the "cost" object of a statusLine payload.
type Cost struct {
	TotalCostUSD       *float64 `json:"total_cost_usd"`
	TotalDurationMS    *float64 `json:"total_duration_ms"`
	TotalAPIDurationMS *float64 `json:"total_api_duration_ms"`
	TotalLinesAdded    *float64 `json:"total_lines_added"`
	TotalLinesRemoved  *float64 `json:"total_lines_removed"`
}

// CurrentUsage is "context_window.current_usage". It is null before the
// first API call in a session and again after /compact until the next call.
type CurrentUsage struct {
	InputTokens              *float64 `json:"input_tokens"`
	OutputTokens             *float64 `json:"output_tokens"`
	CacheCreationInputTokens *float64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *float64 `json:"cache_read_input_tokens"`
}

// ContextWindow is the "context_window" object of a statusLine payload.
type ContextWindow struct {
	TotalInputTokens     *float64      `json:"total_input_tokens"`
	TotalOutputTokens    *float64      `json:"total_output_tokens"`
	ContextWindowSize    *float64      `json:"context_window_size"`
	UsedPercentagePushed *float64      `json:"used_percentage"`
	RemainingPercentage  *float64      `json:"remaining_percentage"`
	CurrentUsage         *CurrentUsage `json:"current_usage"`
}

// UsedPercentage implements the fallback cascade
// (borrowed from ccstatusline's getContextWindowMetrics): prefer the pushed
// used_percentage; else derive from current_usage against
// context_window_size; else derive from total_input_tokens. Returns
// ok=false when none of the three yields a usable number (e.g. very early
// in a session, before the first API call).
//
// used_percentage, when present, is INPUT-ONLY: (input + cache_creation +
// cache_read) / context_window_size, excluding output_tokens. It is stored
// and returned exactly as pushed -- never recomputed -- so this number never
// disagrees with what the user sees in their own Claude Code UI.
func (cw *ContextWindow) UsedPercentage() (float64, bool) {
	if cw == nil {
		return 0, false
	}
	if v, ok := coercePercent(cw.UsedPercentagePushed); ok {
		return v, true
	}
	size, ok := coerceNonNegative(cw.ContextWindowSize)
	if !ok || size <= 0 {
		return 0, false
	}
	if cw.CurrentUsage != nil {
		input, hasInput := coerceNonNegative(cw.CurrentUsage.InputTokens)
		cacheCreate, _ := coerceNonNegative(cw.CurrentUsage.CacheCreationInputTokens)
		cacheRead, _ := coerceNonNegative(cw.CurrentUsage.CacheReadInputTokens)
		if hasInput {
			return clampPercent(100 * (input + cacheCreate + cacheRead) / size), true
		}
	}
	if total, ok := coerceNonNegative(cw.TotalInputTokens); ok {
		return clampPercent(100 * total / size), true
	}
	return 0, false
}

// PromptCache is the "prompt_cache" object of a statusLine payload.
// HitRatio is a fraction in [0, 1], not a percentage.
//
// Claude Code has changed the JSON type of these fields between releases
// (e.g. ttl became a string), so UnmarshalJSON decodes every field
// leniently via flexFloat: a number, a numeric string, or a duration string
// all work, and anything else becomes nil rather than an error. The exported
// fields stay *float64 so every read still goes through
// coerceFinite/coerceNonNegative, the single coercion point.
type PromptCache struct {
	Warm      *bool
	TTL       *float64
	ExpiresAt *float64
	HitRatio  *float64
	Requests  *float64
	Misses    *float64
}

// UnmarshalJSON implements json.Unmarshaler. It never fails on a wrongly
// typed field; only a syntactically invalid document errors.
func (pc *PromptCache) UnmarshalJSON(data []byte) error {
	var aux struct {
		Warm      json.RawMessage `json:"warm"`
		TTL       flexFloat       `json:"ttl"`
		ExpiresAt flexFloat       `json:"expires_at"`
		HitRatio  flexFloat       `json:"hit_ratio"`
		Requests  flexFloat       `json:"requests"`
		Misses    flexFloat       `json:"misses"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	*pc = PromptCache{
		TTL:       aux.TTL.v,
		ExpiresAt: aux.ExpiresAt.v,
		HitRatio:  aux.HitRatio.v,
		Requests:  aux.Requests.v,
		Misses:    aux.Misses.v,
	}
	var warm bool
	if len(aux.Warm) > 0 && json.Unmarshal(aux.Warm, &warm) == nil {
		pc.Warm = &warm
	}
	return nil
}

// flexFloat decodes a JSON number, a numeric string ("300", "300.5"), or a
// Go duration string ("5m", "1h" -> seconds) into an optional float64.
// null, unparseable strings, and any other JSON type yield nil, never an
// error.
type flexFloat struct{ v *float64 }

// UnmarshalJSON implements json.Unmarshaler.
func (f *flexFloat) UnmarshalJSON(data []byte) error {
	f.v = nil
	data = bytes.TrimSpace(data)
	if bytes.Equal(data, []byte("null")) {
		return nil
	}
	var n float64
	if json.Unmarshal(data, &n) == nil {
		f.v = &n
		return nil
	}
	var str string
	if json.Unmarshal(data, &str) != nil {
		return nil
	}
	str = strings.TrimSpace(str)
	if v, err := strconv.ParseFloat(str, 64); err == nil {
		f.v = &v
	} else if d, err := time.ParseDuration(str); err == nil {
		v := d.Seconds()
		f.v = &v
	}
	return nil
}

// RateLimitWindow is one entry of the "rate_limits" object
// (five_hour / seven_day / spend_limit). ResetsAt is Unix epoch SECONDS.
type RateLimitWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *float64 `json:"resets_at"`
}

// RateLimits is the "rate_limits" object of a statusLine payload. The whole
// object is absent unless the account is Claude.ai Pro/Max or sits behind a
// gateway -- treat a nil RateLimits as "no data", never as zero. Each
// individual window is also independently droppable once its window
// expires, so a present RateLimits with a nil FiveHour is valid and must be
// handled the same way as a nil RateLimits.
type RateLimits struct {
	FiveHour   *RateLimitWindow `json:"five_hour"`
	SevenDay   *RateLimitWindow `json:"seven_day"`
	SpendLimit *RateLimitWindow `json:"spend_limit"`
}

// Agent is the "agent" object of a statusLine payload.
type Agent struct {
	Name *string `json:"name"`
}

// PR is the "pr" object of a statusLine payload. It is removed entirely
// from the payload when the PR merges.
type PR struct {
	Number      *float64 `json:"number"`
	URL         *string  `json:"url"`
	ReviewState *string  `json:"review_state"`
	Kind        *string  `json:"kind"`
}

// Effort is the "effort" object of a statusLine payload.
type Effort struct {
	Level *string `json:"level"`
}

// Thinking is the "thinking" object of a statusLine payload.
type Thinking struct {
	Enabled *bool `json:"enabled"`
}

// Vim is the "vim" object of a statusLine payload.
type Vim struct {
	Mode *string `json:"mode"`
}

// Decode parses a statusLine payload from raw JSON bytes.
//
// A type mismatch in one section must not discard the rest of the payload:
// Claude Code's schema drifts, and losing rate_limits/model because
// prompt_cache changed shape blanks the whole status line. On a
// *json.UnmarshalTypeError, Decode retries section by section, dropping (and
// debug-logging) only the sections that fail. Invalid JSON syntax still
// returns an error.
func Decode(data []byte) (*Payload, error) {
	var p Payload
	err := json.Unmarshal(data, &p)
	if err == nil {
		return &p, nil
	}
	var typeErr *json.UnmarshalTypeError
	if !errors.As(err, &typeErr) {
		return nil, err
	}

	var sections map[string]json.RawMessage
	if err := json.Unmarshal(data, &sections); err != nil {
		return nil, err
	}
	var salvaged Payload
	v := reflect.ValueOf(&salvaged).Elem()
	for i := 0; i < v.NumField(); i++ {
		name, _, _ := strings.Cut(v.Type().Field(i).Tag.Get("json"), ",")
		raw, ok := sections[name]
		if !ok || name == "" {
			continue
		}
		dst := reflect.New(v.Field(i).Type())
		if err := json.Unmarshal(raw, dst.Interface()); err != nil {
			logging.Debug("statusline: dropping malformed payload section %q: %v", name, err)
			continue
		}
		v.Field(i).Set(dst.Elem())
	}
	return &salvaged, nil
}

// coerceFinite dereferences p, rejecting nil, NaN, and +/-Inf. This is the
// single coercion point every numeric read in this package must go through
// -- a malformed or adversarial payload value can never produce a NaN/Inf that propagates into windowstate or the shared rate-limits file.
func coerceFinite(p *float64) (float64, bool) {
	if p == nil {
		return 0, false
	}
	v := *p
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// coerceNonNegative reads a numeric pointer field, clamping negatives to 0.
// Use this for fields that may legitimately exceed 100 (e.g.
// spend_limit.used_percentage must NOT be run through coercePercent).
func coerceNonNegative(p *float64) (float64, bool) {
	v, ok := coerceFinite(p)
	if !ok {
		return 0, false
	}
	if v < 0 {
		v = 0
	}
	return v, true
}

// coercePercent reads a numeric pointer field, clamping to [0, 100].
// Do NOT use this for spend_limit.used_percentage, which can exceed 100.
func coercePercent(p *float64) (float64, bool) {
	v, ok := coerceNonNegative(p)
	if !ok {
		return 0, false
	}
	return clampPercent(v), true
}

// clampPercent clamps an already-finite value to [0, 100].
func clampPercent(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
