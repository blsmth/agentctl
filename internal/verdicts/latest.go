package verdicts

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// latestCache holds the ledger's records between lookups; it is re-read only
// when a monthly file's name, size or mtime changes, so a caller on a fast
// tick pays a few stats per lookup, not a parse.
var latestCache struct {
	mu   sync.Mutex
	sig  string
	recs []Record // newest first
	// loads counts re-reads, for tests.
	loads int
}

// ledgerSignature identifies the ledger files' current state.
func ledgerSignature() (string, error) {
	dir, err := dirFn()
	if err != nil {
		return "", err
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "verdicts-*.ndjson"))
	sort.Strings(matches)
	var sb strings.Builder
	sb.WriteString(dir)
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		fmt.Fprintf(&sb, "|%s:%d:%d", filepath.Base(m), fi.Size(), fi.ModTime().UnixNano())
	}
	return sb.String(), nil
}

// Latest is the newest verdict recorded for PR number pr within MaxAge. repo
// ("owner/repo") narrows the match when both it and the record's Repo are
// set; "" matches any repo. The ledger is cached and re-read only when its
// files change.
func Latest(pr int, repo string) (Record, bool) {
	sig, err := ledgerSignature()
	if err != nil {
		return Record{}, false
	}
	latestCache.mu.Lock()
	defer latestCache.mu.Unlock()
	if sig != latestCache.sig {
		recs, err := Load(now().Add(-MaxAge))
		if err != nil {
			return Record{}, false
		}
		latestCache.sig, latestCache.recs = sig, recs
		latestCache.loads++
	}
	for _, r := range latestCache.recs {
		if r.PR != pr {
			continue
		}
		if repo != "" && r.Repo != "" && !strings.EqualFold(r.Repo, repo) {
			continue
		}
		return r, true
	}
	return Record{}, false
}
