package statusline

import (
	"regexp"
	"strconv"
	"strings"

	"github.com/blsmth/agentctl/internal/herd"
	"github.com/blsmth/agentctl/internal/windowstate"
)

var pullURLRe = regexp.MustCompile(`/pull/([0-9]+)/?$`)

// prFromURL extracts N from a trailing /pull/N, or 0.
func prFromURL(url string) int {
	m := pullURLRe.FindStringSubmatch(url)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0
	}
	return n
}

// resolvePR finds the PR to show. Order: the Claude Code payload, then the
// window's windowstate PRNumber/PRURL (dispatch completion), then the newest
// pr-link in the session log (the same source as the hub). The log is scanned
// incrementally from the cursor cached in ws.LogPR; the updated cache is
// returned with changed=true only when it differs from what ws holds. A nil
// result means no PR segment.
func resolvePR(p *Payload, ws windowstate.WindowState) (pr *PR, cache windowstate.LogPRScan, changed bool) {
	cache = ws.LogPR
	if p.PR != nil && p.PR.Number != nil {
		return p.PR, cache, false
	}
	if ws.PRURL != "" || ws.PRNumber > 0 {
		n := ws.PRNumber
		if n == 0 {
			n = prFromURL(ws.PRURL)
		}
		if n > 0 {
			return newPR(n, ws.PRURL), cache, false
		}
	}

	path := ws.LogPath
	if p.TranscriptPath != nil && *p.TranscriptPath != "" {
		path = *p.TranscriptPath
	}
	if path == "" {
		return nil, cache, false
	}
	next := cache
	if next.Path != path {
		next = windowstate.LogPRScan{Path: path}
	}
	offset, size, info, fresh, ok := herd.ScanLogFrom(path, next.Offset, next.Size)
	if !ok {
		return nil, cache, false
	}
	next.Offset, next.Size = offset, size
	if fresh || info.PRURL != "" {
		next.PRNumber, next.PRURL = info.PRNumber, info.PRURL
	}
	changed = next != cache
	cache = next
	n := cache.PRNumber
	if n == 0 {
		n = prFromURL(cache.PRURL)
	}
	if n == 0 {
		return nil, cache, changed
	}
	return newPR(n, cache.PRURL), cache, changed
}

func newPR(n int, url string) *PR {
	f := float64(n)
	pr := &PR{Number: &f}
	if url != "" {
		pr.URL = &url
	}
	return pr
}

// readResolvedPR is the read-only path (--dry-run): resolve without writing
// the scan cursor. Never errors: no PR means no segment.
func readResolvedPR(p *Payload, windowID string) *PR {
	if windowID == "" {
		return p.PR
	}
	ws, err := windowstate.Read(windowID)
	if err != nil {
		return p.PR
	}
	pr, _, _ := resolvePR(p, ws)
	return pr
}

// prLinkable reports whether url is safe to emit as a hyperlink.
func prLinkable(url string) bool {
	return strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://")
}
