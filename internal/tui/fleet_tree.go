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
// never recorded a dispatcher: phone launches), and have no such parent are
// roots, in incoming order. Dispatched rows that did record a dispatcher which
// is now absent, plus any row left over because it sits in a parent cycle, go
// under a trailing "dispatched (N)" bucket; cron rows with no usable parent
// (detached or not) go under a "scheduled (N)" bucket after it. Children keep
// incoming row order, and every row is
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

	// Cron windows have no dispatcher. Those without a usable parent go under
	// their own "scheduled (N)" bucket rather than reading as orphaned
	// dispatches.
	schedRoot := func(i int) bool { return !hasParent[i] && rows[i].ScheduledBy != "" }
	// schedTree marks scheduled roots and everything below them, so the
	// dispatched bucket's cycle pass leaves their subtrees alone.
	schedTree := make([]bool, len(rows))
	var mark func(i int)
	mark = func(i int) {
		if schedTree[i] {
			return
		}
		schedTree[i] = true
		for _, c := range children[i] {
			mark(c)
		}
	}
	for i := range rows {
		if schedRoot(i) {
			mark(i)
		}
	}

	for i, r := range rows {
		if (!r.Dispatched || r.Detached) && !hasParent[i] && !schedRoot(i) {
			walk(i, 0, "", true)
		}
	}

	// bucket appends a "<label> (N)" header followed by every unvisited row
	// that pick selects, over two passes, each walked as a depth-1 child of the
	// label. Whether a top-level entry is the last is only known after walking
	// (a cycle member may be absorbed into an earlier subtree), so walk all as
	// non-last and fix up the final one afterwards. An empty bucket adds
	// nothing.
	bucket := func(label string, pick func(i, pass int) bool) {
		head := len(out)
		out = append(out, treeRow{})
		lastTop := -1
		for pass := 0; pass < 2; pass++ {
			for i := range rows {
				if visited[i] || !pick(i, pass) {
					continue
				}
				lastTop = len(out)
				walk(i, 1, "", false)
			}
		}
		if lastTop < 0 {
			out = out[:head]
			return
		}
		for j := lastTop; j < len(out); j++ {
			p := out[j].Prefix
			if j == lastTop {
				out[j].Prefix = treeLast + strings.TrimPrefix(p, treeBranch)
			} else {
				out[j].Prefix = treeGap + strings.TrimPrefix(p, treeBar)
			}
		}
		out[head] = treeRow{Label: fmt.Sprintf("%s (%d)", label, len(out)-head-1)}
	}

	// Dispatched rows with no usable parent, then anything still unreached
	// (cycles); scheduled subtrees are kept for their own bucket.
	bucket("dispatched", func(i, pass int) bool {
		if schedTree[i] {
			return false
		}
		return pass == 1 || (!hasParent[i] && rows[i].Dispatched && !rows[i].Detached)
	})
	bucket("scheduled", func(i, pass int) bool { return pass == 0 && schedRoot(i) })
	return out
}

// subtreeEnd is the index just past the last descendant of tree[i]: walk
// emits a subtree contiguously, so its descendants are the rows right after
// it that sit deeper. A bucket label ends every subtree.
func subtreeEnd(tree []treeRow, i int) int {
	j := i + 1
	for j < len(tree) && tree[j].Label == "" && tree[j].Depth > tree[i].Depth {
		j++
	}
	return j
}

// fleetSubtreeCounts tallies the rows a collapsed parent hides, by state, so
// its summary still says that a child needs the human.
type fleetSubtreeCounts struct {
	total, blocked, working, done, idle int
}

// tallySubtree counts the rows of tr (a collapsed parent's descendants).
// Shell rows count toward the total only.
func tallySubtree(tr []treeRow) fleetSubtreeCounts {
	var c fleetSubtreeCounts
	for _, t := range tr {
		if t.Label != "" {
			continue
		}
		c.total++
		if t.Row.Shell {
			continue
		}
		switch rowWeight(t.Row) {
		case fleetWeightBlocked:
			c.blocked++
		case fleetWeightWorking:
			c.working++
		case fleetWeightDone:
			c.done++
		default:
			c.idle++
		}
	}
	return c
}
