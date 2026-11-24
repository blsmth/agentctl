package main

import (
	"os"
	"testing"
)

// maxClaudeMDBytes is the hard ceiling for CLAUDE.md, which is loaded into
// every agent session in this repo.
const maxClaudeMDBytes = 16384

func TestClaudeMDSize(t *testing.T) {
	info, err := os.Stat("CLAUDE.md")
	if err != nil {
		t.Fatalf("stat CLAUDE.md: %v", err)
	}
	if info.Size() > maxClaudeMDBytes {
		t.Fatalf("CLAUDE.md is %d bytes (limit %d). It is loaded into every agent session: keep one line per package and move details into docs/ (see docs/README.md, e.g. docs/package-reference.md and docs/behavior-notes.md).", info.Size(), maxClaudeMDBytes)
	}
}
