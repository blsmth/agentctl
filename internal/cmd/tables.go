package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/blsmth/agentctl/internal/agent"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/prompt"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/theme"
	"github.com/blsmth/agentctl/internal/tokens"
	"github.com/blsmth/agentctl/internal/tui"
)

// ─── CLI color helpers ────────────────────────────────────────────────────────

// cliStyle returns a lipgloss.Style with the given foreground color.
// lipgloss v2 automatically strips ANSI codes when stdout is not a TTY.
func cliStyle(color string) lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(color))
}

func cliHeaderStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Foreground(lipgloss.Color(theme.Current().Muted)).
		Bold(true)
}

func cliMuted() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Muted))
}

func cliAccent() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Accent))
}

func cliGreen() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Green))
}

// cliRed is the themed counterpart to cliGreen, for warnings that must not be
// missed in otherwise-muted output (stale snapshots, pane/state disagreements).
func cliRed() lipgloss.Style {
	return lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Red))
}

// cliAbbreviateModel shortens a model name to a display label.
// Thin wrapper over tokens.Abbreviate so call sites don't churn.
func cliAbbreviateModel(model string) string {
	return tokens.Abbreviate(model)
}

// ─── Tickets table ───────────────────────────────────────────────────────────

// cliIssue is a GitHub issue from `gh issue list`.
type cliIssue struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	Labels    []cliLabel `json:"labels"`
	CreatedAt time.Time  `json:"createdAt"`
	URL       string     `json:"url"`
	Assignees []cliUser  `json:"assignees"`
}

type cliLabel struct {
	Name string `json:"name"`
}

type cliUser struct {
	Login string `json:"login"`
}

// ticketsJSONRow is the flattened structure emitted for --json output.
type ticketsJSONRow struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	Age       string   `json:"age"`
	URL       string   `json:"url"`
}

// PrintTicketsTable fetches open tickets and prints them as a table or JSON.
func PrintTicketsTable(jsonOut bool) error {
	dir, _ := os.Getwd()
	slug := tui.TLRepoSlug(dir)
	if slug == "" {
		return fmt.Errorf("no git remote found in current directory")
	}

	cmd := exec.Command("gh", "issue", "list",
		"--repo", slug,
		"--limit", "50",
		"--state", "open",
		"--json", "number,title,labels,createdAt,url,assignees",
	)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("fetching issues: %w", err)
	}
	var issues []cliIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return fmt.Errorf("parsing issues: %w", err)
	}

	if jsonOut {
		rows := make([]ticketsJSONRow, 0, len(issues))
		for _, iss := range issues {
			labels := make([]string, 0, len(iss.Labels))
			for _, l := range iss.Labels {
				labels = append(labels, l.Name)
			}
			assignees := make([]string, 0, len(iss.Assignees))
			for _, u := range iss.Assignees {
				assignees = append(assignees, u.Login)
			}
			rows = append(rows, ticketsJSONRow{
				Number:    iss.Number,
				Title:     iss.Title,
				Labels:    labels,
				Assignees: assignees,
				Age:       fmtAge(time.Since(iss.CreatedAt)),
				URL:       iss.URL,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	header := cliHeaderStyle()
	muted := cliMuted()
	accent := cliAccent()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
		header.Render("#"),
		header.Render("TITLE"),
		header.Render("LABELS"),
		header.Render("ASSIGNEE"),
		header.Render("AGE"),
	)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
		muted.Render(strings.Repeat("─", 6)),
		muted.Render(strings.Repeat("─", 40)),
		muted.Render(strings.Repeat("─", 20)),
		muted.Render(strings.Repeat("─", 12)),
		muted.Render(strings.Repeat("─", 6)),
	)
	for _, iss := range issues {
		var labelStrs []string
		for _, l := range iss.Labels {
			labelStrs = append(labelStrs, l.Name)
		}
		labels := strings.Join(labelStrs, ",")
		if len(labels) > 22 {
			labels = labels[:21] + "…"
		}

		assignee := ""
		if len(iss.Assignees) > 0 {
			assignee = "@" + iss.Assignees[0].Login
		}

		num := accent.Render(fmt.Sprintf("#%d", iss.Number))
		title := iss.Title
		if len(title) > 60 {
			title = title[:59] + "…"
		}
		age := muted.Render(fmtAge(time.Since(iss.CreatedAt)))
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			num,
			title,
			muted.Render(labels),
			muted.Render(assignee),
			age,
		)
	}
	return w.Flush()
}

// ticketDetailJSON is the structure emitted for `tickets N --json`.
type ticketDetailJSON struct {
	Number    int      `json:"number"`
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	Labels    []string `json:"labels"`
	Assignees []string `json:"assignees"`
	URL       string   `json:"url"`
}

// PrintTicketDetail fetches and prints a single ticket's detail.
func PrintTicketDetail(number int, jsonOut bool) error {
	dir, _ := os.Getwd()
	slug := tui.TLRepoSlug(dir)
	if slug == "" {
		return fmt.Errorf("no git remote found in current directory")
	}

	cmd := exec.Command("gh", "issue", "view",
		fmt.Sprintf("%d", number),
		"--repo", slug,
		"--json", "number,title,body,labels,assignees,url",
	)
	out, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("fetching issue #%d: %w", number, err)
	}

	var v struct {
		Number    int        `json:"number"`
		Title     string     `json:"title"`
		Body      string     `json:"body"`
		Labels    []cliLabel `json:"labels"`
		Assignees []cliUser  `json:"assignees"`
		URL       string     `json:"url"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		return fmt.Errorf("parsing issue: %w", err)
	}

	labels := make([]string, 0, len(v.Labels))
	for _, l := range v.Labels {
		labels = append(labels, l.Name)
	}
	assignees := make([]string, 0, len(v.Assignees))
	for _, u := range v.Assignees {
		assignees = append(assignees, u.Login)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(ticketDetailJSON{
			Number:    v.Number,
			Title:     v.Title,
			Body:      v.Body,
			Labels:    labels,
			Assignees: assignees,
			URL:       v.URL,
		})
	}

	labelStyle := cliMuted()
	val := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	hdStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.Current().Accent))

	fmt.Printf("%s  %s\n\n", hdStyle.Render(fmt.Sprintf("Issue #%d", v.Number)), val.Render(v.Title))
	fmt.Printf("%s  %s\n", labelStyle.Render("URL:      "), val.Render(v.URL))
	fmt.Printf("%s  %s\n", labelStyle.Render("Labels:   "), val.Render(strings.Join(labels, ", ")))

	if len(assignees) > 0 {
		var at []string
		for _, a := range assignees {
			at = append(at, "@"+a)
		}
		fmt.Printf("%s  %s\n", labelStyle.Render("Assignees:"), val.Render(strings.Join(at, ", ")))
	} else {
		fmt.Printf("%s  %s\n", labelStyle.Render("Assignees:"), cliMuted().Render("(unassigned)"))
	}

	if v.Body != "" {
		fmt.Printf("\n%s\n\n%s\n", labelStyle.Render("── Body ──"), v.Body)
	}
	return nil
}

// ─── Sessions table ──────────────────────────────────────────────────────────

// sessionsJSONRow is the flattened structure emitted for --json output.
type sessionsJSONRow struct {
	ID       string  `json:"id"`
	Agent    string  `json:"agent"`
	Project  string  `json:"project"`
	Model    string  `json:"model"`
	Duration string  `json:"duration"`
	Turns    int     `json:"turns"`
	CostUSD  float64 `json:"cost_usd"`
	Age      string  `json:"age"`
}

// PrintSessionsTable fetches recent sessions and prints them as a table or JSON.
func PrintSessionsTable(jsonOut bool) error {
	recentSessions, err := sessions.FindRecentSessions(50, 30*24*time.Hour)
	if err != nil {
		return fmt.Errorf("loading sessions: %w", err)
	}

	type sessRow struct {
		id       string
		agent    string
		project  string
		model    string
		duration time.Duration
		turns    int
		costUSD  float64
		age      string
	}

	var rows []sessRow
	for _, s := range recentSessions {
		sum, err := sessions.ParseSummary(s)
		if err != nil {
			continue
		}
		dur := s.Duration()
		project := filepath.Base(s.Project)
		modelShort := cliAbbreviateModel(sum.ModelName)
		age := fmtAge(time.Since(s.StartTime))

		rows = append(rows, sessRow{
			id:       s.ID,
			agent:    s.Agent,
			project:  project,
			model:    modelShort,
			duration: dur,
			turns:    sum.TurnCount,
			costUSD:  sessions.EstimateCostFromSummary(sum),
			age:      age,
		})
	}

	if jsonOut {
		jrows := make([]sessionsJSONRow, 0, len(rows))
		for _, r := range rows {
			jrows = append(jrows, sessionsJSONRow{
				ID:       r.id,
				Agent:    r.agent,
				Project:  r.project,
				Model:    r.model,
				Duration: fmtDuration(r.duration),
				Turns:    r.turns,
				CostUSD:  r.costUSD,
				Age:      r.age,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(jrows)
	}

	hdr := cliHeaderStyle()
	muted := cliMuted()
	accent := cliAccent()
	green := cliGreen()

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		hdr.Render("AGE"),
		hdr.Render("AGENT"),
		hdr.Render("PROJECT"),
		hdr.Render("MODEL"),
		hdr.Render("TURNS"),
		hdr.Render("DUR"),
		hdr.Render("COST"),
	)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
		muted.Render(strings.Repeat("─", 8)),
		muted.Render(strings.Repeat("─", 18)),
		muted.Render(strings.Repeat("─", 12)),
		muted.Render(strings.Repeat("─", 8)),
		muted.Render(strings.Repeat("─", 5)),
		muted.Render(strings.Repeat("─", 6)),
		muted.Render(strings.Repeat("─", 8)),
	)
	for _, r := range rows {
		costStr := "-"
		if r.costUSD > 0 {
			costStr = fmt.Sprintf("$%.3f", r.costUSD)
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			muted.Render(r.age),
			accent.Render(r.agent),
			muted.Render(r.project),
			muted.Render(r.model),
			fmt.Sprintf("%d", r.turns),
			muted.Render(fmtDuration(r.duration)),
			green.Render(costStr),
		)
	}
	return w.Flush()
}

// PrintSessionDetail prints details for a single session by ID prefix.
func PrintSessionDetail(id string, jsonOut bool) error {
	recentSessions, err := sessions.FindRecentSessions(200, 90*24*time.Hour)
	if err != nil {
		return fmt.Errorf("loading sessions: %w", err)
	}

	// Find by ID prefix match.
	var matched *sessions.Session
	for i := range recentSessions {
		if strings.HasPrefix(recentSessions[i].ID, id) {
			s := recentSessions[i]
			matched = &s
			break
		}
	}
	if matched == nil {
		return fmt.Errorf("session %q not found", id)
	}

	sum, err := sessions.ParseSummary(*matched)
	if err != nil {
		return fmt.Errorf("parsing session: %w", err)
	}

	if jsonOut {
		type detail struct {
			ID           string  `json:"id"`
			Agent        string  `json:"agent"`
			Project      string  `json:"project"`
			Model        string  `json:"model"`
			Duration     string  `json:"duration"`
			Turns        int     `json:"turns"`
			CostUSD      float64 `json:"cost_usd"`
			InputTokens  int     `json:"input_tokens"`
			OutputTokens int     `json:"output_tokens"`
			GitBranch    string  `json:"git_branch"`
			LogFile      string  `json:"log_file"`
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(detail{
			ID:           matched.ID,
			Agent:        matched.Agent,
			Project:      matched.Project,
			Model:        cliAbbreviateModel(sum.ModelName),
			Duration:     fmtDuration(matched.Duration()),
			Turns:        sum.TurnCount,
			CostUSD:      sessions.EstimateCostFromSummary(sum),
			InputTokens:  sum.TotalInputTokens,
			OutputTokens: sum.TotalOutputTokens,
			GitBranch:    sum.GitBranch,
			LogFile:      matched.LogFile,
		})
	}

	labelStyle := cliMuted()
	val := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	hdStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.Current().Accent))

	fmt.Printf("%s\n\n", hdStyle.Render("Session "+matched.ID))
	fmt.Printf("%s  %s\n", labelStyle.Render("Agent:   "), val.Render(matched.Agent))
	fmt.Printf("%s  %s\n", labelStyle.Render("Project: "), val.Render(matched.Project))
	fmt.Printf("%s  %s\n", labelStyle.Render("Model:   "), val.Render(cliAbbreviateModel(sum.ModelName)))
	fmt.Printf("%s  %s\n", labelStyle.Render("Duration:"), val.Render(fmtDuration(matched.Duration())))
	fmt.Printf("%s  %d\n", labelStyle.Render("Turns:   "), sum.TurnCount)
	if sum.GitBranch != "" {
		fmt.Printf("%s  %s\n", labelStyle.Render("Branch:  "), val.Render(sum.GitBranch))
	}
	cost := sessions.EstimateCostFromSummary(sum)
	if cost > 0 {
		fmt.Printf("%s  %s\n", labelStyle.Render("Cost:    "), cliGreen().Render(fmt.Sprintf("$%.4f", cost)))
	}
	fmt.Printf("%s  %d in / %d out\n", labelStyle.Render("Tokens:  "),
		sum.TotalInputTokens, sum.TotalOutputTokens)
	fmt.Printf("%s  %s\n", labelStyle.Render("Log:     "), cliMuted().Render(matched.LogFile))
	return nil
}

// fmtAge formats a duration as a human-readable age string (e.g. "2h ago").
func fmtAge(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// fmtDuration formats a duration as a short human-readable string.
func fmtDuration(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if m == 0 {
		return fmt.Sprintf("%dh", h)
	}
	return fmt.Sprintf("%dh%dm", h, m)
}

// ─── Agents table ────────────────────────────────────────────────────────────

// agentsJSONRow is the flattened row for `agents --json`.
type agentsJSONRow struct {
	Name        string `json:"name"`
	Model       string `json:"model"`
	Scope       string `json:"scope"`
	Description string `json:"description"`
	Tokens      int    `json:"tokens"`
}

// PrintAgentsTable discovers agents and prints them as a table or JSON.
func PrintAgentsTable(jsonOut bool) error {
	cfg := config.Load()
	agts, err := agent.DiscoverFromCwd(cfg)
	if err != nil {
		return fmt.Errorf("discovering agents: %w", err)
	}

	if jsonOut {
		rows := make([]agentsJSONRow, 0, len(agts))
		for _, a := range agts {
			tok := resolveAgentTokens(a)
			rows = append(rows, agentsJSONRow{
				Name:        a.Name,
				Model:       a.Model,
				Scope:       a.Scope.String(),
				Description: a.Description,
				Tokens:      tok,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	hdr := cliHeaderStyle()
	muted := cliMuted()
	accent := cliAccent()
	blue := cliStyle(theme.Current().Blue)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
		hdr.Render("NAME"),
		hdr.Render("MODEL"),
		hdr.Render("SCOPE"),
		hdr.Render("TOKENS"),
		hdr.Render("DESCRIPTION"),
	)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
		muted.Render(strings.Repeat("─", 20)),
		muted.Render(strings.Repeat("─", 10)),
		muted.Render(strings.Repeat("─", 8)),
		muted.Render(strings.Repeat("─", 6)),
		muted.Render(strings.Repeat("─", 30)),
	)
	for _, a := range agts {
		tok := resolveAgentTokens(a)
		tokStr := "-"
		if tok > 0 {
			if tok >= 1000 {
				tokStr = fmt.Sprintf("%dk", tok/1000)
			} else {
				tokStr = fmt.Sprintf("%d", tok)
			}
		}
		model := a.Model
		if model == "" {
			model = "-"
		}
		desc := a.Description
		if len(desc) > 50 {
			desc = desc[:49] + "…"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			accent.Render(a.Name),
			muted.Render(model),
			blue.Render(a.Scope.String()),
			muted.Render(tokStr),
			desc,
		)
	}
	return w.Flush()
}

// resolveAgentTokens estimates the token count for an agent's prompt.
func resolveAgentTokens(a agent.Agent) int {
	if a.FilePath == "" {
		return 0
	}
	rp, err := prompt.Resolve(a.FilePath)
	if err != nil {
		return 0
	}
	return rp.Tokens.Total
}

// PrintAgentDetail prints details for a named agent.
func PrintAgentDetail(name string, jsonOut bool) error {
	cfg := config.Load()
	agts, err := agent.DiscoverFromCwd(cfg)
	if err != nil {
		return fmt.Errorf("discovering agents: %w", err)
	}

	var found *agent.Agent
	for i := range agts {
		if agts[i].Name == name {
			found = &agts[i]
			break
		}
	}
	if found == nil {
		return fmt.Errorf("agent %q not found", name)
	}

	tok := resolveAgentTokens(*found)

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(agentsJSONRow{
			Name:        found.Name,
			Model:       found.Model,
			Scope:       found.Scope.String(),
			Description: found.Description,
			Tokens:      tok,
		})
	}

	labelStyle := cliMuted()
	val := lipgloss.NewStyle().Foreground(lipgloss.Color(theme.Current().Text))
	hdStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(theme.Current().Accent))

	fmt.Printf("%s\n\n", hdStyle.Render("Agent: "+found.Name))
	fmt.Printf("%s  %s\n", labelStyle.Render("Scope:      "), val.Render(found.Scope.String()))
	if found.Model != "" {
		fmt.Printf("%s  %s\n", labelStyle.Render("Model:      "), val.Render(found.Model))
	}
	if found.Description != "" {
		fmt.Printf("%s  %s\n", labelStyle.Render("Description:"), val.Render(found.Description))
	}
	if found.Extends != "" {
		fmt.Printf("%s  %s\n", labelStyle.Render("Extends:    "), val.Render(found.Extends))
	}
	if found.Dir != "" {
		fmt.Printf("%s  %s\n", labelStyle.Render("Dir:        "), val.Render(found.Dir))
	}
	if tok > 0 {
		fmt.Printf("%s  %d\n", labelStyle.Render("Tokens:     "), tok)
	}
	fmt.Printf("%s  %s\n", labelStyle.Render("File:       "), cliMuted().Render(found.FilePath))
	return nil
}

// ─── Skills table ─────────────────────────────────────────────────────────────

// skillsJSONRow is the flattened row for `skills --json`.
type skillsJSONRow struct {
	Name   string `json:"name"`
	Scope  string `json:"scope"`
	Tokens int    `json:"tokens"`
	File   string `json:"file"`
}

// PrintSkillsTable discovers Claude Code slash-command files and prints them
// as a table or JSON. Commands are discovered from ~/.claude/commands/ (global)
// and $CWD/.claude/commands/ (project-local).
func PrintSkillsTable(jsonOut bool) error {
	cwd, _ := os.Getwd()
	skills := tui.DiscoverSkills(cwd)

	if jsonOut {
		rows := make([]skillsJSONRow, 0, len(skills))
		for _, s := range skills {
			rows = append(rows, skillsJSONRow{
				Name:   s.Name,
				Scope:  s.Scope.String(),
				Tokens: s.Tokens,
				File:   s.FilePath,
			})
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	hdr := cliHeaderStyle()
	muted := cliMuted()
	accent := cliAccent()
	blue := cliStyle(theme.Current().Blue)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
		hdr.Render("COMMAND"),
		hdr.Render("SCOPE"),
		hdr.Render("TOKENS"),
		hdr.Render("FILE"),
	)
	fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
		muted.Render(strings.Repeat("─", 20)),
		muted.Render(strings.Repeat("─", 8)),
		muted.Render(strings.Repeat("─", 6)),
		muted.Render(strings.Repeat("─", 40)),
	)

	if len(skills) == 0 {
		fmt.Fprintf(w, "%s\n", muted.Render("(no slash-command files found)"))
		return w.Flush()
	}

	for _, s := range skills {
		tokStr := "-"
		if s.Tokens > 0 {
			if s.Tokens >= 1000 {
				tokStr = fmt.Sprintf("%dk", s.Tokens/1000)
			} else {
				tokStr = fmt.Sprintf("%d", s.Tokens)
			}
		}
		var scopeStr string
		if s.Scope == tui.SkillScopeProject {
			scopeStr = blue.Render(s.Scope.String())
		} else {
			scopeStr = muted.Render(s.Scope.String())
		}
		fp := shortenPath(s.FilePath)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n",
			accent.Render(s.Name),
			scopeStr,
			muted.Render(tokStr),
			muted.Render(fp),
		)
	}
	return w.Flush()
}

// ─── Memory table ─────────────────────────────────────────────────────────────

// memoryEntry represents a discovered CLAUDE.md memory file.
type memoryEntry struct {
	Path   string
	Scope  string
	Tokens int
}

// memoryJSONRow is the flattened row for `memory --json`.
type memoryJSONRow struct {
	Path   string `json:"path"`
	Scope  string `json:"scope"`
	Tokens int    `json:"tokens"`
}

// PrintMemoryTable discovers CLAUDE.md memory files and prints them.
func PrintMemoryTable(jsonOut bool) error {
	entries := discoverMemoryFiles()
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Path < entries[j].Path
	})

	if jsonOut {
		rows := make([]memoryJSONRow, 0, len(entries))
		for _, e := range entries {
			rows = append(rows, memoryJSONRow(e))
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(rows)
	}

	hdr := cliHeaderStyle()
	muted := cliMuted()
	accent := cliAccent()
	blue := cliStyle(theme.Current().Blue)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "%s\t%s\t%s\n",
		hdr.Render("PATH"),
		hdr.Render("SCOPE"),
		hdr.Render("TOKENS"),
	)
	fmt.Fprintf(w, "%s\t%s\t%s\n",
		muted.Render(strings.Repeat("─", 40)),
		muted.Render(strings.Repeat("─", 8)),
		muted.Render(strings.Repeat("─", 6)),
	)
	for _, e := range entries {
		tokStr := "-"
		if e.Tokens > 0 {
			if e.Tokens >= 1000 {
				tokStr = fmt.Sprintf("%dk", e.Tokens/1000)
			} else {
				tokStr = fmt.Sprintf("%d", e.Tokens)
			}
		}
		fmt.Fprintf(w, "%s\t%s\t%s\n",
			accent.Render(shortenPath(e.Path)),
			blue.Render(e.Scope),
			muted.Render(tokStr),
		)
	}
	return w.Flush()
}

// discoverMemoryFiles finds CLAUDE.md files from common locations.
func discoverMemoryFiles() []memoryEntry {
	var entries []memoryEntry
	seen := make(map[string]bool)

	add := func(path, scope string) {
		abs, err := filepath.Abs(path)
		if err != nil || seen[abs] {
			return
		}
		if _, err := os.Stat(abs); err != nil {
			return
		}
		seen[abs] = true
		tok := estimateFileTokens(abs)
		entries = append(entries, memoryEntry{Path: abs, Scope: scope, Tokens: tok})
	}

	// Global Claude memory.
	home, _ := os.UserHomeDir()
	if home != "" {
		add(filepath.Join(home, ".claude", "CLAUDE.md"), "global")
		// Shared skill files under ~/.claude/agents/shared/.
		sharedDir := filepath.Join(home, ".claude", "agents", "shared")
		if fis, err := os.ReadDir(sharedDir); err == nil {
			for _, fi := range fis {
				if !fi.IsDir() && strings.HasSuffix(fi.Name(), ".md") {
					add(filepath.Join(sharedDir, fi.Name()), "shared")
				}
			}
		}
	}

	// Project-local CLAUDE.md files relative to cwd.
	cwd, _ := os.Getwd()
	if cwd != "" {
		add(filepath.Join(cwd, "CLAUDE.md"), "project")
		add(filepath.Join(cwd, ".claude", "CLAUDE.md"), "project")
	}

	return entries
}

// estimateFileTokens reads a file and estimates its token count.
func estimateFileTokens(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return prompt.EstimateTokens(string(data))
}

// shortenPath replaces the home directory prefix with ~.
func shortenPath(p string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return p
	}
	rel, err := filepath.Rel(home, p)
	if err != nil {
		return p
	}
	if !strings.HasPrefix(rel, "..") {
		return "~/" + rel
	}
	return p
}
