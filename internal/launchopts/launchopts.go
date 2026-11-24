// Package launchopts holds the small vocabulary shared by the launch popup
// (internal/tui) and the dispatch pipeline (internal/dispatch). It is a leaf
// package because tui cannot import dispatch.
package launchopts

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Inherit is the model / effort value that clears the agent's configured value
// instead of overriding it.
const Inherit = "inherit"

// BackendNone is the backend value that forces Anthropic even when the agent
// config selects a backend.
const BackendNone = "none"

// Efforts are the --effort levels Claude Code accepts.
var Efforts = []string{"low", "medium", "high", "xhigh", "max", "auto"}

// ValidateEffort rejects an effort that is neither Inherit nor a known level.
func ValidateEffort(e string) error {
	if e == Inherit || slices.Contains(Efforts, e) {
		return nil
	}
	return fmt.Errorf("invalid effort %q (want %s or %s)", e, strings.Join(Efforts, ", "), Inherit)
}

// ErrNoModelList means a backend answered but not with an Ollama tag list
// (non-200, unreadable, or no "models" field): it may be a non-Ollama
// Anthropic-compatible server, so its model cannot be checked.
var ErrNoModelList = errors.New("backend did not return an Ollama model list")

// NormalizeModel appends Ollama's implicit ":latest" tag when the final path
// segment has no tag. Colons earlier in the name (registry host:port) are not
// tag separators. Comparison-only; never used for the name passed to --model.
func NormalizeModel(name string) string {
	last := name[strings.LastIndex(name, "/")+1:]
	if strings.Contains(last, ":") {
		return name
	}
	return name + ":latest"
}

// SameModel reports whether two Ollama model names refer to the same model.
func SameModel(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	return NormalizeModel(a) == NormalizeModel(b)
}

// modelRE is the one charset rule for a model name: a leading alphanumeric
// (or "~", for OpenRouter-style "~vendor/name" ids), then name characters
// including "/" (vendor/name slugs), ":" (tags) and "[...]" (e.g. "[1m]").
var modelRE = regexp.MustCompile(`^~?[A-Za-z0-9][A-Za-z0-9._:/\[\]-]{0,99}$`)

// ValidModel reports whether s is a well-formed model name. It checks the
// charset and length only, not whether the model exists.
func ValidModel(s string) bool { return modelRE.MatchString(s) }
