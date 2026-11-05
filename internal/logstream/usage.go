package logstream

// UsageAccumulator is a Subscriber that accumulates token usage and turn counts
// from a stream of assistant log entries. It implements logstream.Subscriber.
//
// All fields are exported so callers can read them directly after each Tick.
// OnReset zeroes all fields, which happens whenever SetPath is called on the
// parent Stream (e.g. after /clear or a new session file is discovered).
type UsageAccumulator struct {
	// Cumulative totals for the current session.
	InputTokens       int // input + cache_read + cache_create (total context sent)
	OutputTokens      int
	CacheReadTokens   int
	CacheCreateTokens int
	TurnCount         int
	ModelName         string

	// Per-turn values from the most recent assistant entry with usage.
	LastTurnInput       int
	LastTurnOutput      int
	LastTurnCacheRead   int
	LastTurnCacheCreate int
}

// OnEntry accumulates token usage from assistant entries that carry a Usage block.
// Non-assistant entries and assistant entries without usage are ignored.
func (u *UsageAccumulator) OnEntry(entry Entry) {
	if entry.Type != "assistant" || entry.Usage == nil {
		return
	}
	u.TurnCount++
	u.CacheReadTokens += entry.Usage.CacheReadTokens
	u.CacheCreateTokens += entry.Usage.CacheCreateTokens
	u.InputTokens += entry.Usage.InputTokens + entry.Usage.CacheReadTokens + entry.Usage.CacheCreateTokens
	u.OutputTokens += entry.Usage.OutputTokens

	u.LastTurnInput = entry.Usage.InputTokens + entry.Usage.CacheReadTokens + entry.Usage.CacheCreateTokens
	u.LastTurnOutput = entry.Usage.OutputTokens
	u.LastTurnCacheRead = entry.Usage.CacheReadTokens
	u.LastTurnCacheCreate = entry.Usage.CacheCreateTokens

	if entry.Model != "" && entry.Model != "<synthetic>" {
		u.ModelName = entry.Model
	}
}

// OnReset zeroes all accumulated state. Called by the parent Stream when a new
// log file is opened (e.g. /clear) so counters start fresh.
func (u *UsageAccumulator) OnReset() {
	*u = UsageAccumulator{}
}
