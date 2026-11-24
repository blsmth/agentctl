// Package race turns an agent transcript into a race between the agent and a
// simulated human doing the same work: every tool call and text block is a
// step, the agent's time per step comes from the transcript's timestamps, and
// the human's comes from how many characters a person would have to type and
// read at a given speed.
//
// The model is pure: nothing here reads the wall clock. Build produces the
// timeline; AgentAt and HumanAt place each racer on it at a given point of
// transcript time; Playback maps wall time onto transcript time for a replay.
package race

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/logstream"
	"github.com/blsmth/agentctl/internal/toolinput"
)

// Step kinds.
const (
	KindTool = "tool"
	KindText = "text"
)

// Display caps. Counts always cover the full text; only what is kept for
// drawing is cut.
const (
	labelMax      = 160
	resultHeadMax = 2000
	resultTailMax = 200
)

// Params are the assumptions behind the human's time.
type Params struct {
	WPM     float64       // typing speed, words (5 characters) per minute
	ReadWPM float64       // reading speed, words per minute
	Think   time.Duration // pause added to every step
	MaxGap  time.Duration // longest gap between entries charged to the agent

	// RealLife converts the human's nonstop effort into calendar time on
	// Schedule (see CalendarFor).
	RealLife bool
	Schedule Schedule
}

// DefaultParams are the defaults of `agentctl race`.
func DefaultParams() Params {
	return Params{WPM: 60, ReadWPM: 250, Think: 0, MaxGap: 60 * time.Second, Schedule: DefaultSchedule()}
}

// Step is one unit of work: a tool call with its result, or a text block.
type Step struct {
	Kind  string // KindTool or KindText
	Tool  string // tool name; "" for text
	Label string // one-line summary for display

	Typed      string // what a person would have to type
	TypedRunes int
	ReadRunes  int    // runes of tool result a person would have to read
	ResultHead string // start of the result, for display
	ResultTail string // last non-empty result line, for display
	IsError    bool

	// Call context, for analyzers that look at what was run (sessionaudit).
	ID     string         // tool_use id; "" for text
	Input  map[string]any // tool input; nil for text
	At     time.Time      // timestamp of the entry that made the call
	Cwd    string         // working directory recorded on that entry
	RawGap time.Duration  // longest uncapped gap charged to this step
	// ModelGap is the longest uncapped part of RawGap that was the model
	// thinking: a gap ending in an assistant entry that follows a user entry
	// (a tool result or a prompt). The wait between a tool call and its
	// result is the tool running and is not in it.
	ModelGap time.Duration

	AgentStart time.Duration // offset on the agent timeline
	AgentDur   time.Duration
	HumanStart time.Duration // offset on the human timeline
	TypeDur    time.Duration
	ReadDur    time.Duration
	ThinkDur   time.Duration
}

// HumanDur is the step's total human time.
func (s Step) HumanDur() time.Duration { return s.TypeDur + s.ReadDur + s.ThinkDur }

// Race is the full comparison for one transcript.
type Race struct {
	Params     Params
	Steps      []Step
	AgentTotal time.Duration
	HumanTotal time.Duration
	TypedRunes int
	ReadRunes  int
	ToolCalls  map[string]int
	TextBlocks int
}

// Speedup is HumanTotal / AgentTotal, or 0 when the agent took no time.
func (r *Race) Speedup() float64 {
	if r.AgentTotal <= 0 {
		return 0
	}
	return float64(r.HumanTotal) / float64(r.AgentTotal)
}

// Calendar lays the human's total effort onto the params' schedule.
func (r *Race) Calendar() Calendar { return CalendarFor(r.HumanTotal, r.Params.Schedule) }

// CalendarSpeedup is calendar time over the agent's active time, or 0 when the
// agent took no time.
func (r *Race) CalendarSpeedup() float64 {
	if r.AgentTotal <= 0 {
		return 0
	}
	return float64(r.Calendar().Elapsed) / float64(r.AgentTotal)
}

// Build extracts the steps from entries and prices them with p.
func Build(entries []logstream.Entry, p Params) *Race {
	r := &Race{Params: p, ToolCalls: map[string]int{}}
	r.Steps = extract(entries, p.MaxGap)
	var agentAt, humanAt time.Duration
	for i := range r.Steps {
		s := &r.Steps[i]
		s.AgentStart = agentAt
		agentAt += s.AgentDur
		s.TypeDur = charsDuration(s.TypedRunes, p.WPM)
		s.ReadDur = charsDuration(s.ReadRunes, p.ReadWPM)
		s.ThinkDur = p.Think
		s.HumanStart = humanAt
		humanAt += s.HumanDur()
		r.TypedRunes += s.TypedRunes
		r.ReadRunes += s.ReadRunes
		if s.Kind == KindTool {
			r.ToolCalls[s.Tool]++
		} else {
			r.TextBlocks++
		}
	}
	r.AgentTotal = agentAt
	r.HumanTotal = humanAt
	return r
}

// charsDuration is how long n characters take at wpm words (of 5
// characters) per minute.
func charsDuration(n int, wpm float64) time.Duration {
	if n <= 0 || wpm <= 0 {
		return 0
	}
	return time.Duration(float64(n) / (wpm * 5) * float64(time.Minute))
}

// extract walks the transcript in order and builds the steps with their
// agent durations. Every gap between two timestamped entries is charged,
// capped at maxGap, to the step the later entry belongs to (a tool result is
// charged to its call), except a gap that ends at a human prompt: that is the
// agent waiting on a person. Gaps ending at an entry that starts no step
// (thinking only) carry over to the next step. Sidechain (sub-agent) entries
// are skipped.
func extract(entries []logstream.Entry, maxGap time.Duration) []Step {
	var steps []Step
	byID := map[string]int{}
	var last time.Time
	var pending, pendingRaw, pendingModel time.Duration
	lastType := ""
	cwd := ""

	for _, e := range entries {
		if e.Raw == nil || e.Type != "assistant" && e.Type != "user" {
			continue
		}
		if side, _ := e.Raw["isSidechain"].(bool); side {
			continue
		}
		if c, _ := e.Raw["cwd"].(string); c != "" {
			cwd = strings.TrimRight(c, "/") + "/"
		}
		blocks, prompt := contentBlocks(e.Raw)

		first := -1
		switch e.Type {
		case "assistant":
			for _, b := range blocks {
				s, id, ok := stepFromBlock(b, cwd)
				if !ok {
					continue
				}
				s.At = e.Timestamp
				s.Cwd = strings.TrimSuffix(cwd, "/")
				if first < 0 {
					first = len(steps)
				}
				if id != "" {
					byID[id] = len(steps)
				}
				steps = append(steps, s)
			}
		case "user":
			for _, b := range blocks {
				if t, _ := b["type"].(string); t != "tool_result" {
					continue
				}
				id, _ := b["tool_use_id"].(string)
				i, ok := byID[id]
				if !ok {
					continue
				}
				applyResult(&steps[i], b, cwd)
				if first < 0 {
					first = i
				}
			}
		}

		if e.Timestamp.IsZero() {
			continue
		}
		gap := time.Duration(0)
		if !last.IsZero() {
			gap = e.Timestamp.Sub(last)
		}
		last = e.Timestamp
		if gap < 0 {
			gap = 0
		}
		raw := gap
		var model time.Duration
		if e.Type == "assistant" && lastType == "user" {
			model = raw
		}
		lastType = e.Type
		if maxGap > 0 && gap > maxGap {
			gap = maxGap
		}
		switch {
		case e.Type == "user" && first < 0 && prompt:
			// Waiting on a person, not working.
		case first >= 0:
			steps[first].AgentDur += gap + pending
			steps[first].RawGap = max(steps[first].RawGap, raw, pendingRaw)
			steps[first].ModelGap = max(steps[first].ModelGap, model, pendingModel)
			pending, pendingRaw, pendingModel = 0, 0, 0
		default:
			pending += gap
			pendingRaw = max(pendingRaw, raw)
			pendingModel = max(pendingModel, model)
		}
	}
	if pending > 0 && len(steps) > 0 {
		steps[len(steps)-1].AgentDur += pending
		steps[len(steps)-1].RawGap = max(steps[len(steps)-1].RawGap, pendingRaw)
		steps[len(steps)-1].ModelGap = max(steps[len(steps)-1].ModelGap, pendingModel)
	}
	return steps
}

// IsPrompt reports whether e is a person's prompt in the main conversation:
// user text that is not a tool result, outside any sub-agent sidechain.
func IsPrompt(e logstream.Entry) bool {
	if e.Raw == nil || e.Type != "user" {
		return false
	}
	if side, _ := e.Raw["isSidechain"].(bool); side {
		return false
	}
	blocks, prompt := contentBlocks(e.Raw)
	for _, b := range blocks {
		if t, _ := b["type"].(string); t == "tool_result" {
			return false
		}
	}
	return prompt
}

// contentBlocks returns the message's content blocks, and whether the entry
// is a person's prompt (user text that is not a tool result).
func contentBlocks(raw map[string]any) (blocks []map[string]any, prompt bool) {
	msg, _ := raw["message"].(map[string]any)
	if msg == nil {
		return nil, false
	}
	switch c := msg["content"].(type) {
	case string:
		return nil, strings.TrimSpace(c) != ""
	case []any:
		for _, v := range c {
			b, ok := v.(map[string]any)
			if !ok {
				continue
			}
			blocks = append(blocks, b)
			if t, _ := b["type"].(string); t == "text" {
				prompt = true
			}
		}
	}
	return blocks, prompt
}

// stepFromBlock builds a step from an assistant content block. ok is false
// for blocks that are not work (thinking, empty text). Paths under cwd are
// shown, and for lookups typed, relative to it, as a person would.
func stepFromBlock(b map[string]any, cwd string) (s Step, id string, ok bool) {
	switch b["type"] {
	case "text":
		text, _ := b["text"].(string)
		if strings.TrimSpace(text) == "" {
			return Step{}, "", false
		}
		return Step{
			Kind:       KindText,
			Label:      firstLine(text, labelMax),
			Typed:      text,
			TypedRunes: utf8.RuneCountInString(text),
		}, "", true
	case "tool_use":
		name, _ := b["name"].(string)
		id, _ = b["id"].(string)
		input, _ := b["input"].(map[string]any)
		typed := TypedText(name, input)
		raw, _ := json.Marshal(input)
		label := toolinput.Summarize(name, raw, 4*labelMax)
		if label == "" {
			label = firstLine(typed, 4*labelMax)
		}
		if cwd != "/" && cwd != "" {
			label = strings.ReplaceAll(label, cwd, "")
			switch name {
			case "Read", "Grep", "Glob":
				typed = strings.ReplaceAll(typed, cwd, "")
			}
		}
		label = headRunes(label, labelMax)
		return Step{
			Kind:       KindTool,
			Tool:       name,
			Label:      label,
			Typed:      typed,
			TypedRunes: utf8.RuneCountInString(typed),
			ID:         id,
			Input:      input,
		}, id, true
	}
	return Step{}, "", false
}

// applyResult records a tool_result block on its step. A Read's tail is
// its line count: the last line of a file says nothing.
func applyResult(s *Step, b map[string]any, cwd string) {
	text := resultText(b["content"])
	s.ReadRunes += utf8.RuneCountInString(text)
	if s.ResultHead == "" {
		s.ResultHead = headRunes(text, resultHeadMax)
	}
	tail := lastLine(text, resultTailMax)
	if s.Tool == "Read" && text != "" {
		tail = fmt.Sprintf("%d lines", strings.Count(strings.TrimRight(text, "\n"), "\n")+1)
	}
	if cwd != "/" && cwd != "" {
		tail = strings.ReplaceAll(tail, cwd, "")
	}
	if tail != "" {
		s.ResultTail = tail
	}
	if e, _ := b["is_error"].(bool); e {
		s.IsError = true
	}
}

// resultText flattens a tool_result's content (a string, or text blocks;
// images carry no text) into one string.
func resultText(v any) string {
	switch c := v.(type) {
	case string:
		return c
	case []any:
		var parts []string
		for _, x := range c {
			b, ok := x.(map[string]any)
			if !ok {
				continue
			}
			if t, _ := b["text"].(string); t != "" {
				parts = append(parts, t)
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

// TypedText is what a person would have to type to make the same tool call:
// the command, the new code, the prompt. Lookups (Read, Grep, Glob) cost only
// their pattern and path. Tools with no rule cost their one-line summary.
func TypedText(tool string, input map[string]any) string {
	str := func(k string) string { s, _ := input[k].(string); return s }
	join := func(parts ...string) string {
		var out []string
		for _, p := range parts {
			if p != "" {
				out = append(out, p)
			}
		}
		return strings.Join(out, " ")
	}
	switch tool {
	case "Bash":
		return str("command")
	case "Edit":
		return str("new_string")
	case "MultiEdit":
		edits, _ := input["edits"].([]any)
		var out []string
		for _, e := range edits {
			if m, ok := e.(map[string]any); ok {
				if s, _ := m["new_string"].(string); s != "" {
					out = append(out, s)
				}
			}
		}
		return strings.Join(out, "\n")
	case "Write":
		return str("content")
	case "NotebookEdit":
		return str("new_source")
	case "Task", "Agent":
		return str("prompt")
	case "Read":
		return str("file_path")
	case "Grep", "Glob":
		return join(str("pattern"), str("path"))
	case "TodoWrite":
		todos, _ := input["todos"].([]any)
		var out []string
		for _, t := range todos {
			if m, ok := t.(map[string]any); ok {
				if s, _ := m["content"].(string); s != "" {
					out = append(out, s)
				}
			}
		}
		return strings.Join(out, "\n")
	}
	raw, _ := json.Marshal(input)
	return toolinput.Summarize(tool, raw, 1<<20)
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return headRunes(strings.TrimSpace(s), max)
}

func lastLine(s string, max int) string {
	lines := strings.Split(strings.TrimRight(s, "\n\r\t "), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if l := strings.TrimSpace(lines[i]); l != "" {
			return headRunes(l, max)
		}
	}
	return ""
}

// headRunes returns the first max runes of s.
func headRunes(s string, max int) string {
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	n := 0
	for i := range s {
		if n == max {
			return s[:i]
		}
		n++
	}
	return s
}

// ToolCount is one tool's call count.
type ToolCount struct {
	Tool  string `json:"tool"`
	Count int    `json:"count"`
}

// SortedTools returns the tool call counts, most used first, then by name.
func (r *Race) SortedTools() []ToolCount {
	out := make([]ToolCount, 0, len(r.ToolCalls))
	for k, v := range r.ToolCalls {
		out = append(out, ToolCount{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Tool < out[j].Tool
	})
	return out
}
