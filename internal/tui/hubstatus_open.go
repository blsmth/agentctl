package tui

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
	"strings"

	"github.com/blsmth/agentctl/internal/verdicts"
)

// openURL opens a URL in the user's browser; a seam so tests never spawn the
// real opener. Only http(s) URLs are opened: the URL comes from a state file.
var openURL = func(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("refusing to open %q", raw)
	}
	name := "xdg-open"
	if runtime.GOOS == "darwin" {
		name = "open"
	}
	cmd := exec.Command(name, raw)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("opening %s: %w", raw, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// latestVerdict is the PR's latest review verdict from the machine-wide
// ledger ("" for none); the ledger is cached until its files change.
func latestVerdict(pr int, repo string) string {
	if r, ok := verdicts.Latest(pr, repo); ok {
		return r.Verdict
	}
	return ""
}

// prURLRepoSlug is "owner/repo" from a PR URL, or "".
func prURLRepoSlug(u string) string {
	if m := prURLRepo.FindStringSubmatch(strings.TrimSpace(u)); m != nil {
		return m[1]
	}
	return ""
}
