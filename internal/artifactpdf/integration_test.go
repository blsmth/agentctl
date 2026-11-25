package artifactpdf

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/internal/artifactbrand"
	"github.com/blsmth/agentctl/internal/artifactkit"
	"github.com/blsmth/agentctl/internal/testenv"
)

// TestRenderRealBrowser prints an html and a markdown artifact with an actual
// headless browser. It launches a real Chrome, so it runs only with
// AGENTCTL_PDF_IT=1, and is skipped when no browser is installed.
func TestRenderRealBrowser(t *testing.T) {
	if v, _ := testenv.Original("AGENTCTL_PDF_IT"); v != "1" {
		t.Skip("set AGENTCTL_PDF_IT=1 to run the real-browser test")
	}
	old := configured
	t.Cleanup(func() { configured = old })
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

// brandedLongSpec is a branded kit spec long enough for several pages.
func brandedLongSpec() string {
	var blocks []string
	for i := 0; i < 14; i++ {
		blocks = append(blocks, fmt.Sprintf(`{"type":"text","title":"Section %d","text":%q}`, i+1, strings.Repeat("Findings paragraph with some words in it. ", 60)))
	}
	return `{"kind":"comparison","title":"Q4 Review","summary":"Cover summary line","client":"Acme Corp","date":"2026-11-24","brand":"b","blocks":[` + strings.Join(blocks, ",") + `]}`
}

// TestRenderBrandedRealBrowser prints a branded kit page and checks the cover
// page, the running footer and the page numbers. Text extraction needs
// pdftotext (poppler); without it only the PDF shape is checked.
func TestRenderBrandedRealBrowser(t *testing.T) {
	if v, _ := testenv.Original("AGENTCTL_PDF_IT"); v != "1" {
		t.Skip("set AGENTCTL_PDF_IT=1 to run the real-browser test")
	}
	old := configured
	t.Cleanup(func() { configured = old })
	configured = func() string { return "" }
	if _, err := FindBrowser(); err != nil {
		t.Skipf("no browser: %v", err)
	}
	brand := &artifactbrand.Brand{Name: "b", Firm: "Acme Advisory", Footer: "© 2026 Acme Advisory · Confidential"}
	page, err := artifactkit.RenderBranded([]byte(brandedLongSpec()), brand)
	if err != nil {
		t.Fatal(err)
	}
	s := newStore(t)
	id := addFile(t, s, "r.html", string(page.HTML)).ID
	var buf bytes.Buffer
	if err := Render(context.Background(), s, id, 0, &buf); err != nil {
		t.Fatal(err)
	}
	pdf := filepath.Join(t.TempDir(), "out.pdf")
	if err := os.WriteFile(pdf, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("pdftotext"); err != nil {
		t.Skip("pdftotext not installed: cannot check the page text")
	}
	text := func(first, last int) string {
		out, err := exec.Command("pdftotext", "-layout", "-f", strconv.Itoa(first), "-l", strconv.Itoa(last), pdf, "-").Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	info, err := exec.Command("pdfinfo", pdf).Output()
	if err != nil {
		t.Skipf("pdfinfo: %v", err)
	}
	m := regexp.MustCompile(`Pages:\s+(\d+)`).FindStringSubmatch(string(info))
	if m == nil {
		t.Fatalf("no page count in %s", info)
	}
	pages, _ := strconv.Atoi(m[1])
	if pages < 3 {
		t.Fatalf("pages = %d, want a cover plus several content pages", pages)
	}
	cover := text(1, 1)
	for _, want := range []string{"Q4 Review", "Cover summary line", "Prepared for Acme Corp", "Nov 24, 2026", "Acme Advisory"} {
		if !strings.Contains(cover, want) {
			t.Errorf("cover lacks %q:\n%s", want, cover)
		}
	}
	if strings.Contains(cover, "Confidential") || strings.Contains(cover, "Page 1 of") || strings.Contains(cover, "Section 1") {
		t.Errorf("cover has a footer or content:\n%s", cover)
	}
	p2 := text(2, 2)
	for _, want := range []string{"© 2026 Acme Advisory · Confidential", fmt.Sprintf("Page 2 of %d", pages)} {
		if !strings.Contains(p2, want) {
			t.Errorf("page 2 lacks %q:\n%s", want, p2)
		}
	}
	if !strings.Contains(string(info), "Title:") || !strings.Contains(string(info), "Q4 Review") {
		t.Errorf("PDF title not the spec title:\n%s", info)
	}
	if strings.Contains(strings.ToLower(string(info)), "agentctl") {
		t.Errorf("PDF metadata names agentctl:\n%s", info)
	}
}
