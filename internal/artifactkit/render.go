package artifactkit

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"

	"github.com/blsmth/agentctl/internal/artifactbrand"
	"github.com/blsmth/agentctl/plugin/skills/artifacts/kit"
)

// Page is a rendered artifact page.
type Page struct {
	// Title is the spec's title.
	Title string
	// HTML is one self-contained document: kit.css and kit.js inlined and the
	// spec embedded as JSON.
	HTML []byte
}

// Render validates data and builds the page. An invalid spec returns Errors
// and no page.
func Render(data []byte) (Page, error) { return RenderBranded(data, nil) }

// RenderBranded is Render with a brand profile applied: a logo and firm name
// header above the page, the brand's colours and font over the kit's tokens,
// the light palette pinned, and the brand footer. A nil brand gives exactly
// Render's page.
func RenderBranded(data []byte, brand *artifactbrand.Brand) (Page, error) {
	spec, errs := parse(data)
	if errs != nil {
		return Page{}, errs
	}
	title, _ := spec["title"].(string)
	embedded, err := embedJSON(data)
	if err != nil {
		return Page{}, fmt.Errorf("embedding spec: %w", err)
	}
	var b bytes.Buffer
	if brand == nil {
		b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n<meta charset=\"utf-8\">\n")
	} else {
		attr := "data-light"
		if isDark(brand) {
			attr = "data-dark"
		}
		b.WriteString("<!doctype html>\n<html lang=\"en\" " + attr + ">\n<head>\n<meta charset=\"utf-8\">\n")
	}
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	if brand == nil {
		b.WriteString("<meta name=\"color-scheme\" content=\"light dark\">\n")
	} else {
		scheme := "light"
		if isDark(brand) {
			scheme = "dark"
		}
		b.WriteString("<meta name=\"color-scheme\" content=\"" + scheme + "\">\n")
	}
	b.WriteString("<title>" + html.EscapeString(title) + "</title>\n<style>\n")
	b.WriteString(kit.CSS)
	b.WriteString("</style>\n")
	if brand != nil {
		b.WriteString("<style id=\"brand\">\n" + brandCSS(brand) + "</style>\n")
	}
	b.WriteString("</head>\n<body>\n")
	if brand != nil {
		b.WriteString(brandCover(brand, spec))
		b.WriteString(brandHeader(brand))
	}
	b.WriteString("<main id=\"kit-main\"></main>\n")
	if brand != nil {
		b.WriteString(brandFooter(brand))
	}
	b.WriteString("<script type=\"application/json\" id=\"spec\">\n")
	b.Write(embedded)
	b.WriteString("\n</script>\n<script>\n")
	b.WriteString(kit.JS)
	b.WriteString("</script>\n</body>\n</html>\n")
	return Page{Title: title, HTML: b.Bytes()}, nil
}

// embedJSON compacts data and escapes <, > and & (and U+2028/9) as unicode
// escapes, so no string in the spec can close the script element or open an
// HTML comment. The result is the same JSON value.
func embedJSON(data []byte) ([]byte, error) {
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	json.HTMLEscape(&out, compact.Bytes())
	return out.Bytes(), nil
}
