package tmux

import (
	"os"
	"strings"
)

// fallbackLocale is the UTF-8 locale applied when the environment names none.
const fallbackLocale = "en_US.UTF-8"

// NeedsUTF8Locale reports whether the environment (via get) lacks a UTF-8
// locale. POSIX precedence applies: the first non-empty of LC_ALL, LC_CTYPE,
// LANG decides. In a non-UTF-8 locale tmux replaces tabs and non-ASCII bytes in
// -F output with "_", which breaks every tab-delimited format.
func NeedsUTF8Locale(get func(string) string) bool {
	for _, k := range []string{"LC_ALL", "LC_CTYPE", "LANG"} {
		if v := get(k); v != "" {
			u := strings.ToUpper(v)
			return !strings.Contains(u, "UTF-8") && !strings.Contains(u, "UTF8")
		}
	}
	return true
}

// EnsureUTF8Locale sets LC_CTYPE=en_US.UTF-8 in the process environment when
// no UTF-8 locale is in effect, so every tmux child inherits it. Long-running
// non-interactive entrypoints (launchd, detached spawns) call it before
// shelling out to tmux. It reports whether it changed the environment.
func EnsureUTF8Locale() bool {
	if !NeedsUTF8Locale(os.Getenv) {
		return false
	}
	// LC_ALL overrides LC_CTYPE, so a non-UTF-8 LC_ALL must go.
	if os.Getenv("LC_ALL") != "" {
		_ = os.Unsetenv("LC_ALL")
	}
	return os.Setenv("LC_CTYPE", fallbackLocale) == nil
}
