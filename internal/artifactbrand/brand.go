// Package artifactbrand loads brand profiles for artifact pages: a directory
// <config dir>/brands/<name>/ holding a brand.json and the logo and font files
// it names. A brand is validated in full (every problem is reported with its
// field path) and its files are read into memory, so a rendered page needs
// nothing from disk.
package artifactbrand

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/blsmth/agentctl/internal/config"
)

// Size limits for the files a brand embeds in every page.
const (
	MaxLogoBytes = 256 << 10
	MaxFontBytes = 300 << 10
	maxFooterLen = 200
	maxNameLen   = 80
)

// MinAccentContrast is the WCAG AA ratio for body-size text; links and
// headings use the accent colour.
const MinAccentContrast = 4.5

// Light-palette surfaces the accent must be readable on (kit.css --bg and --card).
var accentBackgrounds = []string{"#ffffff", "#f5f7fa"}

var (
	nameRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,47}$`)
	hexRE    = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	familyRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9 _-]{0,39}$`)
)

// Error is one problem in a brand profile, located by its field path.
type Error struct {
	Path string
	Msg  string
}

func (e Error) String() string { return e.Path + ": " + e.Msg }

// Errors is the full list of problems in a brand profile.
type Errors []Error

func (es Errors) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid brand (%d error", len(es))
	if len(es) != 1 {
		b.WriteByte('s')
	}
	b.WriteString("):")
	for _, e := range es {
		b.WriteString("\n  " + e.String())
	}
	return b.String()
}

// File is an embedded asset.
type File struct {
	// Mime is the media type of Data.
	Mime string
	Data []byte
}

// DataURI is the asset as a base64 data: URI.
func (f File) DataURI() string {
	return "data:" + f.Mime + ";base64," + base64.StdEncoding.EncodeToString(f.Data)
}

// Font is a brand's typeface: a system stack, or embedded woff2 files.
type Font struct {
	// Stack is "serif", "sans" or "mono" for a system stack.
	Stack string
	// Family names an embedded face; Regular and Bold hold its woff2 data.
	Family  string
	Regular *File
	Bold    *File
}

// Colors are the brand's light-palette overrides; empty keeps the kit's value.
type Colors struct {
	Accent string
	Chart  [6]string // c1..c6
}

// Brand is a validated brand profile with its files loaded.
type Brand struct {
	// Name is the profile's directory name.
	Name string
	// Firm is the firm name shown in the header and footer.
	Firm   string
	Logo   *File
	Colors Colors
	Font   Font
	// Footer is the one-line footer text; empty when unset.
	Footer string
}

// Dir returns <config dir>/brands, resolved on every call.
func Dir() (string, error) {
	dir, err := config.GlobalDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "brands"), nil
}

// Load reads and validates the brand called name from the config dir.
func Load(name string) (Brand, error) {
	root, err := Dir()
	if err != nil {
		return Brand{}, err
	}
	return LoadFrom(root, name)
}

// List returns the names of the brand directories in the config dir, sorted.
// A missing brands directory is an empty list.
func List() ([]string, error) {
	root, err := Dir()
	if err != nil {
		return nil, err
	}
	return ListIn(root)
}

// ListIn returns the brand directory names under root, sorted.
func ListIn(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing brands: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !nameRE.MatchString(e.Name()) {
			continue
		}
		if fi, err := os.Stat(filepath.Join(root, e.Name())); err == nil && fi.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// LoadFrom reads and validates brand name under root (the brands directory).
// An unknown name returns an error that lists the available brands and says
// where to create one; an invalid profile returns Errors.
func LoadFrom(root, name string) (Brand, error) {
	if !nameRE.MatchString(name) {
		return Brand{}, fmt.Errorf("invalid brand name %q: use letters, digits, - and _ (at most 48 characters, starting with a letter or digit)", name)
	}
	dir := filepath.Join(root, name)
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return Brand{}, unknownErr(root, name)
	}
	data, err := os.ReadFile(filepath.Join(dir, "brand.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Brand{}, Errors{{"brand.json", "missing: " + filepath.Join(dir, "brand.json")}}
	}
	if err != nil {
		return Brand{}, fmt.Errorf("reading brand.json: %w", err)
	}
	b, errs := parse(dir, data)
	b.Name = name
	if len(errs) > 0 {
		return Brand{}, errs
	}
	return b, nil
}

func unknownErr(root, name string) error {
	names, _ := ListIn(root)
	avail := "none yet"
	if len(names) > 0 {
		avail = strings.Join(names, ", ")
	}
	return fmt.Errorf("unknown brand %q (available: %s); create one as %s with a brand.json",
		name, avail, filepath.Join(root, name)+string(filepath.Separator))
}

type parser struct {
	dir  string
	errs Errors
}

func (p *parser) add(path, format string, args ...any) {
	p.errs = append(p.errs, Error{path, fmt.Sprintf(format, args...)})
}

func parse(dir string, data []byte) (Brand, Errors) {
	var b Brand
	dec := json.NewDecoder(bytes.NewReader(data))
	var v any
	if err := dec.Decode(&v); err != nil {
		return b, Errors{{"brand.json", "not valid JSON: " + err.Error()}}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return b, Errors{{"brand.json", "unexpected data after the JSON object"}}
	}
	top, ok := v.(map[string]any)
	if !ok {
		return b, Errors{{"brand.json", "must be a JSON object"}}
	}
	p := &parser{dir: dir}
	p.closed(top, "", "name", "logo", "colors", "font", "footer")

	if s, ok := p.str(top, "name", "name", true); ok {
		b.Firm = s
		if len(s) > maxNameLen {
			p.add("name", "at most %d characters", maxNameLen)
		}
	}
	if s, ok := p.str(top, "logo", "logo", false); ok && s != "" {
		b.Logo = p.logo(s)
	}
	if m, ok := p.obj(top, "colors"); ok {
		b.Colors = p.colors(m)
	}
	if m, ok := p.obj(top, "font"); ok {
		b.Font = p.font(m)
	}
	if s, ok := p.str(top, "footer", "footer", false); ok {
		b.Footer = s
		if len(s) > maxFooterLen {
			p.add("footer", "at most %d characters", maxFooterLen)
		}
	}
	return b, p.errs
}

// closed reports every key of m that is not allowed.
func (p *parser) closed(m map[string]any, path string, allowed ...string) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		known := false
		for _, a := range allowed {
			known = known || a == k
		}
		if !known {
			p.add(joinPath(path, k), "unknown field (allowed: %s)", strings.Join(allowed, ", "))
		}
	}
}

func joinPath(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

// str reads a one-line string field. ok is false when absent or invalid; an
// error has been recorded for an invalid or (when required) missing value.
func (p *parser) str(m map[string]any, key, path string, required bool) (string, bool) {
	raw, present := m[key]
	if !present {
		if required {
			p.add(path, "required")
		}
		return "", false
	}
	s, isStr := raw.(string)
	switch {
	case !isStr:
		p.add(path, "expected a string")
	case strings.TrimSpace(s) == "" && required:
		p.add(path, "must not be empty")
	case strings.ContainsAny(s, "\r\n"):
		p.add(path, "must be one line")
	default:
		return s, true
	}
	return "", false
}

func (p *parser) obj(m map[string]any, key string) (map[string]any, bool) {
	raw, present := m[key]
	if !present {
		return nil, false
	}
	o, ok := raw.(map[string]any)
	if !ok {
		p.add(key, "expected an object")
	}
	return o, ok
}

func (p *parser) colors(m map[string]any) Colors {
	var c Colors
	p.closed(m, "colors", "accent", "c1", "c2", "c3", "c4", "c5", "c6")
	hex := func(key string) string {
		s, ok := p.str(m, key, "colors."+key, false)
		if !ok {
			return ""
		}
		if !hexRE.MatchString(s) {
			p.add("colors."+key, "expected a colour as #rrggbb (got %q)", s)
			return ""
		}
		return strings.ToLower(s)
	}
	if c.Accent = hex("accent"); c.Accent != "" {
		for _, bg := range accentBackgrounds {
			if r := contrast(c.Accent, bg); r < MinAccentContrast {
				p.add("colors.accent", "contrast ratio %.2f:1 against %s is below %.1f:1 (links and headings use the accent); pick a darker colour", r, bg, MinAccentContrast)
			}
		}
	}
	for i := range c.Chart {
		c.Chart[i] = hex(fmt.Sprintf("c%d", i+1))
	}
	return c
}

func (p *parser) font(m map[string]any) Font {
	var f Font
	p.closed(m, "font", "stack", "family", "regular", "bold")
	_, hasStack := m["stack"]
	_, hasFamily := m["family"]
	switch {
	case hasStack && hasFamily:
		p.add("font", "use either stack or family, not both")
		return f
	case !hasStack && !hasFamily:
		p.add("font", `needs "stack" ("serif", "sans" or "mono") or "family" with a woff2 file`)
		return f
	case hasStack:
		for _, k := range []string{"regular", "bold"} {
			if _, ok := m[k]; ok {
				p.add("font."+k, "only applies with family")
			}
		}
		if s, ok := p.str(m, "stack", "font.stack", false); ok {
			switch s {
			case "serif", "sans", "mono":
				f.Stack = s
			default:
				p.add("font.stack", `expected "serif", "sans" or "mono" (got %q)`, s)
			}
		}
		return f
	}
	if s, ok := p.str(m, "family", "font.family", false); ok {
		if familyRE.MatchString(s) {
			f.Family = s
		} else {
			p.add("font.family", "use letters, digits, spaces, - and _ (at most 40 characters)")
		}
	}
	if s, ok := p.str(m, "regular", "font.regular", true); ok {
		f.Regular = p.fontFile(s, "font.regular")
	}
	if s, ok := p.str(m, "bold", "font.bold", false); ok {
		f.Bold = p.fontFile(s, "font.bold")
	}
	return f
}

func (p *parser) fontFile(rel, path string) *File {
	if !strings.EqualFold(filepath.Ext(rel), ".woff2") {
		p.add(path, "only .woff2 fonts are supported (got %q)", rel)
		return nil
	}
	data, ok := p.readAsset(rel, path, MaxFontBytes)
	if !ok {
		return nil
	}
	if !bytes.HasPrefix(data, []byte("wOF2")) {
		p.add(path, "%s is not a woff2 font file", rel)
		return nil
	}
	return &File{Mime: "font/woff2", Data: data}
}

func (p *parser) logo(rel string) *File {
	var mime string
	switch strings.ToLower(filepath.Ext(rel)) {
	case ".svg":
		mime = "image/svg+xml"
	case ".png":
		mime = "image/png"
	default:
		p.add("logo", "only .svg and .png logos are supported (got %q)", rel)
		return nil
	}
	data, ok := p.readAsset(rel, "logo", MaxLogoBytes)
	if !ok {
		return nil
	}
	switch mime {
	case "image/png":
		if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) {
			p.add("logo", "%s is not a PNG file", rel)
			return nil
		}
	default:
		if msg := unsafeSVG(data); msg != "" {
			p.add("logo", "unsafe SVG: %s", msg)
			return nil
		}
	}
	return &File{Mime: mime, Data: data}
}

// readAsset reads a file named by brand.json: a relative path inside the
// brand directory (symlinks allowed only when they stay inside it), at most
// max bytes.
func (p *parser) readAsset(rel, path string, max int64) ([]byte, bool) {
	if filepath.IsAbs(rel) || !filepath.IsLocal(rel) || strings.Contains(rel, `\`) {
		p.add(path, "must be a relative path inside the brand directory (no .. or absolute paths): %q", rel)
		return nil, false
	}
	full := filepath.Join(p.dir, rel)
	realDir, err := filepath.EvalSymlinks(p.dir)
	if err != nil {
		p.add(path, "resolving brand directory: %v", err)
		return nil, false
	}
	real, err := filepath.EvalSymlinks(full)
	if errors.Is(err, os.ErrNotExist) {
		p.add(path, "file not found: %s", rel)
		return nil, false
	}
	if err != nil {
		p.add(path, "resolving %s: %v", rel, err)
		return nil, false
	}
	if inside, err := filepath.Rel(realDir, real); err != nil || !filepath.IsLocal(inside) {
		p.add(path, "%s is a symlink that points outside the brand directory", rel)
		return nil, false
	}
	fi, err := os.Stat(real)
	if err != nil || !fi.Mode().IsRegular() {
		p.add(path, "%s is not a regular file", rel)
		return nil, false
	}
	if fi.Size() > max {
		p.add(path, "%s is %d KB; the limit is %d KB", rel, (fi.Size()+1023)/1024, max>>10)
		return nil, false
	}
	data, err := os.ReadFile(real)
	if err != nil {
		p.add(path, "reading %s: %v", rel, err)
		return nil, false
	}
	return data, true
}

var (
	svgScriptRE   = regexp.MustCompile(`(?i)<\s*(script|foreignObject|iframe|embed|object)\b`)
	svgOnAttrRE   = regexp.MustCompile(`(?i)[\s"'/]on[a-z]+\s*=`)
	svgHrefRE     = regexp.MustCompile(`(?i)\bhref\s*=\s*["']?\s*([^"'\s>]*)`)
	svgURLRE      = regexp.MustCompile(`(?i)url\(\s*["']?\s*([^"')\s]*)`)
	svgImportRE   = regexp.MustCompile(`(?i)@import`)
	svgJSSchemeRE = regexp.MustCompile(`(?i)javascript:`)
)

// unsafeSVG returns why an SVG must not be embedded, or "".
func unsafeSVG(data []byte) string {
	s := string(data)
	if !strings.Contains(strings.ToLower(s), "<svg") {
		return "not an SVG document"
	}
	if m := svgScriptRE.FindStringSubmatch(s); m != nil {
		return "contains <" + strings.ToLower(m[1]) + ">"
	}
	if svgOnAttrRE.MatchString(s) {
		return "contains an on* event attribute"
	}
	if svgJSSchemeRE.MatchString(s) {
		return "contains a javascript: URL"
	}
	if svgImportRE.MatchString(s) {
		return "contains @import"
	}
	for _, m := range svgHrefRE.FindAllStringSubmatch(s, -1) {
		if !strings.HasPrefix(m[1], "#") {
			return "contains an external href " + fmt.Sprintf("%q", m[1])
		}
	}
	for _, m := range svgURLRE.FindAllStringSubmatch(s, -1) {
		if !strings.HasPrefix(m[1], "#") {
			return "contains an external url() reference " + fmt.Sprintf("%q", m[1])
		}
	}
	return ""
}
