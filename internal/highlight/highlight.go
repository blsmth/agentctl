// Package highlight provides terminal syntax highlighting via chroma.
package highlight

import (
	"bytes"
	"strings"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/formatters"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// Highlight returns ANSI-coloured content based on the filename extension.
// Falls back to plain text if the language cannot be detected or content is
// binary.  The filename is used only for extension-based language detection.
func Highlight(content string, filename string) (string, error) {
	if content == "" {
		return "", nil
	}

	// Binary content detection: null bytes indicate binary data.
	if strings.ContainsRune(content, 0) {
		return "", nil // caller will show "Binary file" message instead
	}

	// Detect lexer from filename; fall back to plain text.
	lexer := lexers.Match(filename)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	lexer = chroma.Coalesce(lexer)

	// TTY256 produces 256-colour ANSI escape sequences — wide terminal compat.
	formatter := formatters.Get("terminal256")
	if formatter == nil {
		// Absolute fallback: return content unchanged.
		return content, nil
	}

	style := styles.Get("monokai")
	if style == nil {
		style = styles.Fallback
	}

	iterator, err := lexer.Tokenise(nil, content)
	if err != nil {
		return content, nil //nolint:nilerr // fall back gracefully
	}

	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return content, nil //nolint:nilerr // fall back gracefully
	}

	return buf.String(), nil
}

// IsBinary reports whether the content appears to be binary (contains null bytes).
func IsBinary(content string) bool {
	return strings.ContainsRune(content, 0)
}
