package serve

import (
	"bytes"
	"embed"
	"html/template"
	"strings"
)

// webFS holds the HTML pages and the web app's static files (see assets.go).
//
//go:embed web
var webFS embed.FS

var (
	pageIndex      = page("index.html")
	pageNotPaired  = page("notpaired.html")
	pagePhoneOff   = page("phoneoff.html")
	pagePaired     = page("paired.html")
	pagePairFailed = page("pair_failed.html")
	bootstrapTmpl  = template.Must(template.New("bootstrap").Parse(string(page("bootstrap.html"))))
)

// page loads web/<name> with the shared stylesheet inlined in place of the
// /*CSS*/ placeholder, so no page needs a second (authenticated) request.
func page(name string) []byte {
	body, err := webFS.ReadFile("web/" + name)
	if err != nil {
		panic(err) // embedded at build time; a missing file is a build bug
	}
	css, err := webFS.ReadFile("web/style.css")
	if err != nil {
		panic(err)
	}
	return bytes.Replace(body, []byte("/*CSS*/"), css, 1)
}

// bootstrapPage is the data for the plain-HTTP pairing landing page.
type bootstrapPage struct {
	Host        string
	CAName      string
	Fingerprint string
}

func renderBootstrap(d bootstrapPage) ([]byte, error) {
	var b strings.Builder
	if err := bootstrapTmpl.Execute(&b, d); err != nil {
		return nil, err
	}
	return []byte(b.String()), nil
}
