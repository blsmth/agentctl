package tui

import (
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/charmbracelet/x/ansi"
)

// Render-time click zones.
//
// A view wraps each clickable span in a pair of zero-width marker sequences
// (zoneMark). After the whole frame is composed (borders, joins, split panes,
// overlays), scanZones walks the final string once, records the rectangle of
// every marked span and strips the markers. Hit-testing then asks the zone
// list what is under (x, y); nothing ever hand-computes a column or row
// offset, so a layout change cannot skew click targets.
//
// Markers are CSI sequences with a private final byte ('z'), so lipgloss and
// x/ansi measure them as zero width and carry them through Width, padding,
// truncation and joins like any other escape sequence. They never reach the
// terminal: scanZones removes every one of them.

// zoneRect is one marked span on one screen row: columns [x0, x1) of row y.
type zoneRect struct {
	id     string
	x0, x1 int
	y      int
}

// zoneList is the set of zones recorded for one frame, in marker order.
type zoneList []zoneRect

// zoneCodes interns zone ids as small integers for the marker sequences.
var zoneCodes = struct {
	sync.Mutex
	byID   map[string]int
	byCode []string
}{byID: map[string]int{}}

func zoneCode(id string) int {
	zoneCodes.Lock()
	defer zoneCodes.Unlock()
	if c, ok := zoneCodes.byID[id]; ok {
		return c
	}
	c := len(zoneCodes.byCode)
	zoneCodes.byCode = append(zoneCodes.byCode, id)
	zoneCodes.byID[id] = c
	return c
}

func zoneID(code int) string {
	zoneCodes.Lock()
	defer zoneCodes.Unlock()
	if code < 0 || code >= len(zoneCodes.byCode) {
		return ""
	}
	return zoneCodes.byCode[code]
}

// zoneMark wraps s in start/end markers for zone id. s must be a single
// line; mark multi-line content line by line.
func zoneMark(id, s string) string {
	if id == "" {
		return s
	}
	c := strconv.Itoa(zoneCode(id))
	return "\x1b[" + c + "z" + s + "\x1b[" + c + ";1z"
}

// zoneMarkLines marks every line of a multi-line block with the same id.
func zoneMarkLines(id, s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = zoneMark(id, l)
	}
	return strings.Join(lines, "\n")
}

var zoneMarkerRE = regexp.MustCompile(`\x1b\[(\d+)(;1)?z`)

// stripZones removes every zone marker from s without recording anything.
func stripZones(s string) string {
	if !strings.Contains(s, "z") {
		return s
	}
	return zoneMarkerRE.ReplaceAllString(s, "")
}

// scanZones records every marked span in the composed frame s and returns s
// with the markers removed. A span left open at the end of its line (its end
// marker was cut by truncation) closes at the line's end.
func scanZones(s string) (string, zoneList) {
	lines := strings.Split(s, "\n")
	var zones zoneList
	for y, line := range lines {
		idx := zoneMarkerRE.FindAllStringSubmatchIndex(line, -1)
		if len(idx) == 0 {
			continue
		}
		var b strings.Builder
		open := map[int]int{} // code -> start column
		var order []int
		x, prev := 0, 0
		for _, m := range idx {
			seg := line[prev:m[0]]
			b.WriteString(seg)
			x += ansi.StringWidth(seg)
			prev = m[1]
			code, _ := strconv.Atoi(line[m[2]:m[3]])
			if m[4] < 0 { // start marker
				if _, ok := open[code]; !ok {
					order = append(order, code)
				}
				open[code] = x
				continue
			}
			if x0, ok := open[code]; ok {
				if x > x0 {
					zones = append(zones, zoneRect{id: zoneID(code), x0: x0, x1: x, y: y})
				}
				delete(open, code)
			}
		}
		rest := line[prev:]
		b.WriteString(rest)
		x += ansi.StringWidth(rest)
		for _, code := range order {
			if x0, ok := open[code]; ok && x > x0 {
				zones = append(zones, zoneRect{id: zoneID(code), x0: x0, x1: x, y: y})
			}
		}
		lines[y] = b.String()
	}
	return strings.Join(lines, "\n"), zones
}

// at returns the innermost zone id under (x, y): the narrowest span that
// contains the point, so a label chip inside a row wins over the row.
func (z zoneList) at(x, y int) string {
	best, bestW := "", -1
	for _, r := range z {
		if r.y == y && x >= r.x0 && x < r.x1 {
			if w := r.x1 - r.x0; bestW < 0 || w < bestW {
				best, bestW = r.id, w
			}
		}
	}
	return best
}

// atPrefix returns the id of a zone under (x, y) whose id starts with
// prefix, or "" (used for the pane regions under nested targets).
func (z zoneList) atPrefix(x, y int, prefix string) string {
	for _, r := range z {
		if r.y == y && x >= r.x0 && x < r.x1 && strings.HasPrefix(r.id, prefix) {
			return r.id
		}
	}
	return ""
}

// find returns the first rectangle recorded for id.
func (z zoneList) find(id string) (zoneRect, bool) {
	for _, r := range z {
		if r.id == id {
			return r, true
		}
	}
	return zoneRect{}, false
}

// has reports whether any zone carries id.
func (z zoneList) has(id string) bool {
	_, ok := z.find(id)
	return ok
}
