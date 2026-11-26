package artifactkit

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/blsmth/agentctl/plugin/skills/artifacts/kit"
)

const kitDir = "../../plugin/skills/artifacts/kit"

func fixtures(t *testing.T, sub string) map[string][]byte {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(kitDir, "testdata", sub, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no fixtures in testdata/%s: %v", sub, err)
	}
	out := map[string][]byte{}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		out[strings.TrimSuffix(filepath.Base(f), ".json")] = data
	}
	return out
}

type expectation struct {
	Path   string `json:"path"`
	GoOnly bool   `json:"go_only"`
}

func expectations(t *testing.T) map[string]expectation {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(kitDir, "testdata", "expect.json"))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]expectation
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func paths(errs Errors) []string {
	var out []string
	for _, e := range errs {
		out = append(out, e.Path)
	}
	return out
}

func TestValidateValidFixtures(t *testing.T) {
	for name, data := range fixtures(t, "valid") {
		t.Run(name, func(t *testing.T) {
			if errs := Validate(data); errs != nil {
				t.Errorf("want valid, got %v", errs)
			}
		})
	}
}

func TestValidateExamples(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join(kitDir, "examples", "*.json"))
	if len(files) != 4 {
		t.Fatalf("want an example per kind (4), got %v", files)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, err := os.ReadFile(f)
			if err != nil {
				t.Fatal(err)
			}
			if errs := Validate(data); errs != nil {
				t.Errorf("example must validate: %v", errs)
			}
			if _, err := Render(data); err != nil {
				t.Errorf("example must render: %v", err)
			}
		})
	}
}

func TestValidateInvalidFixtures(t *testing.T) {
	want := expectations(t)
	for name, data := range fixtures(t, "invalid") {
		t.Run(name, func(t *testing.T) {
			exp, ok := want[name]
			if !ok {
				t.Fatalf("no entry for %q in testdata/expect.json", name)
			}
			errs := Validate(data)
			if !slices.Contains(paths(errs), exp.Path) {
				t.Errorf("want an error at %q, got %v", exp.Path, errs)
			}
			if _, err := Render(data); err == nil {
				t.Error("Render accepted an invalid spec")
			}
		})
	}
	for name := range want {
		if _, err := os.Stat(filepath.Join(kitDir, "testdata", "invalid", name+".json")); err != nil {
			t.Errorf("expect.json names %q but the fixture is missing", name)
		}
	}
}

// TestKitJSAgreesWithFixtures runs kit.js over the same fixtures with node.
// CI's ubuntu runners ship node; without it the browser half is skipped.
func TestKitJSAgreesWithFixtures(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found; kit.js is not checked against the shared fixtures")
	}
	out, err := exec.Command(node, filepath.Join(kitDir, "testdata", "check.js"), filepath.Join(kitDir, "kit.js"),
		filepath.Join(kitDir, "testdata", "valid"), filepath.Join(kitDir, "testdata", "invalid")).Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			t.Fatalf("node: %v\n%s", err, ee.Stderr)
		}
		t.Fatal(err)
	}
	var got map[string][]string
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("node output: %v\n%s", err, out)
	}
	for name := range fixtures(t, "valid") {
		if p := got["valid/"+name]; len(p) != 0 {
			t.Errorf("kit.js rejects valid/%s at %v", name, p)
		}
	}
	for name, exp := range expectations(t) {
		if exp.GoOnly {
			continue
		}
		if p := got["invalid/"+name]; !slices.Contains(p, exp.Path) {
			t.Errorf("kit.js on invalid/%s: want an error at %q, got %v", name, exp.Path, p)
		}
	}
}

func TestValidateReportsEveryErrorWithPath(t *testing.T) {
	spec := `{"kind":"report","title":"T","bogus":1,"blocks":[
		{"type":"text","text":"ok"},
		{"type":"chart","chart":"bar","labels":["a","b"],"series":[{"name":"s","values":[1]}]},
		{"type":"kpis","items":[{"label":"x"}]}]}`
	errs := Validate([]byte(spec))
	for _, want := range []string{"bogus", "blocks[1].series[0].values", "blocks[2].items[0].value", "blocks"} {
		if !slices.Contains(paths(errs), want) {
			t.Errorf("missing error at %q in %v", want, errs)
		}
	}
	msg := errs.Error()
	if !strings.Contains(msg, "blocks[1].series[0].values: has 1 values but labels has 2") {
		t.Errorf("message = %s", msg)
	}
}

func TestValidateRejectsMalformedJSON(t *testing.T) {
	for name, in := range map[string]string{
		"empty":    ``,
		"garbage":  `{"kind":`,
		"array":    `[1]`,
		"trailing": `{"kind":"diagram"} {}`,
	} {
		t.Run(name, func(t *testing.T) {
			if errs := Validate([]byte(in)); len(errs) == 0 {
				t.Error("accepted")
			}
		})
	}
}

func TestDateValidation(t *testing.T) {
	cases := []struct {
		in       string
		dateOnly bool
		dateTime bool
	}{
		{"2026-11-24", true, false},
		{"2026-02-29", false, false},
		{"2028-02-29", true, false},
		{"2026-13-01", false, false},
		{"2026-11-24T09:30:00Z", false, true},
		{"2026-11-24T09:30:00.123+10:00", false, true},
		{"2026-11-24T09:30Z", false, false},
		{"2026-11-24T09:30:00", false, false},
		{"2026-11-24T24:00:00Z", false, false},
		{"2026-11-24T09:30:00+25:00", false, false},
		{"24 Nov 2026", false, false},
	}
	for _, c := range cases {
		if got := isDateOnly(c.in); got != c.dateOnly {
			t.Errorf("isDateOnly(%q) = %v", c.in, got)
		}
		if got := isDateTime(c.in); got != c.dateTime {
			t.Errorf("isDateTime(%q) = %v", c.in, got)
		}
	}
}

// TestRenderEscapesSpec: a spec string with </script><!-- must round-trip and
// the page must keep exactly two script elements.
func TestRenderEscapesSpec(t *testing.T) {
	const evil = `</script><!-- <script>alert(1)</script> & -->`
	spec := map[string]any{
		"kind": "comparison", "title": "T " + evil,
		"blocks": []any{map[string]any{"type": "text", "text": evil}},
	}
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	page, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page.HTML)

	if n := strings.Count(html, "</script>"); n != 2 {
		t.Errorf("closing script tags = %d, want 2", n)
	}
	// kit.js mentions a script tag inside a string literal; ignore the kit
	// source and count what the spec could have added.
	if n := len(regexp.MustCompile(`<script[ >]`).FindAllString(strings.Replace(html, kit.JS, "", 1), -1)); n != 2 {
		t.Errorf("opening script tags outside the kit = %d, want 2", n)
	}
	i := strings.Index(html, `id="spec">`)
	j := strings.Index(html[i:], "</script>")
	body := html[i+len(`id="spec">`) : i+j]
	if strings.ContainsAny(body, "<>&") {
		t.Errorf("embedded spec has raw <, > or &: %s", body)
	}
	var back struct {
		Title  string `json:"title"`
		Blocks []struct {
			Text string `json:"text"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal([]byte(body), &back); err != nil {
		t.Fatalf("embedded spec is not JSON: %v", err)
	}
	if back.Title != "T "+evil || back.Blocks[0].Text != evil {
		t.Errorf("round trip lost data: %+v", back)
	}
	if strings.Contains(html, "<title>T "+evil) {
		t.Error("document title is not escaped")
	}
	if page.Title != "T "+evil {
		t.Errorf("Page.Title = %q", page.Title)
	}
}

func TestRenderPageShape(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(kitDir, "examples", "report.json"))
	if err != nil {
		t.Fatal(err)
	}
	page, err := Render(data)
	if err != nil {
		t.Fatal(err)
	}
	html := string(page.HTML)
	for _, want := range []string{"<!doctype html>", `<main id="kit-main">`, `name="viewport"`, "agentctl artifact kit", "var SCHEMA"} {
		if !strings.Contains(html, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	if _, err := Render([]byte(`{"kind":"report"}`)); err == nil {
		t.Error("invalid spec rendered")
	}
}

// TestKitJSFormatters checks the percent and duration formatters never show a
// value the data does not support (99.97 is not "100%").
func TestKitJSFormatters(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found; kit.js formatters are not checked")
	}
	out, err := exec.Command(node, filepath.Join(kitDir, "testdata", "formats.js"), filepath.Join(kitDir, "kit.js")).CombinedOutput()
	if err != nil {
		t.Fatalf("node: %v\n%s", err, out)
	}
	var bad [][]any
	if err := json.Unmarshal(out, &bad); err != nil {
		t.Fatalf("node output: %v\n%s", err, out)
	}
	for _, b := range bad {
		t.Errorf("%v(%v): want %q, got %q", b[0], b[1], b[2], b[3])
	}
}

// TestKitJSIsDark runs kit.js's isDark in node against stub documents: a
// dark brand is dark even when printing, a light brand never is, and an
// unbranded page follows the device and turns light in print.
func TestKitJSIsDark(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not found")
	}
	out, err := exec.Command(node, filepath.Join(kitDir, "testdata", "isdark.js"), filepath.Join(kitDir, "kit.js")).CombinedOutput()
	if err != nil {
		t.Fatalf("isdark.js: %v\n%s", err, out)
	}
}
