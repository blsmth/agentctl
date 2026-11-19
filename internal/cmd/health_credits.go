package cmd

import (
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/blsmth/agentctl/internal/backendauth"
	"github.com/blsmth/agentctl/internal/config"
)

// printCreditsHealth reports each nous backend whose no-credits marker is
// set. It reads local state only and prints nothing when every backend is
// clear. Warn only.
func printCreditsHealth(w io.Writer, backends map[string]config.BackendConfig, now time.Time) {
	var names []string
	for n, b := range backends {
		if b.IsNous() {
			names = append(names, n)
		}
	}
	sort.Strings(names)
	for _, n := range names {
		st := backendauth.LoadCreditState(n)
		if st.NoCreditsSince.IsZero() {
			continue
		}
		fmt.Fprintf(w, "Backend %s: warn: Nous Portal out of credits since %s (last checked %s). Add credits at %s or use a :free model; `agentctl backend status --verify` rechecks.\n",
			n, backendauth.RelativeSince(st.NoCreditsSince, now), backendauth.RelativeSince(st.LastChecked, now), backendauth.CreditsURL)
	}
}
