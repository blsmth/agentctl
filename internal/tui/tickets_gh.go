package tui

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
)

// Issue states the list can show (gh issue list --state).
const (
	tkStateOpen   = "open"
	tkStateClosed = "closed"
	tkStateAll    = "all"
)

// nextIssueState cycles open → closed → all → open.
func nextIssueState(s string) string {
	switch s {
	case tkStateOpen, "":
		return tkStateClosed
	case tkStateClosed:
		return tkStateAll
	}
	return tkStateOpen
}

// tkIssueView is the JSON shape of `gh issue view --json` used by the browser.
type tkIssueView struct {
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Labels    []ghLabel `json:"labels"`
	Assignees []ghUser  `json:"assignees"`
	URL       string    `json:"url"`
	State     string    `json:"state"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// viewIssue fetches one issue's full details.
func viewIssue(slug string, number int) tkDetailFetchedMsg {
	out, err := tkExec("gh", "issue", "view", strconv.Itoa(number),
		"--repo", slug,
		"--json", "title,body,labels,assignees,url,state,updatedAt",
	)
	if err != nil {
		return tkDetailFetchedMsg{number: number, err: err}
	}
	var v tkIssueView
	if err := json.Unmarshal(out, &v); err != nil {
		return tkDetailFetchedMsg{number: number, err: err}
	}
	return tkDetailFetchedMsg{
		number:    number,
		title:     v.Title,
		body:      v.Body,
		labels:    v.Labels,
		assignees: v.Assignees,
		url:       v.URL,
		state:     v.State,
		updatedAt: v.UpdatedAt,
	}
}

// tkFetchPreviewCmd fetches full details for a single issue and returns tkPreviewFetchedMsg.
func tkFetchPreviewCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg { return tkPreviewFetchedMsg(viewIssue(slug, number)) }
}

// tkFetchDetailCmd fetches full details for a single issue.
func tkFetchDetailCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg { return viewIssue(slug, number) }
}

// tkFetchTicketsCmd fetches the repo's issues in the given state ("open",
// "closed" or "all"). It also fetches open PRs and builds a map of issue
// number → linked PR.
func tkFetchTicketsCmd(dir, state string) tea.Cmd {
	return func() tea.Msg {
		slug := TLRepoSlug(dir)
		if slug == "" {
			return ticketsFetchedMsg{repoSlug: ""}
		}
		issues, err := listIssues(slug, state, 20)
		if err != nil {
			return ticketsFetchedMsg{repoSlug: slug, err: err}
		}

		// Fetch open PRs and build issue number → PR map from branch names.
		linkedPRs := make(map[int]ghPR)
		prOut, prErr := tkExec("gh", "pr", "list",
			"--repo", slug,
			"--state", "open",
			"--limit", "50",
			"--json", "number,title,headRefName,url",
		)
		if prErr == nil {
			var prs []ghPR
			if json.Unmarshal(prOut, &prs) == nil {
				for _, pr := range prs {
					if issNum := extractIssueFromBranch(pr.HeadRefName); issNum > 0 {
						linkedPRs[issNum] = pr
					}
				}
			}
		}

		return ticketsFetchedMsg{issues: issues, repoSlug: slug, linkedPRs: linkedPRs}
	}
}

// listOpenIssues returns up to limit open issues for the repo slug via
// `gh issue list`. Shared by the tickets browser and the ticket launcher.
func listOpenIssues(slug string, limit int) ([]ghIssue, error) {
	return listIssues(slug, tkStateOpen, limit)
}

// listIssues returns up to limit issues in state ("open", "closed", "all").
func listIssues(slug, state string, limit int) ([]ghIssue, error) {
	if state == "" {
		state = tkStateOpen
	}
	out, err := tkExec("gh", "issue", "list",
		"--repo", slug,
		"--limit", strconv.Itoa(limit),
		"--state", state,
		"--json", "number,title,labels,createdAt,url,assignees,state",
	)
	if err != nil {
		return nil, err
	}
	var issues []ghIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, err
	}
	return issues, nil
}

// tkBrowserOpenedMsg reports a failed browser open (nil error is dropped).
type tkBrowserOpenedMsg struct{ err error }

// openIssueInBrowserCmd opens the given issue in the browser.
func openIssueInBrowserCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		_, err := tkExec("gh", "issue", "view", strconv.Itoa(number), "--repo", slug, "--web")
		return tkBrowserOpenedMsg{err: err}
	}
}

// tkOpenPRInBrowserCmd opens a PR URL in the browser.
func tkOpenPRInBrowserCmd(url string) tea.Cmd {
	return func() tea.Msg {
		_, err := tkExec("gh", "pr", "view", url, "--web")
		return tkBrowserOpenedMsg{err: err}
	}
}

// tkEditIssueCmd opens the given issue for editing in the browser via `gh issue edit --web`.
func tkEditIssueCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		_, err := tkExec("gh", "issue", "edit", strconv.Itoa(number), "--repo", slug, "--web")
		return tkBrowserOpenedMsg{err: err}
	}
}

// tkCloseIssueCmd closes a GitHub issue and returns tkIssueClosedMsg.
func tkCloseIssueCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		_, err := tkExec("gh", "issue", "close", strconv.Itoa(number), "--repo", slug)
		return tkIssueClosedMsg{number: number, err: err}
	}
}

// tkAssignSelfCmd assigns the current user to a GitHub issue.
func tkAssignSelfCmd(slug string, number int) tea.Cmd {
	return func() tea.Msg {
		_, err := tkExec("gh", "issue", "edit", strconv.Itoa(number), "--repo", slug, "--add-assignee", "@me")
		return tkSelfAssignedMsg{number: number, err: err}
	}
}

// tkCopiedMsg reports the outcome of copying an issue URL to the tmux buffer.
type tkCopiedMsg struct {
	url string
	err error
}

// tkCopyURLCmd copies url into the tmux paste buffer.
func tkCopyURLCmd(url string) tea.Cmd {
	return func() tea.Msg {
		_, err := tkExec("tmux", "set-buffer", "--", url)
		if err != nil {
			err = fmt.Errorf("tmux set-buffer: %w", err)
		}
		return tkCopiedMsg{url: url, err: err}
	}
}
