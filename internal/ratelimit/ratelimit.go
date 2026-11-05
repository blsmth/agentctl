package ratelimit

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/blsmth/agentctl/internal/logging"
)

// RateLimitInfo holds rate-limit data derived from the statusLine payload
// Claude Code pushes to agentctl (see internal/statusline).
type RateLimitInfo struct {
	Status        string `json:"status"`
	ResetsAt      int64  `json:"resetsAt"`
	RateLimitType string `json:"rateLimitType"`

	// Source records which path produced this RateLimitInfo. The only
	// producer is the pushed statusLine payload (see pushedToInfo), so it is
	// "statusline" whenever the info is non-nil. The monitor's persistState
	// uses Source to decide whether to trust FiveHourUsedPct directly or fall
	// back to its own windowTokens/tokenCap computation.
	Source string `json:"source,omitempty"`

	// FiveHourUsedPct, SevenDayUsedPct, and SevenDayResetsAt come straight
	// from the pushed payload; SevenDay* stay nil/zero when it carries no
	// seven_day window.
	FiveHourUsedPct  *float64 `json:"fiveHourUsedPct,omitempty"`
	SevenDayUsedPct  *float64 `json:"sevenDayUsedPct,omitempty"`
	SevenDayResetsAt int64    `json:"sevenDayResetsAt,omitempty"`
}

// WindowTokenScanner maintains a periodically-refreshed cache of the total
// input+output tokens consumed across ALL Claude Code sessions within the
// current 5-hour rate limit window. It scans ~/.claude/projects/**/*.jsonl
// in a background goroutine every 30 seconds so disk I/O never blocks the
// monitor's 500 ms tick.
//
// The number this scanner produces is only a fallback input to
// RateLimitState.UsagePct: when a fresh pushed five_hour percentage exists
// (see internal/statusline, RateLimitProber.Tick), the monitor uses that
// directly and this scan is pure unused I/O. Tick short-circuits in that
// case -- see pushedFresh.
type WindowTokenScanner struct {
	mu           sync.Mutex
	windowTokens int64
	lastScan     time.Time
	scanning     bool
	resultCh     chan int64

	// pushedFresh reports whether a fresh pushed five_hour percentage
	// currently exists, i.e. whether the number this scanner produces would
	// go unused. Injected (rather than calling LoadPushed/Fresh inline) so
	// this package stays unit-testable without touching
	// ~/.local/state/agentctl/ratelimits.json, and so the monitor never needs its
	// own copy of the freshness gate. Production wires this to LoadPushed +
	// PushedRateLimits.Fresh in NewWindowTokenScanner.
	pushedFresh func() bool
	// now is injected for tests; production uses time.Now.
	now func() time.Time
}

// NewWindowTokenScanner creates a new WindowTokenScanner.
func NewWindowTokenScanner() *WindowTokenScanner {
	return &WindowTokenScanner{
		resultCh: make(chan int64, 1),
		pushedFresh: func() bool {
			pushed, err := LoadPushed()
			if err != nil {
				return false
			}
			return pushed.Fresh(time.Now())
		},
		now: time.Now,
	}
}

// Tick returns the latest cached window token count.
//
// When a fresh pushed five_hour percentage exists (pushedFresh), Tick
// returns the cached value as-is and starts no scan at all -- the value it
// would produce is unused in that case. Otherwise it kicks
// off a background scan every 30 s when none is already in flight, as usual.
func (s *WindowTokenScanner) Tick(windowStart time.Time) int64 {
	// Collect any completed scan result.
	select {
	case result := <-s.resultCh:
		s.mu.Lock()
		s.windowTokens = result
		s.scanning = false
		s.mu.Unlock()
		logging.Log("ratelimit: window token scan complete: %d tokens", result)
	default:
	}

	if s.pushedFresh != nil && s.pushedFresh() {
		s.mu.Lock()
		tokens := s.windowTokens
		s.mu.Unlock()
		return tokens
	}

	s.mu.Lock()
	shouldScan := !s.scanning && s.now().Sub(s.lastScan) > 30*time.Second
	if shouldScan {
		s.scanning = true
		s.lastScan = s.now()
	}
	tokens := s.windowTokens
	s.mu.Unlock()

	if shouldScan {
		go func() {
			s.resultCh <- scanWindowTokens(windowStart)
		}()
	}

	return tokens
}

// scanWindowTokens walks ~/.claude/projects/*/*.jsonl (and subagent paths),
// skipping files whose ModTime predates windowStart, then sums
// input_tokens + output_tokens (raw, excluding cache) for every entry whose
// timestamp falls within [windowStart, now].
func scanWindowTokens(windowStart time.Time) int64 {
	home := os.Getenv("HOME")
	if home == "" {
		return 0
	}
	projectsDir := filepath.Join(home, ".claude", "projects")

	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return 0
	}

	var total int64
	for _, projectEntry := range entries {
		if !projectEntry.IsDir() {
			continue
		}
		projectPath := filepath.Join(projectsDir, projectEntry.Name())
		total += scanJSONLDir(projectPath, windowStart)

		// Also scan subagent directories: {projectDir}/{sessionId}/subagents/*.jsonl
		subDirs, err := os.ReadDir(projectPath)
		if err != nil {
			continue
		}
		for _, sub := range subDirs {
			if !sub.IsDir() {
				continue
			}
			subagentDir := filepath.Join(projectPath, sub.Name(), "subagents")
			total += scanJSONLDir(subagentDir, windowStart)
		}
	}
	return total
}

// scanJSONLDir scans all *.jsonl files in dir, summing in-window tokens.
func scanJSONLDir(dir string, windowStart time.Time) int64 {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	var total int64
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".jsonl" {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		// Cheap filter: skip files not modified within the window.
		if info.ModTime().Before(windowStart) {
			continue
		}
		total += scanJSONLFile(filepath.Join(dir, e.Name()), windowStart)
	}
	return total
}

// jsonlUsageEntry is the minimal struct needed for token scanning.
type jsonlUsageEntry struct {
	Timestamp string `json:"timestamp"`
	Message   struct {
		Usage struct {
			InputTokens  int `json:"input_tokens"`
			OutputTokens int `json:"output_tokens"`
			// Deliberately omitting cache fields — they are excluded per spec.
		} `json:"usage"`
	} `json:"message"`
}

// scanJSONLFile reads a single JSONL file and sums input+output tokens for
// entries whose timestamp falls within [windowStart, now].
func scanJSONLFile(path string, windowStart time.Time) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	var total int64
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 256*1024)
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		// Fast pre-check: skip lines that don't look like assistant messages
		// with usage data (avoids full JSON parse for most lines).
		if !bytes.Contains(line, []byte(`"input_tokens"`)) {
			continue
		}

		var entry jsonlUsageEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}

		// Parse timestamp.
		var ts time.Time
		if entry.Timestamp != "" {
			if t, err := time.Parse(time.RFC3339Nano, entry.Timestamp); err == nil {
				ts = t
			} else if t, err := time.Parse("2006-01-02T15:04:05Z", entry.Timestamp); err == nil {
				ts = t
			}
		}
		if ts.IsZero() || ts.Before(windowStart) {
			continue
		}

		total += int64(entry.Message.Usage.InputTokens) + int64(entry.Message.Usage.OutputTokens)
	}
	return total
}

// RateLimitProber reads the latest pushed statusLine rate-limit data (see
// internal/statusline). The name is historical: it used to spawn a `claude -p`
// probe, which has been removed.
type RateLimitProber struct {
	// usingPushed tracks which data source was live on the previous Tick,
	// purely so transitions log once instead of every 500ms tick.
	usingPushed bool

	// loadPushed and now are injectable so tests can exercise the gating
	// logic without touching ~/.local/state/agentctl/ratelimits.json or a real
	// clock. Production code uses LoadPushed and time.Now.
	loadPushed func() (*PushedRateLimits, error)
	now        func() time.Time
}

// NewRateLimitProber creates a new RateLimitProber.
func NewRateLimitProber() *RateLimitProber {
	return &RateLimitProber{
		loadPushed: LoadPushed,
		now:        time.Now,
	}
}

// Tick returns rate limit info derived from the pushed payload, or nil.
//
// Tick loads the shared pushed-rate-limits file (see internal/statusline).
// If it is fresh (written within pushedFreshness) and carries a
// five_hour window, Tick returns info derived from it. If the pushed data is
// missing, stale, or lacks a five_hour window (accounts that aren't
// Pro/Max/gateway, or no statusLine installed), Tick returns nil and the
// rate-limit display is simply absent. That is acceptable degradation: there
// is no probe fallback, and stale data is deliberately not replayed.
func (p *RateLimitProber) Tick() *RateLimitInfo {
	now := p.now()

	pushed, err := p.loadPushed()
	if err != nil {
		logging.Log("ratelimit: loading pushed rate limits: %v", err)
		pushed = nil
	}

	if pushed.Fresh(now) {
		if !p.usingPushed {
			logging.Log("ratelimit: using pushed statusline data")
			p.usingPushed = true
		}
		return pushedToInfo(pushed)
	}
	if p.usingPushed {
		logging.Log("ratelimit: pushed statusline data stale or missing")
		p.usingPushed = false
	}
	return nil
}

// pushedToInfo adapts fresh pushed rate-limit data to the RateLimitInfo shape
// existing callers (the monitor's Tick loop) already consume. It only ever
// reports Status "allowed": the statusLine payload carries a used_percentage,
// not a blocked/allowed verdict, and inventing one here would be a guess the
// probe path never had to make.
//
// It also carries FiveHourUsedPct (so the monitor can use the
// pushed percentage directly instead of the windowTokens/tokenCap estimate --
// see monitor.go persistState) and, when present, SevenDayUsedPct/
// SevenDayResetsAt -- data the probe path never had at all.
func pushedToInfo(pr *PushedRateLimits) *RateLimitInfo {
	if pr == nil || pr.FiveHour == nil {
		return nil
	}
	fiveHourPct := pr.FiveHour.UsedPercentage
	info := &RateLimitInfo{
		Status:          "allowed",
		ResetsAt:        pr.FiveHour.ResetsAt,
		RateLimitType:   "five_hour",
		Source:          "statusline",
		FiveHourUsedPct: &fiveHourPct,
	}
	if pr.SevenDay != nil {
		sevenDayPct := pr.SevenDay.UsedPercentage
		info.SevenDayUsedPct = &sevenDayPct
		info.SevenDayResetsAt = pr.SevenDay.ResetsAt
	}
	return info
}
