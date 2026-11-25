package artifactpage

import (
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/artifacts"
)

func TestContentType(t *testing.T) {
	for name, want := range map[string]string{
		"a.HTML": "text/html; charset=utf-8", "x.md": "text/plain; charset=utf-8",
		"p.png": "image/png", "noext": "application/octet-stream", "a.exe": "application/octet-stream",
	} {
		if got := ContentType(name); got != want {
			t.Errorf("ContentType(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestRender(t *testing.T) {
	tests := []struct {
		name, kind, rel, src string
		want                 []string
		not                  []string
	}{
		{"markdown", artifacts.KindMarkdown, "r.md", "# Hi\n\n| a | b |\n|---|---|\n| 1 | 2 |\n", []string{`class="markdown"`, "<h1", "<table>", "@media print"}, []string{"mermaid@11"}},
		{"mermaid", artifacts.KindMarkdown, "r.md", "```mermaid\ngraph TD\n```\n", []string{"mermaid@11"}, nil},
		{"text escaped", artifacts.KindText, "n.txt", "<b>x</b>", []string{`class="text"`, "&lt;b&gt;x&lt;/b&gt;"}, []string{"<b>x"}},
		{"csv", artifacts.KindText, "d.csv", "a,b\n1,<2>\n", []string{`class="csv"`, "<thead>", "&lt;2&gt;"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Render(tt.kind, tt.rel, "T", []byte(tt.src))
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range tt.want {
				if !strings.Contains(string(out), w) {
					t.Errorf("missing %q", w)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(string(out), n) {
					t.Errorf("unexpected %q", n)
				}
			}
		})
	}
}

func TestCSVRowCap(t *testing.T) {
	var b strings.Builder
	for range MaxCSVRows + 5 {
		b.WriteString("x,y\n")
	}
	if got := string(renderCSV([]byte(b.String()))); !strings.Contains(got, "Showing the first") {
		t.Error("no truncation note")
	}
}

func TestImagePage(t *testing.T) {
	out, err := ImagePage("A <pic>", "dir/my pic.png")
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.Contains(s, `src="my%20pic.png?raw=1"`) || strings.Contains(s, "<pic>") {
		t.Errorf("page = %s", s)
	}
}

func TestErrorPage(t *testing.T) {
	if got := string(ErrorPage("<gone>")); !strings.Contains(got, "&lt;gone&gt;") {
		t.Errorf("not escaped: %s", got)
	}
}
