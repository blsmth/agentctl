package servetest

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// TestTinyPDFIsWellFormed checks the cross-reference table PDF.js needs: each
// entry points at its "N 0 obj", and startxref points at the table.
func TestTinyPDFIsWellFormed(t *testing.T) {
	pdf := tinyPDF(4)
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !bytes.HasSuffix(pdf, []byte("%%EOF\n")) {
		t.Fatalf("bad header or trailer: %q", pdf)
	}
	s := string(pdf)
	tail := s[strings.LastIndex(s, "startxref\n")+len("startxref\n"):]
	xref, err := strconv.Atoi(strings.SplitN(tail, "\n", 2)[0])
	if err != nil || !strings.HasPrefix(s[xref:], "xref\n0 11\n") { // catalog, pages, 4 x (page, stream), plus the free entry
		t.Fatalf("startxref = %d (%v), does not point at the table", xref, err)
	}
	lines := strings.Split(s[xref:], "\n")
	for n := 1; n < 11; n++ {
		off, err := strconv.Atoi(strings.Fields(lines[2+n])[0])
		if err != nil || !strings.HasPrefix(s[off:], fmt.Sprintf("%d 0 obj\n", n)) {
			t.Errorf("object %d: offset %d does not point at its object", n, off)
		}
	}
	if got := strings.Count(s, "/Type /Page "); got != 4 {
		t.Errorf("pages = %d, want 4", got)
	}
}
