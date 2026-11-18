package tui

import (
	"fmt"
	"strings"

	"github.com/blsmth/agentctl/internal/fleet"
)

// Tree connector fragments. Each is three display columns wide.
const (
	treeBranch = "├─ "
	treeLast   = "└─ "
	treeBar    = "│  "
	treeGap    = "   "
)

// treeRow is one entry of a group's flattened parent/child tree, in display
// order. Either Label is set (the non-selectable "dispatched (N)" bucket
// header, Row is zero) or Row is set.
type treeRow struct {
	Row    fleet.Row
	Label  string // non-empty for the bucket header entry
	Depth  int    // 0 for roots; the bucket's direct children are depth 1
	Prefix string // connector prefix drawn before the name ("" for roots)
}

// treeOrder returns just the rows of a flattened tree, in display order.
func treeOrder(tr []treeRow) []fleet.Row {
	var out []fleet.Row
	for _, t := range tr {
		if t.Label == "" {
			out = append(out, t.Row)
		}
	}
	return out
}

// treeRows flattens one group's rows into display order.
//
// A row nests under Row.Parent when that window is another row of the same
// group (and not itself). Rows that are not dispatched, or are detached (they
// never recorded a dispatcher: phone or cron launches), and have no such
// parent are roots, in incoming order. Dispatched rows that did record a
// dispatcher which is now absent, plus any row left over because it sits in a
// parent cycle, go under a trailing "dispatched (N)" bucket. Children keep incoming row order, and every row is
// emitted exactly once.
func treeRows(rows []fleet.Row) []treeRow {
	idx := make(map[string]int, len(rows))
	for i, r := range rows {
		if r.WindowID != "" {
			if _, dup := idx[r.WindowID]; !dup {
				idx[r.WindowID] = i
			}
		}
	}
	children := make(map[int][]int)
	hasParent := make([]bool, len(rows))
	for i, r := range rows {
		p, ok := idx[r.Parent]
		if r.Parent == "" || !ok || p == i {
			continue
		}
		hasParent[i] = true
		children[p] = append(children[p], i)
	}

	visited := make([]bool, len(rows))
	var out []treeRow
	var walk func(i, depth int, anc string, last bool)
	walk = func(i, depth int, anc string, last bool) {
		visited[i] = true
		prefix := ""
		childAnc := anc
		if depth > 0 {
			if last {
				prefix, childAnc = anc+treeLast, anc+treeGap
			} else {
				prefix, childAnc = anc+treeBranch, anc+treeBar
			}
		}
		out = append(out, treeRow{Row: rows[i], Depth: depth, Prefix: prefix})
		var kids []int
		for _, c := range children[i] {
			if !visited[c] {
				kids = append(kids, c)
			}
		}
		for n, c := range kids {
			if visited[c] { // reached via an earlier sibling's subtree
				continue
			}
			walk(c, depth+1, childAnc, n == len(kids)-1)
		}
	}

	for i, r := range rows {
		if (!r.Dispatched || r.Detached) && !hasParent[i] {
			walk(i, 0, "", true)
		}
	}

	// Bucket: dispatched rows with no usable parent, then anything still
	// unreached (cycles), each walked as a depth-1 child of the label. Whether
	// a top-level entry is the last is only known after walking (a cycle member
	// may be absorbed into an earlier subtree), so walk all as non-last and fix
	// up the final one afterwards.
	head := len(out)
	out = append(out, treeRow{})
	lastTop := -1
	for pass := 0; pass < 2; pass++ {
		for i := range rows {
			if visited[i] || (pass == 0 && (hasParent[i] || !rows[i].Dispatched || rows[i].Detached)) {
				continue
			}
			lastTop = len(out)
			walk(i, 1, "", false)
		}
	}
	if lastTop < 0 {
		return out[:head]
	}
	for j := lastTop; j < len(out); j++ {
		p := out[j].Prefix
		if j == lastTop {
			out[j].Prefix = treeLast + strings.TrimPrefix(p, treeBranch)
		} else {
			out[j].Prefix = treeGap + strings.TrimPrefix(p, treeBar)
		}
	}
	out[head] = treeRow{Label: fmt.Sprintf("dispatched (%d)", len(out)-head-1)}
	return out
}
