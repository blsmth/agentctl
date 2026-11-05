// Package plugin embeds the agentctl Claude Code plugin tree so the binary can
// install it (and read agent templates) without a source checkout.
package plugin

import "embed"

// FS holds the plugin tree: agents/, commands/, hooks/, skills/ and
// .claude-plugin/.
//
//go:embed .claude-plugin agents commands hooks skills
var FS embed.FS
