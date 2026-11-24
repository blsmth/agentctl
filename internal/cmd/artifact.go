package cmd

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/dustin/go-humanize"

	"github.com/blsmth/agentctl/internal/artifacts"
	"github.com/blsmth/agentctl/internal/artifacturl"
	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/prompttemplate"
	"github.com/blsmth/agentctl/internal/sessions"
	"github.com/blsmth/agentctl/internal/tmux"
	"github.com/blsmth/agentctl/internal/windowstate"
)

// ArtifactAddOptions are the flags of `agentctl artifact add`.
type ArtifactAddOptions struct {
	Path, Title, Description, Kind, Entry string
	Pin, NoNotify, JSON                   bool
}

// ArtifactUpdateOptions are the flags of `agentctl artifact update`.
type ArtifactUpdateOptions struct {
	ID, Path, Note, Title string
	JSON                  bool
}

// ArtifactListOptions are the flags of `agentctl artifact list`.
type ArtifactListOptions struct {
	Project, Agent, Since string
	All, JSON             bool
}

// artifactEnv holds the seams of the artifact commands: everything that
// touches tmux, the network config, the browser, or the terminal.
type artifactEnv struct {
	store func() *artifacts.Store
	// origin describes the calling window (or plain shell).
	origin func() artifacts.Origin
	// serveURL returns the base URL of artifact content links (the public
	// hostname once its certificate is held) and whether serve is running.
	serveURL func() (base string, running bool)
	// appBase is the base URL of the phone app's links (#/artifacts/...),
	// which need the device cookie and so stay on the paired, local host.
	// nil means the serveURL base.
	appBase func() string
	// openBrowser opens url in the laptop browser.
	openBrowser func(url string) error
	stdin       io.Reader
	interactive func() bool
	errw        io.Writer
	now         func() time.Time
}

var defaultArtifactEnv = artifactEnv{
	store:       artifacts.New,
	origin:      artifactOrigin,
	serveURL:    artifactServeURL,
	appBase:     artifacturl.AppBase,
	openBrowser: openInBrowser,
	stdin:       os.Stdin,
	interactive: func() bool { return stdinIsTerminal() },
	errw:        os.Stderr,
	now:         time.Now,
}

// RunArtifactAdd publishes a new artifact.
func RunArtifactAdd(w io.Writer, o ArtifactAddOptions) error { return defaultArtifactEnv.add(w, o) }

// RunArtifactUpdate adds a revision to an artifact.
func RunArtifactUpdate(w io.Writer, o ArtifactUpdateOptions) error {
	return defaultArtifactEnv.update(w, o)
}

// RunArtifactList lists artifacts.
func RunArtifactList(w io.Writer, o ArtifactListOptions) error { return defaultArtifactEnv.list(w, o) }

// RunArtifactShow prints one artifact.
func RunArtifactShow(w io.Writer, id string, jsonOut bool) error {
	return defaultArtifactEnv.show(w, id, jsonOut)
}

// RunArtifactComments prints the feedback sent on an artifact from the viewer.
func RunArtifactComments(w io.Writer, id string, jsonOut bool) error {
	return defaultArtifactEnv.comments(w, id, jsonOut)
}

// RunArtifactURL prints an artifact's URL.
func RunArtifactURL(w io.Writer, id string, rev int) error { return defaultArtifactEnv.url(w, id, rev) }

// RunArtifactOpen opens an artifact in the laptop browser.
func RunArtifactOpen(w io.Writer, id string, rev int) error {
	return defaultArtifactEnv.open(w, id, rev)
}

// RunArtifactPin pins or unpins an artifact.
func RunArtifactPin(w io.Writer, id string, pin bool) error {
	return defaultArtifactEnv.pin(w, id, pin)
}

// RunArtifactRm deletes an artifact.
func RunArtifactRm(w io.Writer, id string, yes bool) error { return defaultArtifactEnv.rm(w, id, yes) }

// RunArtifactPrune deletes old unpinned artifacts.
func RunArtifactPrune(w io.Writer, olderThan string, dryRun bool) error {
	return defaultArtifactEnv.prune(w, olderThan, dryRun)
}

// artifactView is the JSON shape of an artifact: its metadata plus where to
// view it. URL is the direct link (any browser that reaches serve, no
// pairing, for an artifact with a capability id); AppURL is the phone app's
// viewer, which needs a paired device.
type artifactView struct {
	artifacts.Meta
	URL          string `json:"url"`
	AppURL       string `json:"app_url"`
	ServeRunning bool   `json:"serve_running"`
}

func (e artifactEnv) view(m artifacts.Meta) artifactView {
	base, running := e.serveURL()
	return viewAt(m, base, e.appBaseFor(base), running)
}

// appBaseFor is the app link base: e.appBase, or base when unset.
func (e artifactEnv) appBaseFor(base string) string {
	if e.appBase == nil {
		return base
	}
	return e.appBase()
}

func viewAt(m artifacts.Meta, base, appBase string, running bool) artifactView {
	return artifactView{Meta: m, URL: artifactDirectURL(base, m.ID, 0), AppURL: artifactURL(appBase, m.ID, 0), ServeRunning: running}
}

// artifactDirectURL is the content URL: <base>/a/<id>/latest/, or
// <base>/a/<id>/<rev>/ for a numbered revision. The id is the credential,
// so it opens in any browser on an allowed network without pairing (a
// legacy 10-character id still needs a paired browser).
func artifactDirectURL(base, id string, rev int) string {
	return artifacturl.Direct(base, id, rev)
}

// artifactURL is the phone app's viewer URL: <base>/#/artifacts/<id>[/<rev>].
func artifactURL(base, id string, rev int) string {
	u := strings.TrimRight(base, "/") + "/#/artifacts/" + id
	if rev > 0 {
		u += fmt.Sprintf("/%d", rev)
	}
	return u
}

// notRunningNote tells the caller serve is down; the command still succeeds.
func (e artifactEnv) notRunningNote(u string) {
	fmt.Fprintf(e.errw, "serve is not running; the artifact will be at %s once it starts (agentctl serve install / serve start)\n", u)
}

func (e artifactEnv) add(w io.Writer, o ArtifactAddOptions) error {
	src, err := e.source(o.Path)
	if err != nil {
		return err
	}
	m, err := e.store().Add(artifacts.AddOptions{
		Source: src, Title: o.Title, Description: o.Description, Kind: o.Kind, Entry: o.Entry,
		Pin: o.Pin, NoNotify: o.NoNotify, Origin: e.origin(),
	})
	if err != nil {
		return err
	}
	return e.report(w, m, "Published", o.JSON)
}

func (e artifactEnv) update(w io.Writer, o ArtifactUpdateOptions) error {
	src, err := e.source(o.Path)
	if err != nil {
		return err
	}
	m, err := e.store().Update(o.ID, artifacts.UpdateOptions{Source: src, Note: o.Note, Title: o.Title})
	if err != nil {
		return err
	}
	return e.report(w, m, "Updated", o.JSON)
}

// report prints the result of add/update.
func (e artifactEnv) report(w io.Writer, m artifacts.Meta, verb string, jsonOut bool) error {
	v := e.view(m)
	if !v.ServeRunning {
		e.notRunningNote(v.URL)
	}
	if jsonOut {
		return writeJSON(w, v)
	}
	fmt.Fprintf(w, "%s %q as %s (rev %d)\n%s\napp: %s\n", verb, m.Title, m.ID, m.Latest(), v.URL, v.AppURL)
	return nil
}

// source turns the path argument into a Source; "-" reads stdin.
func (e artifactEnv) source(p string) (artifacts.Source, error) {
	switch p {
	case "":
		return artifacts.Source{}, errors.New("a path, a directory, or - (stdin) is required")
	case "-":
		return artifacts.Source{Stdin: e.stdin}, nil
	}
	return artifacts.Source{Path: p}, nil
}

func (e artifactEnv) list(w io.Writer, o ArtifactListOptions) error {
	f := artifacts.Filter{Project: o.Project, Agent: o.Agent}
	if o.Since != "" {
		d, err := sessions.ParseSince(o.Since)
		if err != nil {
			return err
		}
		if d > 0 {
			f.Since = e.now().Add(-d)
		}
	}
	if !o.All && o.Project == "" {
		f.Project = e.origin().ProjectDir
	}
	list, err := e.store().List(f)
	if err != nil {
		return err
	}
	base, running := e.serveURL()
	if o.JSON {
		app := e.appBaseFor(base)
		out := make([]artifactView, 0, len(list))
		for _, m := range list {
			out = append(out, viewAt(m, base, app, running))
		}
		return writeJSON(w, out)
	}
	if len(list) == 0 {
		scope := "in this project (use --all for every project)"
		if o.All || o.Project != "" {
			scope = "matching"
		}
		fmt.Fprintf(w, "No artifacts %s.\n", scope)
		return nil
	}
	rows := [][]string{{"ID", "KIND", "PIN", "TITLE", "AGENT", "PROJECT", "REVS", "UPDATED"}}
	for _, m := range list {
		pin := ""
		if m.Pinned {
			pin = "📌"
		}
		rows = append(rows, []string{m.ID, m.Kind, pin, truncWidth(m.Title, 48),
			dash(m.Agent), dash(m.Project), strconv.Itoa(len(m.Revs)), relTime(m.Updated, e.now())})
	}
	return writeTable(w, rows)
}

// writeTable prints rows as columns two spaces apart, padding by display
// width: an emoji or CJK cell is two columns wide, which text/tabwriter
// (rune counts) gets wrong. The last column is not padded.
func writeTable(w io.Writer, rows [][]string) error {
	widths := columnWidths(rows)
	var b strings.Builder
	for _, r := range rows {
		for i, c := range r {
			b.WriteString(c)
			if i < len(r)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-ansi.StringWidth(c)+2))
			}
		}
		b.WriteByte('\n')
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// columnWidths returns the widest display width in each column.
func columnWidths(rows [][]string) []int {
	widths := make([]int, len(rows[0]))
	for _, r := range rows {
		for i, c := range r {
			widths[i] = max(widths[i], ansi.StringWidth(c))
		}
	}
	return widths
}

// truncWidth shortens s to at most n display columns, ending in "…".
func truncWidth(s string, n int) string {
	if ansi.StringWidth(s) <= n {
		return s
	}
	return ansi.Truncate(s, n, "…")
}

func (e artifactEnv) show(w io.Writer, id string, jsonOut bool) error {
	m, err := e.store().Get(id)
	if err != nil {
		return err
	}
	v := e.view(m)
	if jsonOut {
		return writeJSON(w, v)
	}
	base, _ := e.serveURL()
	fmt.Fprintf(w, "%s  %s\n", m.ID, m.Title)
	row := func(k, val string) {
		if val != "" {
			fmt.Fprintf(w, "  %-12s %s\n", k+":", val)
		}
	}
	row("kind", m.Kind)
	row("entry", m.Entry)
	row("description", m.Description)
	row("project", m.Project)
	row("agent", m.Agent)
	row("window", m.WindowID)
	row("session", m.Session)
	row("branch", m.Branch)
	row("created", m.Created.Local().Format(time.RFC3339))
	row("updated", m.Updated.Local().Format(time.RFC3339))
	if m.Pinned {
		row("pinned", "yes")
	}
	row("url", v.URL)
	row("app", v.AppURL)
	fmt.Fprintln(w, "  revisions:")
	for i := len(m.Revs) - 1; i >= 0; i-- {
		r := m.Revs[i]
		note := ""
		if r.Note != "" {
			note = "  " + r.Note
		}
		fmt.Fprintf(w, "    #%d  %s  %s%s\n      %s\n", r.N, r.Created.Local().Format("2006-01-02 15:04"),
			humanize.Bytes(uint64(r.Size)), note, artifactDirectURL(base, m.ID, r.N))
	}
	if len(m.Comments) > 0 {
		fmt.Fprintf(w, "  comments (%d, rev  time  from  status):\n", len(m.Comments))
		printComments(w, m.Comments, "    ")
	}
	return nil
}

func (e artifactEnv) comments(w io.Writer, id string, jsonOut bool) error {
	m, err := e.store().Get(id)
	if err != nil {
		return err
	}
	cs := m.Comments
	if cs == nil {
		cs = []artifacts.Comment{}
	}
	if jsonOut {
		return writeJSON(w, cs)
	}
	if len(cs) == 0 {
		fmt.Fprintln(w, "no comments")
		return nil
	}
	printComments(w, cs, "")
	return nil
}

// printComments writes one block per comment, oldest first.
func printComments(w io.Writer, cs []artifacts.Comment, indent string) {
	for _, c := range cs {
		who := c.Device
		if who == "" {
			who = "viewer"
		}
		fmt.Fprintf(w, "%s#%d  %s  %s  [%s]\n", indent, c.Rev, c.At.Local().Format("2006-01-02 15:04"), who, c.Status)
		for _, line := range strings.Split(c.Text, "\n") {
			fmt.Fprintf(w, "%s    %s\n", indent, line)
		}
	}
}

// revURL validates rev against the artifact and returns its direct URL.
func (e artifactEnv) revURL(id string, rev int) (string, error) {
	m, err := e.store().Get(id)
	if err != nil {
		return "", err
	}
	if len(m.Revs) == 0 {
		return "", fmt.Errorf("artifact %s has no revisions (its metadata is damaged; remove it with `agentctl artifact rm %s`)", id, id)
	}
	if rev > 0 && !m.HasRev(rev) {
		return "", fmt.Errorf("artifact %s has no revision %d (have %d..%d)", id, rev, m.Revs[0].N, m.Latest())
	}
	base, running := e.serveURL()
	u := artifactDirectURL(base, id, rev)
	if !running {
		e.notRunningNote(u)
	}
	return u, nil
}

func (e artifactEnv) url(w io.Writer, id string, rev int) error {
	u, err := e.revURL(id, rev)
	if err != nil {
		return err
	}
	fmt.Fprintln(w, u)
	return nil
}

func (e artifactEnv) open(w io.Writer, id string, rev int) error {
	u, err := e.revURL(id, rev)
	if err != nil {
		return err
	}
	if err := e.openBrowser(u); err != nil {
		return fmt.Errorf("opening browser (the URL is %s): %w", u, err)
	}
	fmt.Fprintf(w, "Opened %s\n", u)
	return nil
}

func (e artifactEnv) pin(w io.Writer, id string, pin bool) error {
	m, err := e.store().Pin(id, pin)
	if err != nil {
		return err
	}
	verb := "Pinned"
	if !pin {
		verb = "Unpinned"
	}
	fmt.Fprintf(w, "%s %s (%s)\n", verb, m.ID, m.Title)
	return nil
}

func (e artifactEnv) rm(w io.Writer, id string, yes bool) error {
	m, err := e.store().Get(id)
	if err != nil {
		return err
	}
	if !yes {
		if !e.interactive() {
			return fmt.Errorf("refusing to remove %s (%q) without --yes: not a terminal", id, m.Title)
		}
		fmt.Fprintf(e.errw, "Remove artifact %s (%q, %d revisions)? [y/N] ", id, m.Title, len(m.Revs))
		line, _ := bufio.NewReader(e.stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(line)); a != "y" && a != "yes" {
			return errors.New("aborted")
		}
	}
	if _, err := e.store().Remove(id); err != nil {
		return err
	}
	fmt.Fprintf(w, "Removed %s (%s)\n", id, m.Title)
	return nil
}

func (e artifactEnv) prune(w io.Writer, olderThan string, dryRun bool) error {
	d := artifacts.DefaultRetention
	if olderThan != "" {
		p, err := sessions.ParseSince(olderThan)
		if err != nil {
			return fmt.Errorf("--older-than: %w", err)
		}
		d = p
	}
	gone, err := e.store().Prune(d, dryRun)
	if err != nil {
		return err
	}
	verb := "Pruned"
	if dryRun {
		verb = "Would prune"
	}
	for _, m := range gone {
		fmt.Fprintf(w, "%s %s  %s  (updated %s)\n", verb, m.ID, m.Title, relTime(m.Updated, e.now()))
	}
	if len(gone) == 0 {
		fmt.Fprintln(w, "Nothing to prune.")
	}
	return nil
}

func writeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Seams for artifactOrigin, replaced in tests.
var (
	artifactPane      = func() string { return os.Getenv("TMUX_PANE") }
	artifactWindowFor = tmux.WindowIDForPane
	artifactSessionOf = tmux.SessionOfWindow
	artifactReadWin   = windowstate.Read
	artifactCwd       = os.Getwd
)

// artifactOrigin describes the caller. Inside tmux it follows TMUX_PANE to
// the window and its windowstate, the trusted source `status whoami` uses
// (never the inherited AGENTCTL_WINDOW_ID). A plain shell gets only the
// project, from the cwd, with the agent and window left blank. The project is
// the worktree-aware root, so a worktree's artifacts group under the main
// project.
func artifactOrigin() artifacts.Origin {
	var o artifacts.Origin
	dir, _ := artifactCwd()
	if pane := artifactPane(); pane != "" {
		if wid := artifactWindowFor(pane); wid != "" {
			o.WindowID = wid
			o.Session = artifactSessionOf(wid)
			if ws, err := artifactReadWin(wid); err == nil {
				o.Agent, o.SessionID, o.Branch, o.Account = ws.AgentName, ws.SessionID, ws.Branch, ws.Account
				if ws.WorkDir != "" {
					dir = ws.WorkDir
				}
			}
		}
	}
	if dir != "" {
		o.ProjectDir = prompttemplate.ProjectRoot(dir)
		o.Project = filepath.Base(o.ProjectDir)
	}
	return o
}

// artifactServeURL builds the base URL of the PWA from the serve config and
// reports whether serve is running (a control-socket probe, no network).
func artifactServeURL() (string, bool) { return artifacturl.Serve() }

// serveBaseURL is artifacturl.BaseURL.
func serveBaseURL(cfg config.ServeConfig, osHostname string) string {
	return artifacturl.BaseURL(cfg, osHostname)
}

// openInBrowser opens u with the platform opener without waiting for it.
func openInBrowser(u string) error { return artifacturl.OpenInBrowser(u) }

// pruneArtifacts is the housekeeping hook: it removes unpinned artifacts not
// updated within DefaultRetention. Never fatal.
var pruneArtifacts = func() {
	gone, err := artifacts.New().Prune(artifacts.DefaultRetention, false)
	if err != nil {
		logging.Log("artifacts prune: %v", err)
		return
	}
	if len(gone) > 0 {
		logging.Log("artifacts prune: removed %d", len(gone))
	}
}
