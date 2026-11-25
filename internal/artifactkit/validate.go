// Package artifactkit validates artifact kit specs and builds the one
// self-contained HTML page for them, so agents never edit artifact HTML.
// The contract is plugin/skills/artifacts/kit/SPEC.md; kit.js enforces the
// same rules in the browser.
package artifactkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Error is one problem in a spec, located by its JSON path.
type Error struct {
	Path string
	Msg  string
}

func (e Error) String() string { return e.Path + ": " + e.Msg }

// Errors is the full list of problems in a spec.
type Errors []Error

func (es Errors) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "invalid artifact spec (%d error", len(es))
	if len(es) != 1 {
		b.WriteByte('s')
	}
	b.WriteString("):")
	for _, e := range es {
		b.WriteString("\n  " + e.String())
	}
	return b.String()
}

// Validate checks a JSON spec and returns every problem found, or nil.
func Validate(data []byte) Errors {
	_, errs := parse(data)
	return errs
}

func parse(data []byte) (map[string]any, Errors) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, Errors{{"spec", "not valid JSON: " + err.Error()}}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, Errors{{"spec", "unexpected data after the JSON object"}}
	}
	spec, ok := v.(map[string]any)
	if !ok {
		return nil, Errors{{"spec", "must be a JSON object"}}
	}
	var errs Errors
	checkObj(spec, topSchema, "", &errs)
	blocks, _ := spec["blocks"].([]any)
	hasSources := false
	for i, b := range blocks {
		p := "blocks[" + strconv.Itoa(i) + "]"
		if m, ok := b.(map[string]any); ok && m["type"] == "sources" {
			hasSources = true
		}
		validateBlock(b, p, &errs)
	}
	kind, _ := spec["kind"].(string)
	if _, isArr := spec["blocks"].([]any); isArr && (kind == "report" || kind == "dashboard") && !hasSources {
		errs = append(errs, Error{"blocks", "a " + kind + " needs a sources block (every number must trace to a source)"})
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return spec, nil
}

func validateBlock(b any, path string, errs *Errors) {
	m, ok := b.(map[string]any)
	if !ok {
		*errs = append(*errs, Error{path, "block must be an object"})
		return
	}
	typ, _ := m["type"].(string)
	schema, known := blockSchemas[typ]
	if !known {
		*errs = append(*errs, Error{path + ".type", fmt.Sprintf("unknown block type %s (expected one of %s)", quote(m["type"]), strings.Join(blockOrder, ", "))})
		return
	}
	var local Errors
	for _, k := range sortedKeys(m) {
		if k != "type" && !hasField(schema, k) {
			local = append(local, Error{k, "unknown field"})
		}
	}
	for _, f := range schema {
		checkField(m[f.name], has(m, f.name), f, f.name, &local)
	}
	if len(local) == 0 {
		postBlock(typ, m, &local)
	}
	for _, e := range local {
		*errs = append(*errs, Error{path + "." + e.Path, e.Msg})
	}
}

func has(m map[string]any, k string) bool { _, ok := m[k]; return ok }

func hasField(schema []field, name string) bool {
	return slices.ContainsFunc(schema, func(f field) bool { return f.name == name })
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func quote(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func join(path, k string) string {
	if path == "" {
		return k
	}
	return path + "." + k
}

func checkObj(obj map[string]any, schema []field, path string, errs *Errors) {
	for _, k := range sortedKeys(obj) {
		if !hasField(schema, k) {
			*errs = append(*errs, Error{join(path, k), "unknown field"})
		}
	}
	for _, f := range schema {
		checkField(obj[f.name], has(obj, f.name), f, join(path, f.name), errs)
	}
}

var (
	curRE      = regexp.MustCompile(`^[A-Z]{3}$`)
	urlRE      = regexp.MustCompile(`^https?://`)
	dateRE     = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})$`)
	dateTimeRE = regexp.MustCompile(`^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-](\d{2}):(\d{2}))$`)
)

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func validDay(y, m, d int) bool {
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	return t.Year() == y && int(t.Month()) == m && t.Day() == d
}

// isDateOnly reports whether s is a real calendar day as YYYY-MM-DD.
func isDateOnly(s string) bool {
	m := dateRE.FindStringSubmatch(s)
	return m != nil && validDay(atoi(m[1]), atoi(m[2]), atoi(m[3]))
}

// isDateTime reports whether s is an RFC 3339 timestamp (seconds and an
// offset are required).
func isDateTime(s string) bool {
	m := dateTimeRE.FindStringSubmatch(s)
	if m == nil || !validDay(atoi(m[1]), atoi(m[2]), atoi(m[3])) {
		return false
	}
	if atoi(m[4]) >= 24 || atoi(m[5]) >= 60 || atoi(m[6]) >= 60 {
		return false
	}
	return m[9] == "" || (atoi(m[9]) < 24 && atoi(m[10]) < 60)
}

func checkField(val any, present bool, f field, path string, errs *Errors) {
	add := func(msg string) { *errs = append(*errs, Error{path, msg}) }
	if !present {
		if f.req {
			add("required")
		}
		return
	}
	if val == nil {
		switch {
		case f.nul:
		case f.req:
			add("required")
		default:
			add("null is not allowed; omit the field")
		}
		return
	}
	switch f.t {
	case tStr:
		s, ok := val.(string)
		switch {
		case !ok:
			add("expected a string")
		case f.req && strings.TrimSpace(s) == "":
			add("must not be empty")
		}
	case tStrNum:
		if _, isS := val.(string); !isS {
			if _, isN := val.(float64); !isN {
				add("expected a string or number")
			}
		}
	case tBool:
		if _, ok := val.(bool); !ok {
			add("expected true or false")
		}
	case tEnum:
		if s, ok := val.(string); !ok || !slices.Contains(f.enum, s) {
			add(fmt.Sprintf("expected one of %s (got %s)", strings.Join(f.enum, ", "), quote(val)))
		}
	case tISODate:
		if s, ok := val.(string); !ok {
			add("expected a string")
		} else if !isDateOnly(s) && !isDateTime(s) {
			add("expected an ISO date YYYY-MM-DD or an RFC 3339 datetime (got " + quote(s) + ")")
		}
	case tCur:
		if s, ok := val.(string); !ok || !curRE.MatchString(s) {
			add("expected a 3-letter ISO currency code such as USD")
		}
	case tURL:
		if s, ok := val.(string); !ok || !urlRE.MatchString(s) {
			add("expected an http(s) URL")
		}
	case tStrs:
		arr, ok := val.([]any)
		if !ok {
			add("expected an array of strings")
			return
		}
		for j, x := range arr {
			if _, ok := x.(string); !ok {
				*errs = append(*errs, Error{idx(path, j), "expected a string"})
			}
		}
	case tNums:
		arr, ok := val.([]any)
		if !ok {
			add("expected an array of numbers (null for unknown)")
			return
		}
		for j, x := range arr {
			if _, isN := x.(float64); x != nil && !isN {
				*errs = append(*errs, Error{idx(path, j), "expected a number or null"})
			}
		}
	case tRows:
		checkRows(val, path, errs)
	case tArr:
		arr, ok := val.([]any)
		if !ok {
			add("expected an array")
			return
		}
		if f.req && len(arr) == 0 {
			add("must not be empty")
		}
		if f.of != nil {
			for i, el := range arr {
				p := idx(path, i)
				o, ok := el.(map[string]any)
				if !ok {
					*errs = append(*errs, Error{p, "expected an object"})
					continue
				}
				checkObj(o, f.of, p, errs)
			}
		}
	}
}

func idx(path string, i int) string { return path + "[" + strconv.Itoa(i) + "]" }

func checkRows(val any, path string, errs *Errors) {
	arr, ok := val.([]any)
	if !ok {
		*errs = append(*errs, Error{path, "expected an array of row objects"})
		return
	}
	for j, r := range arr {
		row, ok := r.(map[string]any)
		if !ok {
			*errs = append(*errs, Error{idx(path, j), "expected an object keyed by column key"})
			continue
		}
		for _, k := range sortedKeys(row) {
			switch row[k].(type) {
			case nil, string, float64, bool:
			default:
				*errs = append(*errs, Error{idx(path, j) + "." + k, "expected a string, number, boolean or null"})
			}
		}
	}
}

// postBlock holds the cross-field rules the per-field schema cannot express.
// It runs only on a block with no per-field errors, so types are trusted.
func postBlock(typ string, b map[string]any, errs *Errors) {
	switch typ {
	case "chart":
		postChart(b, errs)
	case "table":
		postTable(b, errs)
	case "comparison":
		opts, _ := b["options"].([]any)
		if len(opts) < 2 {
			*errs = append(*errs, Error{"options", "needs at least 2 options"})
		}
		rec := 0
		for _, o := range opts {
			if m, ok := o.(map[string]any); ok && m["recommended"] == true {
				rec++
			}
		}
		if rec > 1 {
			*errs = append(*errs, Error{"options", "at most one option may be recommended"})
		}
	}
}

func postChart(b map[string]any, errs *Errors) {
	labels, _ := b["labels"].([]any)
	series, _ := b["series"].([]any)
	for i, s := range series {
		vals, _ := s.(map[string]any)["values"].([]any)
		if len(vals) != len(labels) {
			*errs = append(*errs, Error{fmt.Sprintf("series[%d].values", i), fmt.Sprintf("has %d values but labels has %d", len(vals), len(labels))})
		}
	}
	if b["chart"] != "doughnut" {
		if b["x_axis"] == "date" {
			for i, l := range labels {
				if s, _ := l.(string); !isDateOnly(s) {
					*errs = append(*errs, Error{fmt.Sprintf("labels[%d]", i), "x_axis \"date\" needs ISO dates YYYY-MM-DD (got " + quote(l) + ")"})
				}
			}
		}
		return
	}
	for _, k := range []string{"horizontal", "x_axis"} {
		if _, ok := b[k]; ok {
			*errs = append(*errs, Error{k, "a doughnut does not support this field"})
		}
	}
	if len(series) != 1 {
		*errs = append(*errs, Error{"series", "a doughnut needs exactly one series"})
		return
	}
	vals, _ := series[0].(map[string]any)["values"].([]any)
	if slices.Contains(vals, nil) {
		*errs = append(*errs, Error{"series[0].values", "a doughnut cannot contain null"})
	}
}

func postTable(b map[string]any, errs *Errors) {
	cols, _ := b["columns"].([]any)
	rows, _ := b["rows"].([]any)
	types := map[string]string{}
	for i, c := range cols {
		cm, _ := c.(map[string]any)
		key, _ := cm["key"].(string)
		if _, dup := types[key]; dup {
			*errs = append(*errs, Error{fmt.Sprintf("columns[%d].key", i), "duplicate column key " + quote(key)})
		}
		types[key], _ = cm["type"].(string)
	}
	for i, r := range rows {
		row, _ := r.(map[string]any)
		for _, k := range sortedKeys(row) {
			p := fmt.Sprintf("rows[%d].%s", i, k)
			typ, ok := types[k]
			if !ok {
				*errs = append(*errs, Error{p, "no column with this key"})
				continue
			}
			s, isStr := row[k].(string)
			switch {
			case row[k] == nil || (typ != "date" && typ != "datetime"):
			case typ == "date" && !(isStr && (isDateOnly(s) || isDateTime(s))):
				*errs = append(*errs, Error{p, "expected an ISO date YYYY-MM-DD or an RFC 3339 datetime"})
			case typ == "datetime" && !(isStr && isDateTime(s)):
				*errs = append(*errs, Error{p, "expected an RFC 3339 datetime such as 2026-11-24T09:30:00Z"})
			}
		}
	}
	if sb, ok := b["sort_by"].(string); ok {
		if _, found := types[sb]; !found {
			*errs = append(*errs, Error{"sort_by", "no column with this key"})
		}
	}
}
