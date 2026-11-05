// Package mailbox implements a durable, on-disk message store for
// inter-agent messaging (see docs/agent-messaging-design.md).
//
// Messages are written atomically (tmp+rename) as individual JSON files,
// mirroring the pattern established by internal/registry and
// internal/windowstate. Layout:
//
//	.agentctl/inbox/<windowID>/<msgID>.json        (pending)
//	.agentctl/inbox/<windowID>/read/<msgID>.json   (delivered)
//
// This package only implements the data layer: writing, listing, marking
// read, and pruning. It does not deliver messages (inject them into a tmux
// pane) -- that is the monitor's job, deliberately kept out of this
// package so it has no tmux dependency.
package mailbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/blsmth/agentctl/internal/ids"
	"github.com/blsmth/agentctl/internal/logging"
	"github.com/blsmth/agentctl/internal/paths"
	"github.com/blsmth/agentctl/internal/safefs"
)

// MaxHops caps the number of hops (reply chain length) a message may carry.
// docs/agent-messaging-design.md calls out reply loops as a real risk (A acks
// B's done, B acks A's ack, forever); Enqueue enforces this ceiling so no
// auto-emitted reply chain can run away.
const MaxHops = 8

// Known Kind values. Enqueue rejects any other value.
const (
	KindDone     = "done"
	KindBlocked  = "blocked"
	KindFYI      = "fyi"
	KindQuestion = "question"
)

var validKinds = map[string]bool{
	KindDone:     true,
	KindBlocked:  true,
	KindFYI:      true,
	KindQuestion: true,
}

// Message is one inter-agent mailbox message.
type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`       // sender window ID, may be empty
	To        string    `json:"to"`         // recipient window ID
	ToSession string    `json:"to_session"` // recipient session UUID at enqueue time
	Kind      string    `json:"kind"`       // done|blocked|fyi|question
	Subject   string    `json:"subject,omitempty"`
	Body      string    `json:"body"`
	ReplyTo   string    `json:"reply_to,omitempty"`
	Hops      int       `json:"hops"`
	Created   time.Time `json:"created"`
}

// dirOverride lets tests redirect the mailbox root away from the real
// runtime dir.
var dirOverride string

// SetDirForTest overrides the mailbox root directory. Only for use in tests.
func SetDirForTest(path string) {
	dirOverride = path
}

// resolveDir returns the mailbox root directory (the parent of every
// per-window inbox). See dirs.
func resolveDir() string {
	_, inbox := dirs()
	return inbox
}

// dirs returns the mailbox root (inbox) and the directory above it from
// which no path component may be a symlink (safeRoot): the runtime dir in
// normal operation. The runtime dir lives inside the user's repository, and
// a `git pull` can replace an ignored directory there with a committed
// symlink, so every read, write, move and delete below checks the chain
// from safeRoot down first (see safefs).
//
// Without paths.Init the mailbox falls back to a per-user 0700 directory
// under os.TempDir (safefs.PrivateTempDir) rather than panicking; if even
// that is unavailable both results are "" and operations fail.
func dirs() (safeRoot, inbox string) {
	if dirOverride != "" {
		return dirOverride, dirOverride
	}
	if dir := paths.InboxDir(); dir != "" {
		return paths.RuntimeDir(), dir
	}
	tmp, err := safefs.PrivateTempDir()
	if err != nil {
		return "", ""
	}
	return tmp, filepath.Join(tmp, "inbox")
}

// checkDir verifies dir (inside the mailbox) is reachable without following
// a symlink. A missing dir wraps os.ErrNotExist.
func checkDir(dir string) error {
	safeRoot, inbox := dirs()
	if inbox == "" {
		return fmt.Errorf("mailbox: no mailbox directory")
	}
	return safefs.CheckDir(safeRoot, dir)
}

// sanitize makes windowID safe to use as a directory name. Window IDs like
// "@41" are already filename-safe on every platform we target; this only
// guards against a stray path separator.
func sanitize(windowID string) string {
	return strings.ReplaceAll(windowID, string(filepath.Separator), "_")
}

// pendingDir returns (creating on demand) the pending-messages directory for
// windowID.
//
// Messages carry prompt and issue text, so the mailbox root and every inbox
// are 0700 (existing ones are tightened) and message files are 0600. This
// matters most for the os.TempDir fallback, which is shared with other users.
func pendingDir(windowID string) (string, error) {
	safeRoot, root := dirs()
	if root == "" {
		return "", fmt.Errorf("mailbox: no mailbox directory")
	}
	dir := filepath.Join(root, sanitize(windowID))
	if err := safefs.MkdirAll(safeRoot, dir, 0o700); err != nil {
		return "", fmt.Errorf("creating inbox dir for %s: %w", windowID, err)
	}
	for _, d := range []string{root, dir} {
		// Through an O_NOFOLLOW descriptor: never chmod a symlink's target.
		if err := safefs.ChmodDir(d, 0o700); err != nil {
			return "", fmt.Errorf("restricting inbox dir %s: %w", d, err)
		}
	}
	return dir, nil
}

// readDir returns the delivered-messages directory path for windowID,
// without creating it (MarkRead creates it lazily on first move).
func readDir(windowID string) string {
	return filepath.Join(resolveDir(), sanitize(windowID), "read")
}

// newMessageID generates a message ID that sorts chronologically as a plain
// string. This is required, not cosmetic: delivery is FIFO, and Pending/Read
// sort by filename. A bare UUID (ids.NewUUID()) does NOT sort by time --
// do not "simplify" this to one, or FIFO ordering silently breaks.
//
// The timestamp is RFC3339Nano-derived but with colons stripped (filenames
// on some platforms reject ':') and the fractional-second field zero-padded
// to a fixed width so lexicographic and chronological order agree. An 8-hex
// suffix (the first 8 characters of ids.NewUUID()) is appended purely for
// collision safety within the same nanosecond -- it is not what makes this
// sortable, the timestamp prefix is.
func newMessageID() string {
	ts := time.Now().UTC().Format("20060102T150405.000000000Z")
	suffix := ids.NewUUID()[:8]
	return ts + "-" + suffix
}

// writeMessage atomically writes m to path (temp+rename, mode 0600),
// refusing a symlink anywhere from the mailbox's safe root down.
func writeMessage(path string, m Message) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}
	safeRoot, _ := dirs()
	if err := safefs.WriteFile(safeRoot, path, data, 0o600); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}
	return nil
}

// Enqueue validates m and durably writes it to To's pending inbox, assigning
// ID and Created if unset. Returns the assigned ID.
func Enqueue(m Message) (string, error) {
	if m.To == "" {
		return "", fmt.Errorf("mailbox: To is required")
	}
	if !validKinds[m.Kind] {
		return "", fmt.Errorf("mailbox: unknown kind %q (want one of done, blocked, fyi, question)", m.Kind)
	}
	if m.Hops > MaxHops {
		return "", fmt.Errorf("mailbox: hops %d exceeds MaxHops (%d)", m.Hops, MaxHops)
	}

	if m.ID == "" {
		m.ID = newMessageID()
	}
	if m.Created.IsZero() {
		m.Created = time.Now()
	}

	dir, err := pendingDir(m.To)
	if err != nil {
		return "", err
	}
	if err := writeMessage(filepath.Join(dir, m.ID+".json"), m); err != nil {
		return "", err
	}
	return m.ID, nil
}

// listMessages reads every *.json file directly in dir (never recursing --
// this is what keeps Pending from also returning the read/ subdirectory's
// contents), skipping any file that fails to read or parse. A single
// corrupt/unparseable file must never wedge the rest of the mailbox.
//
// A symlinked inbox dir is an error and symlinked message files are
// skipped: messages are injected into agent prompts, so they must come from
// files agentctl wrote, not from wherever a planted link points.
func listMessages(dir string) ([]Message, error) {
	if err := checkDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading inbox dir %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("reading inbox dir %s: %w", dir, err)
	}

	var out []Message
	for _, e := range entries {
		if !isMessageFile(e) {
			continue
		}
		path := filepath.Join(dir, e.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			logging.Warn("mailbox: skipping unreadable message %s: %v", path, err)
			continue
		}
		var m Message
		if err := json.Unmarshal(data, &m); err != nil {
			logging.Warn("mailbox: skipping corrupt message %s: %v", path, err)
			continue
		}
		out = append(out, m)
	}

	// Filenames are "<id>.json" and IDs are timestamp-prefixed (see
	// newMessageID), so sorting by ID is sorting chronologically -- that is
	// the entire reason message IDs are not bare UUIDs.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// Pending returns windowID's undelivered messages in chronological order. A
// window with no mailbox yet (never received anything) returns an empty
// slice and a nil error, not an error -- callers should not have to
// special-case a fresh mailbox.
func Pending(windowID string) ([]Message, error) {
	dir := filepath.Join(resolveDir(), sanitize(windowID))
	return listMessages(dir)
}

// Read returns windowID's already-delivered messages in chronological order.
func Read(windowID string) ([]Message, error) {
	return listMessages(readDir(windowID))
}

// PendingCount returns the number of undelivered messages in windowID's
// mailbox. It is cheaper than len(Pending(windowID)) because it never reads
// or parses a message body -- just counts directory entries -- which
// matters because the monitor calls this once per window on every throttled
// tick to surface an unread count in the chrome bar (see docs/agent-
// messaging-design.md §2's discoverability note on `agentctl wake`).
func PendingCount(windowID string) (int, error) {
	dir := filepath.Join(resolveDir(), sanitize(windowID))
	if err := checkDir(dir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading inbox dir %s: %w", dir, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading inbox dir %s: %w", dir, err)
	}
	count := 0
	for _, e := range entries {
		if !isMessageFile(e) {
			continue
		}
		count++
	}
	return count, nil
}

// MarkRead moves windowID's msgID from pending to read/. It is idempotent:
// calling it on a message that is already in read/, or on a message that
// never existed, returns nil rather than erroring -- the delivery
// loop may retry a mark-read after a crash mid-delivery.
func MarkRead(windowID, msgID string) error {
	base := filepath.Join(resolveDir(), sanitize(windowID))
	src := filepath.Join(base, msgID+".json")

	if err := checkDir(base); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("checking inbox for %s: %w", windowID, err)
	}
	if _, err := os.Lstat(src); err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("checking message %s: %w", msgID, err)
	}

	dst := readDir(windowID)
	safeRoot, _ := dirs()
	if err := safefs.MkdirAll(safeRoot, dst, 0o700); err != nil {
		return fmt.Errorf("creating read dir for %s: %w", windowID, err)
	}
	if err := os.Rename(src, filepath.Join(dst, msgID+".json")); err != nil {
		return fmt.Errorf("marking message %s read: %w", msgID, err)
	}
	return nil
}

// RemoveInbox deletes windowID's entire mailbox directory, pending messages
// and the read/ subdirectory alike. Used by `agentctl cleanup` when a
// window's dispatch worktree is being reclaimed and the window itself no
// longer exists to receive anything. It is not an error for the directory to
// already be missing.
func RemoveInbox(windowID string) error {
	root := resolveDir()
	// Deleting inside a symlinked mailbox root would delete files wherever
	// it points. (A symlink at dir itself is fine: RemoveAll removes the
	// link, not its target.)
	if err := checkDir(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("removing inbox for %s: %w", windowID, err)
	}
	dir := filepath.Join(root, sanitize(windowID))
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("removing inbox for %s: %w", windowID, err)
	}
	return nil
}

// MaxPendingAge is the hard ceiling on how long an undelivered message may
// sit in a window's pending/ directory before PruneStale deletes it
// outright. This is safe by construction: DefaultTTL (1h) already makes
// anything older than an hour undeliverable the moment a hook fires for
// that window, so a message old enough to hit MaxPendingAge was never
// getting delivered anyway. PruneStale exists because TTL expiry inside
// DrainFor is *lazy* -- it only runs when a hook fires for a still-live
// window -- so a window that closes (or whose "@N" ID tmux later recycles
// onto an unrelated session) leaves its pending mail to rot on disk
// forever, with no hook ever firing to trigger that lazy check.
const MaxPendingAge = 24 * time.Hour

// PruneStale deletes pending messages (across every window's mailbox) whose
// Created timestamp is older than maxAge, and returns the count deleted.
// Unlike Prune (which only ever touches read/), PruneStale operates
// exclusively on pending/ -- it never touches read/, that stays Prune's
// job. Pass mailbox.MaxPendingAge from the monitor's periodic sweep; tests
// may pass a different value.
func PruneStale(maxAge time.Duration) (int, error) {
	root := resolveDir()
	if err := checkDir(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading inbox root: %w", err)
	}
	windowDirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading inbox root: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	deleted := 0
	for _, wd := range windowDirs {
		if !wd.IsDir() {
			continue
		}
		dir := filepath.Join(root, wd.Name())
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			// e.IsDir() skips this window's read/ subdirectory -- pending
			// messages are the *files* directly in dir, exactly like
			// listMessages' own walk.
			if !isMessageFile(e) {
				continue
			}
			path := filepath.Join(dir, e.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var m Message
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			if m.Created.Before(cutoff) {
				if err := os.Remove(path); err == nil {
					deleted++
				}
			}
		}
	}
	return deleted, nil
}

// Prune deletes messages under read/ (across every window's mailbox) whose
// Created timestamp is older than maxAge, and returns the count deleted. It
// only ever touches read/ -- pending messages are never deleted regardless
// of age, since durability for a not-yet-delivered message is the entire
// point of this package.
func Prune(maxAge time.Duration) (int, error) {
	root := resolveDir()
	if err := checkDir(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading inbox root: %w", err)
	}
	windowDirs, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("reading inbox root: %w", err)
	}

	cutoff := time.Now().Add(-maxAge)
	deleted := 0
	for _, wd := range windowDirs {
		if !wd.IsDir() {
			continue
		}
		rd := filepath.Join(root, wd.Name(), "read")
		if safefs.IsSymlink(rd) {
			continue // never delete through a planted link
		}
		msgFiles, err := os.ReadDir(rd)
		if err != nil {
			continue // no read/ dir for this window: nothing to prune
		}
		for _, mf := range msgFiles {
			if !isMessageFile(mf) {
				continue
			}
			path := filepath.Join(rd, mf.Name())
			data, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			var m Message
			if err := json.Unmarshal(data, &m); err != nil {
				continue
			}
			if m.Created.Before(cutoff) {
				if err := os.Remove(path); err == nil {
					deleted++
				}
			}
		}
	}
	return deleted, nil
}

// isMessageFile reports whether e is a regular *.json message file. Window
// dirs from os.ReadDir carry Lstat types, so a symlinked entry is neither a
// dir nor a regular file and is skipped.
func isMessageFile(e os.DirEntry) bool {
	return e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json")
}
