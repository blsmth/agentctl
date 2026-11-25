// Package kit embeds the artifact kit's CSS and JS. The files in this
// directory stay the one copy: templates inline them (drift-tested) and
// `agentctl artifact render` builds pages from these embedded bytes.
package kit

import _ "embed"

// CSS is kit.css.
//
//go:embed kit.css
var CSS string

// JS is kit.js.
//
//go:embed kit.js
var JS string
