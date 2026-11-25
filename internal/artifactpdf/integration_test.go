package artifactpdf

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// TestRenderRealBrowser prints an html and a markdown artifact with an actual
// headless browser. It is skipped when none is installed (CI).
func TestRenderRealBrowser(t *testing.T) {
	configured = func() string { return "" }
	if _, err := FindBrowser(); err != nil {
		t.Skipf("no browser: %v", err)
	}
	s := newStore(t)
	cases := map[string]string{
		"html":     addFile(t, s, "r.html", "<!doctype html><title>R</title><h1>Hello</h1><p>world</p>").ID,
		"markdown": addFile(t, s, "r.md", "# Title\n\n| a | b |\n|---|---|\n| 1 | 2 |\n\n```go\nfmt.Println()\n```\n").ID,
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := Render(context.Background(), s, id, 0, &buf); err != nil {
				t.Fatal(err)
			}
			if !strings.HasPrefix(buf.String(), "%PDF-") || buf.Len() < 500 {
				t.Fatalf("not a PDF: %d bytes", buf.Len())
			}
		})
	}
}
