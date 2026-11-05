package main

import (
	"fmt"
	"runtime/debug"
)

// versionString renders "agentctl <version> (<commit>, <date>)".
//
// The package vars are set by goreleaser ldflags. When they were not (a plain
// `go build` or `go install`), the fallback is the module version and vcs.*
// settings Go embeds in the binary, so such builds still report something real.
func versionString() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		info = nil
	}
	return formatVersion(version, commit, date, info)
}

// formatVersion is the testable core of versionString. Each of v, c and d is
// replaced independently by its build-info equivalent only while it still
// holds its unset default.
func formatVersion(v, c, d string, info *debug.BuildInfo) string {
	if info != nil {
		if v == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
			v = info.Main.Version
		}
		var rev, vcsTime string
		modified := false
		for _, s := range info.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
			case "vcs.time":
				vcsTime = s.Value
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
		if c == "none" && rev != "" {
			if len(rev) > 7 {
				rev = rev[:7]
			}
			if modified {
				rev += "-dirty"
			}
			c = rev
		}
		if d == "unknown" && vcsTime != "" {
			d = vcsTime
		}
	}
	return fmt.Sprintf("agentctl %s (%s, %s)", v, c, d)
}
