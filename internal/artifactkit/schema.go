package artifactkit

// The schema mirrors kit.js SCHEMA and plugin/skills/artifacts/kit/SPEC.md.
// A change to one needs the same change in the others; the shared fixtures in
// kit/testdata keep them honest.

type ftype int

const (
	tStr     ftype = iota // string; non-empty when required
	tStrNum               // string or number
	tBool                 // boolean
	tEnum                 // one of enum
	tCur                  // 3-letter ISO currency code
	tURL                  // http(s) URL
	tStrs                 // array of strings
	tNums                 // array of numbers or null
	tRows                 // array of objects of scalar cells
	tArr                  // array of objects described by of
	tISODate              // YYYY-MM-DD or an RFC 3339 datetime
)

type field struct {
	name string
	t    ftype
	req  bool
	nul  bool // null means "unknown" and is allowed
	enum []string
	of   []field
}

var (
	tones    = []string{"info", "good", "warn", "bad"}
	colTypes = []string{"number", "percent", "currency", "duration", "date", "datetime", "text", "status"}
	kinds    = []string{"report", "dashboard", "comparison", "diagram"}
)

var topSchema = []field{
	{name: "kind", t: tEnum, req: true, enum: kinds},
	{name: "title", t: tStr, req: true},
	{name: "summary", t: tStr},
	{name: "date", t: tISODate},
	{name: "author", t: tStr},
	{name: "scope", t: tStr},
	{name: "brand", t: tStr},
	{name: "client", t: tStr},
	{name: "blocks", t: tArr, req: true},
}

// blockOrder is the closed set of block types, in SPEC.md order.
var blockOrder = []string{"summary", "kpis", "chart", "table", "text", "mermaid", "comparison", "timeline", "html", "sources"}

var blockSchemas = map[string][]field{
	"summary": {
		{name: "title", t: tStr},
		{name: "items", t: tArr, req: true, of: []field{
			{name: "tone", t: tEnum, enum: tones}, {name: "title", t: tStr}, {name: "text", t: tStr, req: true}}},
	},
	"kpis": {
		{name: "title", t: tStr},
		{name: "items", t: tArr, req: true, of: []field{
			{name: "label", t: tStr, req: true},
			{name: "value", t: tStrNum, req: true, nul: true},
			{name: "format", t: tEnum, enum: []string{"number", "percent", "currency", "duration", "text"}},
			{name: "currency", t: tCur},
			{name: "delta", t: tStrNum},
			{name: "trend", t: tEnum, enum: []string{"good", "bad", "neutral"}},
			{name: "note", t: tStr},
			{name: "breakdown", t: tArr, of: []field{
				{name: "label", t: tStr, req: true}, {name: "value", t: tStrNum, req: true, nul: true}}},
		}},
	},
	"chart": {
		{name: "title", t: tStr},
		{name: "chart", t: tEnum, req: true, enum: []string{"line", "bar", "stacked_bar", "doughnut"}},
		{name: "labels", t: tStrs, req: true},
		{name: "series", t: tArr, req: true, of: []field{
			{name: "name", t: tStr, req: true}, {name: "values", t: tNums, req: true}}},
		{name: "unit", t: tStr}, {name: "x_label", t: tStr}, {name: "y_label", t: tStr}, {name: "note", t: tStr},
		{name: "excluded", t: tStrs},
		{name: "horizontal", t: tBool}, {name: "x_axis", t: tEnum, enum: []string{"date"}},
	},
	"table": {
		{name: "title", t: tStr},
		{name: "columns", t: tArr, req: true, of: []field{
			{name: "key", t: tStr, req: true}, {name: "label", t: tStr, req: true},
			{name: "type", t: tEnum, enum: colTypes}, {name: "currency", t: tCur}}},
		{name: "rows", t: tRows, req: true},
		{name: "sortable", t: tBool},
		{name: "sort_by", t: tStr},
		{name: "sort_dir", t: tEnum, enum: []string{"asc", "desc"}},
		{name: "note", t: tStr},
	},
	"text": {{name: "title", t: tStr}, {name: "text", t: tStr, req: true}},
	"mermaid": {
		{name: "title", t: tStr}, {name: "source", t: tStr, req: true}, {name: "caption", t: tStr}},
	"comparison": {
		{name: "title", t: tStr},
		{name: "options", t: tArr, req: true, of: []field{
			{name: "name", t: tStr, req: true}, {name: "description", t: tStr},
			{name: "pros", t: tStrs}, {name: "cons", t: tStrs},
			{name: "effort", t: tStr}, {name: "recommended", t: tBool}}},
		{name: "recommendation", t: tStr},
	},
	"timeline": {
		{name: "title", t: tStr},
		{name: "items", t: tArr, req: true, of: []field{
			{name: "date", t: tStr, req: true}, {name: "title", t: tStr, req: true},
			{name: "text", t: tStr}, {name: "tone", t: tEnum, enum: tones}}},
	},
	"html": {{name: "title", t: tStr}, {name: "html", t: tStr, req: true}},
	"sources": {
		{name: "title", t: tStr},
		{name: "items", t: tArr, req: true, of: []field{
			{name: "label", t: tStr, req: true}, {name: "url", t: tURL}, {name: "note", t: tStr}}},
	},
}
