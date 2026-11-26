// Package context provides a file-backed key/value store for sharing context
// artifacts between agents. Each entry is stored as a markdown file with YAML
// frontmatter in ~/.local/share/agentctl/context/ (or the instance-scoped equivalent).
package context

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/blsmth/agentctl/internal/config"
	"github.com/blsmth/agentctl/internal/logging"
	"gopkg.in/yaml.v3"
)

// Entry is a single item in the context store.
type Entry struct {
	Key string // derived from filename (not in frontmatter)
	// Agent is legacy metadata from older binaries, where it scoped injection.
	// It is still read and displayed but nothing uses it to select entries.
	Agent     string    `yaml:"agent,omitempty"`
	Content   string    // body below frontmatter (not in frontmatter)
	Tags      []string  `yaml:"tags,omitempty"`
	Timestamp time.Time // derived from file mtime (not in frontmatter)
	// Used is when `dispatch --prompt-key` last launched with this entry.
	// Zero means never used (including every entry an older binary wrote).
	// Set clears it: a rewritten entry is new content.
	Used time.Time `yaml:"used,omitempty"`
}

// frontmatter holds the YAML-serializable fields of an entry.
type frontmatter struct {
	Agent string    `yaml:"agent,omitempty"`
	Tags  []string  `yaml:"tags,omitempty"`
	Used  time.Time `yaml:"used,omitempty"`
}

var (
	mu        sync.Mutex
	storePath string // directory path
)

// Init sets the store path to the context directory. Must be called once at
// startup. Creates the directory if it doesn't exist. If the legacy JSON file
// exists and the new directory does not, entries are auto-migrated.
func Init() {
	data, err := config.DataDir()
	if err != nil {
		storePath = fallbackDir()
		if err := os.MkdirAll(storePath, 0o700); err != nil {
			logging.Log("context store: failed to create fallback dir %s: %v", storePath, err)
		}
		return
	}
	storePath = filepath.Join(data, "context")
	// One-time move from the pre-XDG-split location (the config dir).
	if cfg, cerr := config.GlobalDir(); cerr == nil {
		storePath = config.MigrateLegacy(filepath.Join(cfg, "context"), storePath)
		migrate(filepath.Join(cfg, "context-store.json"), storePath)
	}
	// The directory is created lazily by the first write (see Set).
	logging.Debug("context store: dir=%s", storePath)
}

// fallbackDir is the per-user store location used when no config directory can
// be resolved. It lives under os.TempDir and is keyed by uid so users on a
// shared machine do not collide or read each other's entries.
func fallbackDir() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("agentctl-context-%d", os.Getuid()))
}

// Dir returns the resolved store directory path (set by Init).
func Dir() string { return storePath }

// SetPathForTest overrides the store path to a directory. Only for use in tests.
func SetPathForTest(path string) {
	storePath = path
	_ = os.MkdirAll(path, 0o755)
}

// validateKey checks that a key is a valid filename component.
func validateKey(key string) error {
	if key == "" {
		return fmt.Errorf("context key must not be empty")
	}
	if strings.HasPrefix(key, ".") {
		return fmt.Errorf("context key must not start with '.': %q", key)
	}
	if strings.ContainsAny(key, "/\\") {
		return fmt.Errorf("context key must not contain '/' or '\\': %q", key)
	}
	return nil
}

// readEntry reads a single markdown entry from path. Key and Timestamp are
// derived from the filename and file mtime respectively.
func readEntry(path string) (Entry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Entry{}, err
	}

	info, err := os.Stat(path)
	if err != nil {
		return Entry{}, fmt.Errorf("stat context entry: %w", err)
	}

	base := filepath.Base(path)
	key := strings.TrimSuffix(base, ".md")

	var fm frontmatter
	content := string(data)

	// Parse frontmatter if present.
	if strings.HasPrefix(content, "---\n") {
		end := strings.Index(content[4:], "\n---\n")
		if end >= 0 {
			yamlBlock := content[4 : 4+end]
			if err := yaml.Unmarshal([]byte(yamlBlock), &fm); err != nil {
				return Entry{}, fmt.Errorf("parse frontmatter for %q: %w", key, err)
			}
			content = content[4+end+5:] // skip past closing "---\n"
		}
	}

	return Entry{
		Key:       key,
		Agent:     fm.Agent,
		Content:   content,
		Tags:      fm.Tags,
		Timestamp: info.ModTime(),
		Used:      fm.Used,
	}, nil
}

// writeEntry writes an entry as a markdown file with optional YAML frontmatter.
func writeEntry(key string, e Entry) error {
	if err := validateKey(key); err != nil {
		return err
	}

	var buf strings.Builder
	fm := frontmatter{Agent: e.Agent, Tags: e.Tags, Used: e.Used}

	// Only write frontmatter if there's something to write.
	if fm.Agent != "" || len(fm.Tags) > 0 || !fm.Used.IsZero() {
		yamlBytes, err := yaml.Marshal(&fm)
		if err != nil {
			return fmt.Errorf("marshal frontmatter for %q: %w", key, err)
		}
		buf.WriteString("---\n")
		buf.Write(yamlBytes)
		buf.WriteString("---\n")
	}

	buf.WriteString(e.Content)

	path := filepath.Join(storePath, key+".md")
	if err := os.MkdirAll(storePath, 0o700); err != nil {
		return fmt.Errorf("create context dir: %w", err)
	}
	if err := writeFileAtomic(path, []byte(buf.String()), 0o600); err != nil {
		return fmt.Errorf("write context entry %q: %w", key, err)
	}
	logging.Debug("context store: wrote %s", path)
	return nil
}

// Set writes or overwrites an entry. Key is set from the key parameter.
// Timestamp is derived from file mtime after writing.
func Set(key string, e Entry) error {
	mu.Lock()
	defer mu.Unlock()

	e.Key = key
	e.Used = time.Time{}
	return writeEntry(key, e)
}

// Get retrieves an entry by key. Returns (zero, false, nil) if not found.
func Get(key string) (Entry, bool, error) {
	mu.Lock()
	defer mu.Unlock()

	path := filepath.Join(storePath, key+".md")
	e, err := readEntry(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Entry{}, false, nil
		}
		return Entry{}, false, err
	}
	return e, true, nil
}

// List returns all entries sorted by timestamp (mtime, newest first).
func List() ([]Entry, error) {
	mu.Lock()
	defer mu.Unlock()

	dirEntries, err := os.ReadDir(storePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read context dir: %w", err)
	}

	var result []Entry
	for _, de := range dirEntries {
		if de.IsDir() || !strings.HasSuffix(de.Name(), ".md") {
			continue
		}
		e, err := readEntry(filepath.Join(storePath, de.Name()))
		if err != nil {
			logging.Log("context store: skipping %s: %v", de.Name(), err)
			continue
		}
		result = append(result, e)
	}

	sort.Slice(result, func(i, j int) bool {
		return result[i].Timestamp.After(result[j].Timestamp)
	})
	return result, nil
}

// Delete removes an entry by key. No error if the key does not exist.
func Delete(key string) error {
	mu.Lock()
	defer mu.Unlock()

	path := filepath.Join(storePath, key+".md")
	err := os.Remove(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("delete context entry %q: %w", key, err)
	}
	return nil
}

// Search returns entries that carry the given tag, newest first.
func Search(tag string) ([]Entry, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}

	var result []Entry
	for _, e := range all {
		for _, t := range e.Tags {
			if t == tag {
				result = append(result, e)
				break
			}
		}
	}
	return result, nil
}

// MaxEntryBytes caps an injected ticket spec. A longer spec is cut and ends
// with a visible pointer to the full text. It is a var so tests can shrink it.
var MaxEntryBytes = 8 * 1024

var ticketSpecKey = regexp.MustCompile(`^ticket-(\d+)-spec$`)

// TicketSpecNumber returns N for a key of the form ticket-<N>-spec.
func TicketSpecNumber(key string) (int, bool) {
	m := ticketSpecKey.FindStringSubmatch(key)
	if m == nil {
		return 0, false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

// TicketSpecKey is the key of issue n's spec.
func TicketSpecKey(n int) string { return fmt.Sprintf("ticket-%d-spec", n) }

// TicketSpec returns issue n's spec entry (key ticket-<n>-spec), cut at
// MaxEntryBytes, for injection into `dispatch --issue n`. It is the only
// entry ever injected into a launch. ok is false when there is none.
func TicketSpec(n int) (Entry, bool, error) {
	e, ok, err := Get(TicketSpecKey(n))
	if err != nil || !ok {
		return Entry{}, false, err
	}
	return truncateEntry(e), true, nil
}

// MarkUsed records now as the entry's last-used time without touching its
// mtime (and so its AGE). A missing entry is not an error.
func MarkUsed(key string, now time.Time) error {
	mu.Lock()
	defer mu.Unlock()

	if err := validateKey(key); err != nil {
		return err
	}
	if storePath == "" {
		return nil
	}
	path := filepath.Join(storePath, key+".md")
	e, err := readEntry(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	e.Used = now
	if err := writeEntry(key, e); err != nil {
		return err
	}
	return os.Chtimes(path, e.Timestamp, e.Timestamp)
}

// PruneUsed deletes entries whose last use is at least olderThan before now,
// returning the keys removed (or, with dryRun, that would be). Entries that
// were never used are never touched.
func PruneUsed(olderThan time.Duration, now time.Time, dryRun bool) ([]string, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, e := range all {
		if e.Used.IsZero() || now.Sub(e.Used) < olderThan {
			continue
		}
		if !dryRun {
			if err := Delete(e.Key); err != nil {
				return keys, err
			}
		}
		keys = append(keys, e.Key)
	}
	return keys, nil
}

// truncateEntry cuts e.Content to MaxEntryBytes (on a rune boundary) and
// appends a pointer to the full text.
func truncateEntry(e Entry) Entry {
	if len(e.Content) <= MaxEntryBytes {
		return e
	}
	cut := MaxEntryBytes
	for cut > 0 && !utf8.RuneStart(e.Content[cut]) {
		cut--
	}
	e.Content = strings.TrimRight(e.Content[:cut], "\n") +
		fmt.Sprintf("\n\n[truncated, full text: agentctl context get %s]", e.Key)
	return e
}

// Format renders an entry as a human-readable context block suitable for
// injection into an agent prompt.
func Format(e Entry) string {
	header := fmt.Sprintf("## Context: %s", e.Key)
	if e.Agent != "" {
		header += fmt.Sprintf(" (from %s)", e.Agent)
	}
	return header + "\n\n" + e.Content
}

// legacyEntry is the old JSON format for migration purposes.
type legacyEntry struct {
	Key       string    `json:"key"`
	Agent     string    `json:"agent,omitempty"`
	Content   string    `json:"content"`
	Tags      []string  `json:"tags,omitempty"`
	Timestamp time.Time `json:"timestamp"`
}

// migrate auto-migrates from the legacy JSON store to the new directory layout.
func migrate(legacyPath, dirPath string) {
	// Only migrate if the legacy file exists and the new dir does not.
	if _, err := os.Stat(legacyPath); os.IsNotExist(err) {
		return
	}
	if _, err := os.Stat(dirPath); err == nil {
		// Directory already exists — don't migrate.
		return
	}

	data, err := os.ReadFile(legacyPath)
	if err != nil {
		logging.Log("context store: migration read failed: %v", err)
		return
	}

	var entries map[string]legacyEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		logging.Log("context store: migration parse failed: %v", err)
		return
	}

	if err := os.MkdirAll(dirPath, 0o700); err != nil {
		logging.Log("context store: migration mkdir failed: %v", err)
		return
	}

	// Temporarily set storePath so writeEntry works.
	oldPath := storePath
	storePath = dirPath
	count := 0
	for key, le := range entries {
		e := Entry{
			Agent:   le.Agent,
			Content: le.Content,
			Tags:    le.Tags,
		}
		if err := writeEntry(key, e); err != nil {
			logging.Log("context store: migration write %q failed: %v", key, err)
			continue
		}
		// Preserve original timestamp by setting mtime.
		if !le.Timestamp.IsZero() {
			p := filepath.Join(dirPath, key+".md")
			_ = os.Chtimes(p, le.Timestamp, le.Timestamp)
		}
		count++
	}
	storePath = oldPath

	// Rename old file to .bak
	if err := os.Rename(legacyPath, legacyPath+".bak"); err != nil {
		logging.Log("context store: migration rename failed: %v", err)
	}

	logging.Log("context store: migrated %d entries from JSON to markdown files", count)
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so readers never observe a partial entry. Entries may
// hold secrets, so the mode is applied to the temp file before it is renamed
// into place (it is never briefly world-readable).
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".entry-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(mode); err != nil {
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	return nil
}
