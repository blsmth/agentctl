package dispatch

import (
	"encoding/json"
	"fmt"
	"strings"
)

// PullRequest is the PR a ModeReviewPR launch reviews, as fetched by FetchPR,
// plus what the CLI adds: extra instructions and the author's window.
type PullRequest struct {
	Number  int
	Title   string
	URL     string
	Body    string
	BaseRef string
	HeadRef string
	HeadSHA string
	State   string // OPEN, CLOSED or MERGED
	Login   string // GitHub login of the PR author
	Files   []string

	// Additions, Deletions and ChangedFiles are the diffstat as GitHub
	// reports it; CI is the check state at launch: green, pending, red or
	// none (see ciState).
	Additions    int
	Deletions    int
	ChangedFiles int
	CI           string

	// Extra is optional additional review instructions (--prompt or
	// --prompt-key), rendered under "## Additional review instructions".
	Extra string
	// Author is the live agentctl window that owns the PR branch; zero when
	// none was found.
	Author PRAuthor
}

// PRAuthor identifies the session that wrote a PR, so the reviewer can work
// fixes out with it over SendMessage.
type PRAuthor struct {
	WindowID string // tmux window ID, e.g. "@26"
	Pane     string // its agent pane, e.g. "%777"; "" when unknown
	Name     string // session name, set only when known to be accurate
	Account  string // configured Claude account the window runs under

	// Agent, SessionID and RuntimeDir identify the author for the verdict
	// ledger; recorded on the reviewer's windowstate, not shown to it.
	Agent      string
	SessionID  string
	RuntimeDir string // the author session's .agentctl dir

	// crossAccount is set by Launch when the author runs under a different
	// Claude account than the reviewer: accounts do not message each other.
	crossAccount bool
}

// Known reports whether an author window was identified.
func (a PRAuthor) Known() bool { return a.WindowID != "" }

// maxReviewFiles caps the changed-file list injected into the prompt; the
// rest is summarized as "+N more".
const maxReviewFiles = 15

// FetchPR fetches PR number from repoSlug with `gh pr view` (bounded by the
// same timeout as RepoSlug). A closed or merged PR is an error: there is
// nothing left to review.
func FetchPR(repoSlug string, number int) (PullRequest, error) {
	args := []string{"pr", "view", fmt.Sprintf("%d", number),
		"--json", "number,title,url,body,baseRefName,headRefName,headRefOid,files,author,state,additions,deletions,changedFiles,statusCheckRollup"}
	if repoSlug != "" {
		args = append(args, "--repo", repoSlug)
	}
	out, err := ghRunner("", "gh", args...)
	if err != nil {
		return PullRequest{}, fmt.Errorf("fetching PR #%d from %s: %w", number, repoSlug, err)
	}
	pr, err := parsePR(out)
	if err != nil {
		return PullRequest{}, fmt.Errorf("parsing PR #%d: %w", number, err)
	}
	if pr.State != "" && pr.State != "OPEN" {
		return PullRequest{}, fmt.Errorf("PR #%d is %s; only open PRs can be reviewed", number, strings.ToLower(pr.State))
	}
	return pr, nil
}

// parsePR decodes `gh pr view --json` output.
func parsePR(data []byte) (PullRequest, error) {
	var raw struct {
		Number      int    `json:"number"`
		Title       string `json:"title"`
		URL         string `json:"url"`
		Body        string `json:"body"`
		BaseRefName string `json:"baseRefName"`
		HeadRefName string `json:"headRefName"`
		HeadRefOid  string `json:"headRefOid"`
		State       string `json:"state"`
		Author      struct {
			Login string `json:"login"`
		} `json:"author"`
		Files []struct {
			Path string `json:"path"`
		} `json:"files"`
		Additions    int               `json:"additions"`
		Deletions    int               `json:"deletions"`
		ChangedFiles int               `json:"changedFiles"`
		Rollup       []json.RawMessage `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return PullRequest{}, err
	}
	pr := PullRequest{
		Number: raw.Number, Title: raw.Title, URL: raw.URL, Body: raw.Body,
		BaseRef: raw.BaseRefName, HeadRef: raw.HeadRefName, HeadSHA: raw.HeadRefOid,
		State: raw.State, Login: raw.Author.Login,
		Additions: raw.Additions, Deletions: raw.Deletions, ChangedFiles: raw.ChangedFiles,
		CI: ciState(raw.Rollup),
	}
	for _, f := range raw.Files {
		pr.Files = append(pr.Files, f.Path)
	}
	return pr, nil
}

// ciState reduces gh's statusCheckRollup (check runs and commit statuses) to
// green, pending, red or none. Red wins over pending: a failed check already
// decides the question, whatever is still running.
func ciState(rollup []json.RawMessage) string {
	if len(rollup) == 0 {
		return "none"
	}
	pending := false
	for _, r := range rollup {
		var c struct {
			Status     string `json:"status"`     // CheckRun: QUEUED, IN_PROGRESS, COMPLETED, ...
			Conclusion string `json:"conclusion"` // CheckRun, once COMPLETED
			State      string `json:"state"`      // StatusContext: SUCCESS, FAILURE, PENDING, ...
		}
		if json.Unmarshal(r, &c) != nil {
			continue
		}
		switch strings.ToUpper(c.State) {
		case "FAILURE", "ERROR":
			return "red"
		case "PENDING", "EXPECTED":
			pending = true
		}
		if c.Status != "" && !strings.EqualFold(c.Status, "COMPLETED") {
			pending = true
			continue
		}
		switch strings.ToUpper(c.Conclusion) {
		case "FAILURE", "TIMED_OUT", "CANCELLED", "ACTION_REQUIRED", "STARTUP_FAILURE":
			return "red"
		}
	}
	if pending {
		return "pending"
	}
	return "green"
}

// reviewPRUserMessage is ModeReviewPR's first user message.
func reviewPRUserMessage(req Request) string {
	pr := req.ReviewPR
	if pr == nil {
		return "Review the pull request in your system prompt."
	}
	return fmt.Sprintf("Review PR #%d: %s. The PR and your instructions are in your system prompt; the deliverable is your verdict.", pr.Number, pr.Title)
}

// reviewPRInstructions is ModeReviewPR's system-prompt block: the PR
// context, what to deliver, and how to reach the author.
func reviewPRInstructions(req Request) string {
	pr := req.ReviewPR
	if pr == nil {
		return ""
	}
	var b strings.Builder
	b.WriteString(reviewPRContext(*pr))
	b.WriteString("\n\n## Review instructions\n\n")
	fmt.Fprintf(&b, "This is a review of PR #%d. ", pr.Number)
	if req.noMessaging {
		b.WriteString("The deliverable is your verdict, as the first line of your final message, in this form:\n\n")
	} else {
		b.WriteString("The deliverable is one verdict message to your orchestrator (SendMessage), starting with this line:\n\n")
	}
	fmt.Fprintf(&b, "REVIEW PR #%d — VERDICT: MERGE | MERGE-WITH-FOLLOWUPS | BLOCKED | NEEDS-BRENDAN\n\n", pr.Number)
	b.WriteString("- Do not commit, push, open a PR, or write to GitHub (no reviews, comments or approvals).\n")
	b.WriteString("- Work fixes out with the author (see below) for at most 3 rounds, then send the verdict.\n")
	if !req.noMessaging {
		b.WriteString("- If you have no orchestrator, put the verdict in your final message.\n")
	}
	if req.reviewSharedCheckout {
		fmt.Fprintf(&b, "- You are in the shared checkout, not a worktree: do not check out other branches here. Read the change with `gh pr diff %d`.\n", pr.Number)
	}
	b.WriteString("\n")
	b.WriteString(reviewAuthorSection(*pr, req.noMessaging))
	if extra := strings.TrimSpace(pr.Extra); extra != "" {
		b.WriteString("\n\n## Additional review instructions\n\n")
		b.WriteString(extra)
	}
	return b.String()
}

// reviewPRContext renders the PR's metadata, changed files and body.
func reviewPRContext(pr PullRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Pull request #%d: %s\n\n", pr.Number, pr.Title)
	if pr.URL != "" {
		fmt.Fprintf(&b, "- URL: %s\n", pr.URL)
	}
	fmt.Fprintf(&b, "- Base branch: %s\n", pr.BaseRef)
	fmt.Fprintf(&b, "- Head branch: %s\n", pr.HeadRef)
	fmt.Fprintf(&b, "- Head sha: %s\n", pr.HeadSHA)
	if pr.Login != "" {
		fmt.Fprintf(&b, "- GitHub author: %s\n", pr.Login)
	}
	total := max(pr.ChangedFiles, len(pr.Files))
	if total > 0 {
		fmt.Fprintf(&b, "- Diffstat: %d files, +%d/-%d\n", total, pr.Additions, pr.Deletions)
	}
	ci := pr.CI
	if ci == "" {
		ci = "none"
	}
	fmt.Fprintf(&b, "- CI at launch: %s\n", ci)
	b.WriteString("\n### Changed files\n\n")
	if len(pr.Files) == 0 {
		b.WriteString("(none listed)\n")
	}
	for i, f := range pr.Files {
		if i == maxReviewFiles {
			break
		}
		b.WriteString("- " + f + "\n")
	}
	if total > maxReviewFiles {
		fmt.Fprintf(&b, "- +%d more (`gh pr diff %d --name-only`)\n", total-maxReviewFiles, pr.Number)
	}
	b.WriteString("\n### Description\n\n")
	if body := strings.TrimSpace(pr.Body); body != "" {
		b.WriteString(body)
	} else {
		b.WriteString("(empty)")
	}
	return b.String()
}

// reviewAuthorSection tells the reviewer how to reach the PR's author, or
// that it cannot. Modeled on orchestratorSection: the author is named by
// window and pane, never by session name.
func reviewAuthorSection(pr PullRequest, noMessaging bool) string {
	a := pr.Author
	const head = "### The author\n"
	switch {
	case noMessaging:
		return head + "This harness cannot message other sessions, so there is no author to work fixes out with. Review the PR and put your findings in your final message."
	case !a.Known():
		return head + "No live agentctl session was found working on this PR's branch (" + pr.HeadRef + "), so there is no author to work fixes out with. " +
			"Review it and report your findings to your orchestrator only. Do not message any other session."
	case a.crossAccount:
		return head + "The PR's author runs under a different Claude account, and accounts are kept separate. " +
			"Do not try to reach it with SendMessage: report your findings to your orchestrator only."
	}
	id := "tmux window " + a.WindowID
	match := "the window ID " + a.WindowID
	if a.Pane != "" {
		id += ", pane " + a.Pane
		match = "the window and pane IDs (" + a.WindowID + "." + a.Pane + ")"
	}
	var b strings.Builder
	b.WriteString(head)
	b.WriteString("The PR's author is the session in " + id + ". ")
	b.WriteString("Find it in ListAgents by matching " + match + " against the `@N.%P` suffix of a row's tmux location; ignore the tmux session-name prefix before the colon, which is not stable because windows are linked into other tmux sessions. ")
	if a.Name != "" {
		b.WriteString("Its session name was " + a.Name + " when you were launched; treat that as a hint only, since names can change and the window and pane are authoritative. ")
	}
	b.WriteString("agentctl tells it that you are reviewing its PR and may message it directly. ")
	b.WriteString("Send it your must-fix list with SendMessage; it pushes fixes as new commits on the same branch and replies with the new head sha. ")
	b.WriteString("If no ListAgents row matches that window and pane, the author is gone: report your findings to your orchestrator only. Do not message a different session instead, even one with a similar name.")
	return b.String()
}

// reviewWorktreeOrientation replaces the generic worktree note for a
// ModeReviewPR launch. The worktree is cut from the default branch rather than
// the PR head: a branch carrying the PR's commits with no upstream would never
// be classified reclaimable (cleanup.Classify), so the reviewer checks out the
// head detached and returns to the base branch when done.
func reviewWorktreeOrientation(req Request, workDir, branch string) string {
	n, sha := 0, ""
	if req.ReviewPR != nil {
		n, sha = req.ReviewPR.Number, req.ReviewPR.HeadSHA
	}
	return fmt.Sprintf("You are in a git worktree at %s on branch %s, cut from the default branch, not from the PR. "+
		"Check out the PR head yourself: `git fetch origin pull/%d/head && git checkout --detach FETCH_HEAD`, then confirm `git rev-parse HEAD` is %s. "+
		"The worktree is for reading and running tests only and is never pushed. "+
		"When you have sent your verdict, run `git checkout %s` so the worktree is back at its base and can be reclaimed.",
		workDir, branch, n, sha, branch)
}

// ReviewAuthorNotice is the agentctl notice queued for the author's window
// when a reviewer is launched on its PR.
func ReviewAuthorNotice(reviewer, windowID, pane string, pr int) string {
	loc := windowID
	if pane != "" {
		loc += "." + pane
	}
	return fmt.Sprintf("agentctl notice: %s in %s is reviewing your PR #%d at the request of your orchestrator; it may message you directly.", reviewer, loc, pr)
}
