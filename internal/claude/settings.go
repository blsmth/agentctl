package claude

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Permissions holds the allow/deny/ask lists from a Claude Code settings file.
type Permissions struct {
	Allow []string `json:"allow,omitempty"`
	Deny  []string `json:"deny,omitempty"`
	Ask   []string `json:"ask,omitempty"`
}

// Settings represents a .claude/settings.local.json file.
//
// Claude Code settings files can carry arbitrary top-level keys beyond
// "permissions" (e.g. "hooks", "statusLine", "env", "model"). Settings only
// models "permissions" as a typed field, but LoadSettings/SaveSettings
// preserve every other top-level key verbatim via the unexported raw field
// so that a load/save round trip never drops data agentctl doesn't
// understand.
type Settings struct {
	Permissions Permissions `json:"permissions"`

	// raw holds the full decoded top-level document, keyed by top-level
	// JSON key, as it was read from disk. It is nil when the Settings was
	// constructed directly rather than loaded from a file. SaveSettings
	// re-encodes Permissions into raw["permissions"] before marshaling so
	// unrecognized keys survive the round trip.
	raw map[string]json.RawMessage

	// order is the top-level key order as read from disk, so SaveSettings can
	// write keys back in the same order.
	order []string
}

// LoadSettings reads a Claude Code settings file.
// Returns an empty Settings (not an error) if the file doesn't exist.
func LoadSettings(path string) (*Settings, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Settings{}, nil
		}
		return nil, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	var perms Permissions
	if p, ok := raw["permissions"]; ok {
		if err := json.Unmarshal(p, &perms); err != nil {
			return nil, fmt.Errorf("decoding permissions: %w", err)
		}
	}
	return &Settings{Permissions: perms, raw: raw, order: topLevelKeyOrder(data)}, nil
}

// SaveSettings writes a Claude Code settings file, creating the directory if
// needed. Any top-level keys captured when s was loaded (other than
// "permissions") are preserved verbatim and in their original order; only
// "permissions" is re-encoded from s.Permissions. If s was never loaded from
// a file (raw is nil), the file is written containing only the "permissions"
// key.
//
// Before the first modification of an existing file, its original bytes are
// copied to path+".bak" (an existing backup is never overwritten, so it keeps
// the pre-agentctl content). The new content is written atomically via a temp
// file in the same directory and a rename, preserving the file's mode.
//
// Concurrency: this is a read-modify-write of the whole file with no locking,
// so it is last-writer-wins against Claude Code (or anything else) editing the
// same file between LoadSettings and SaveSettings. The atomic rename prevents
// torn writes, not lost updates; the .bak is the recovery path.
func SaveSettings(path string, s *Settings) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	permData, err := json.Marshal(s.Permissions)
	if err != nil {
		return fmt.Errorf("encoding permissions: %w", err)
	}

	doc := make(map[string]json.RawMessage, len(s.raw)+1)
	for k, v := range s.raw {
		doc[k] = v
	}
	doc["permissions"] = permData

	data, err := marshalOrdered(doc, s.order)
	if err != nil {
		return err
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
		if err := backupOnce(path, mode); err != nil {
			return fmt.Errorf("backing up %s: %w", path, err)
		}
	}
	return writeFileAtomic(path, data, mode)
}

// UpdateSettingsKey rewrites a single top-level key of the Claude Code
// settings file at path. fn receives the key's current raw JSON (nil and false
// when absent) and returns its replacement. Every other top-level key keeps
// its value (numbers and strings are not round-tripped through Go types) and
// its original order, though whitespace is re-indented; no other key (not
// even "permissions") is added. A missing file is treated as an empty object.
//
// The write follows SaveSettings: the original file is copied to path+".bak"
// once, the new content is written atomically, and the existing file mode is
// kept (0644 for a new file).
func UpdateSettingsKey(path, key string, fn func(old json.RawMessage, present bool) (json.RawMessage, error)) error {
	doc := map[string]json.RawMessage{}
	var order []string
	mode := os.FileMode(0o644)
	exists := false
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		exists = true
		if err := json.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("parsing %s: %w", path, err)
		}
		if doc == nil {
			doc = map[string]json.RawMessage{}
		}
		order = topLevelKeyOrder(data)
		if info, err := os.Stat(path); err == nil {
			mode = info.Mode().Perm()
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("reading %s: %w", path, err)
	}

	old, present := doc[key]
	val, err := fn(old, present)
	if err != nil {
		return err
	}
	doc[key] = val

	out, err := marshalOrdered(doc, order)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", filepath.Dir(path), err)
	}
	if exists {
		if err := backupOnce(path, mode); err != nil {
			return fmt.Errorf("backing up %s: %w", path, err)
		}
	}
	if err := writeFileAtomic(path, out, mode); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	return nil
}

// marshalOrdered encodes doc as an indented JSON object. Keys listed in order
// come first in that order; any remaining keys follow alphabetically.
func marshalOrdered(doc map[string]json.RawMessage, order []string) ([]byte, error) {
	keys := make([]string, 0, len(doc))
	seen := make(map[string]bool, len(doc))
	for _, k := range order {
		if _, ok := doc[k]; ok && !seen[k] {
			keys = append(keys, k)
			seen[k] = true
		}
	}
	var rest []string
	for k := range doc {
		if !seen[k] {
			rest = append(rest, k)
		}
	}
	sort.Strings(rest)
	keys = append(keys, rest...)

	var buf bytes.Buffer
	buf.WriteString("{")
	for i, k := range keys {
		if i > 0 {
			buf.WriteString(",")
		}
		kb, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		buf.WriteString("\n  ")
		buf.Write(kb)
		buf.WriteString(": ")
		var vb bytes.Buffer
		if err := json.Indent(&vb, doc[k], "  ", "  "); err != nil {
			return nil, fmt.Errorf("encoding %q: %w", k, err)
		}
		buf.Write(vb.Bytes())
	}
	if len(keys) > 0 {
		buf.WriteString("\n")
	}
	buf.WriteString("}\n")
	return buf.Bytes(), nil
}

// topLevelKeyOrder returns the top-level object keys of data in document order.
func topLevelKeyOrder(data []byte) []string {
	dec := json.NewDecoder(bytes.NewReader(data))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return keys
		}
		k, ok := t.(string)
		if !ok {
			return keys
		}
		keys = append(keys, k)
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}

// backupOnce copies path to path+".bak" unless a backup already exists.
func backupOnce(path string, mode os.FileMode) error {
	bak := path + ".bak"
	if _, err := os.Lstat(bak); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	orig, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return writeFileAtomic(bak, orig, mode)
}

// resolveWriteTarget returns the path that should actually be replaced when
// writing to path: the symlink target if path is a symlink (including a
// dangling one, which EvalSymlinks cannot resolve), else path itself.
func resolveWriteTarget(path string) string {
	if resolved, err := filepath.EvalSymlinks(path); err == nil {
		return resolved
	}
	if info, err := os.Lstat(path); err == nil && info.Mode()&os.ModeSymlink != 0 {
		if link, err := os.Readlink(path); err == nil {
			if filepath.IsAbs(link) {
				return link
			}
			return filepath.Join(filepath.Dir(path), link)
		}
	}
	return path
}

// writeFileAtomic writes data to path via a temp file in the same directory
// followed by a rename, so readers never observe a partial file. If path is a
// symlink, the link target is replaced and the symlink itself is preserved.
// (internal/config has an equivalent unexported helper; it is duplicated here
// rather than exported to keep this change local.)
func writeFileAtomic(path string, data []byte, mode os.FileMode) error {
	path = resolveWriteTarget(path)
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*.tmp")
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
	if _, err := tmp.Write(data); err != nil {
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
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

// RawKey returns the raw JSON for top-level key `key` as it was read from
// disk, and whether it was present at all. Callers that need typed access to
// a key Settings doesn't model directly (e.g. "statusLine") use this instead
// of adding another typed field -- adding typed fields for every top-level
// key would reintroduce the exact drop-unknown-keys risk the raw passthrough
// exists to prevent (see the Settings doc comment).
func (s *Settings) RawKey(key string) (json.RawMessage, bool) {
	if s.raw == nil {
		return nil, false
	}
	v, ok := s.raw[key]
	return v, ok
}

// SetRawKey marshals value and stores it under top-level key `key`, to be
// preserved verbatim by the next SaveSettings call. It does not touch
// "permissions", which SaveSettings always re-derives from s.Permissions.
func (s *Settings) SetRawKey(key string, value interface{}) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("marshaling %q: %w", key, err)
	}
	if s.raw == nil {
		s.raw = make(map[string]json.RawMessage, 1)
	}
	s.raw[key] = data
	return nil
}

// DeleteRawKey removes top-level key `key` so the next SaveSettings call
// omits it entirely. A no-op if the key isn't present.
func (s *Settings) DeleteRawKey(key string) {
	if s.raw != nil {
		delete(s.raw, key)
	}
}

// ProjectSettingsPath returns the path to .claude/settings.local.json for a given
// project root directory.
func ProjectSettingsPath(projectRoot string) string {
	return filepath.Join(projectRoot, ".claude", "settings.local.json")
}

// parsePermission splits a permission like "Bash(git *)" into type="Bash" and content="git *".
// Returns empty strings if the format doesn't match.
func parsePermission(perm string) (permType, content string) {
	idx := strings.Index(perm, "(")
	if idx == -1 || !strings.HasSuffix(perm, ")") {
		return "", ""
	}
	return perm[:idx], perm[idx+1 : len(perm)-1]
}

// PermissionCovers checks if a broad permission pattern covers a more specific one.
// For example, Bash(git *) covers Bash(git log:*) because "git log:" starts with "git ".
// Also handles: Bash(just *) covering Bash(just:*) — the command word matches.
func PermissionCovers(broad, specific string) bool {
	if broad == specific {
		return true
	}

	broadType, broadContent := parsePermission(broad)
	specType, specContent := parsePermission(specific)

	if broadType == "" || specType == "" || broadType != specType {
		return false
	}

	// For path patterns ending with /**: check path prefix
	if strings.HasSuffix(broadContent, "/**") {
		prefix := strings.TrimSuffix(broadContent, "**")
		return strings.HasPrefix(specContent, prefix)
	}

	// For "cmd *" patterns (e.g. "git *"):
	// Extract the command word and check if the specific starts with it.
	// This covers both "git log:*" (starts with "git ") and "git:*" (starts with "git").
	if strings.HasSuffix(broadContent, " *") {
		cmdWord := strings.TrimSuffix(broadContent, " *")
		// Specific must be exactly the command word, or the word followed by a
		// space or colon. A different command that merely shares the prefix
		// (e.g. "gitk" vs "git *") is NOT covered.
		return specContent == cmdWord ||
			strings.HasPrefix(specContent, cmdWord+" ") ||
			strings.HasPrefix(specContent, cmdWord+":")
	}

	// Generic trailing wildcard. Intentionally a plain prefix match: an
	// explicit rule like "foo*" asks for every command starting with "foo".
	if strings.HasSuffix(broadContent, "*") {
		prefix := strings.TrimSuffix(broadContent, "*")
		return strings.HasPrefix(specContent, prefix)
	}

	return false
}
