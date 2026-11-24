package hooks

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// serveEditTarget returns the path a file-editing tool call would write
// when it is inside the serve state dir (serveStateDir: devices, CA, the
// control socket, the phone-off switch), else "". It is the Write/Edit
// counterpart of checkServeTamper, by path, with a symlinked parent
// resolved; the same speed bump, not a sandbox.
func serveEditTarget(toolName string, rawInput json.RawMessage) string {
	p := editToolPath(toolName, rawInput)
	if p == "" {
		return ""
	}
	dir := serveStateDir()
	if dir == "" {
		return ""
	}
	dir = filepath.Clean(dir)
	if underDir(p, dir) {
		return p
	}
	if real, err := filepath.EvalSymlinks(filepath.Dir(p)); err == nil {
		if realDir, err := filepath.EvalSymlinks(dir); err == nil && underDir(real, realDir) {
			return p
		}
	}
	return ""
}

// underDir reports whether p is dir or inside it.
func underDir(p, dir string) bool {
	return p == dir || strings.HasPrefix(p, dir+string(filepath.Separator))
}

// serveEditDenyReason is shown to the model when it tries to write serve's
// state with a file-editing tool.
func serveEditDenyReason(path string) string {
	return fmt.Sprintf("\U0001F6D1 BLOCKED: %s is agentctl serve's state (devices, certificates, phone access). "+
		"Remote approvals depend on it; ask the user to change it themselves.", path)
}
