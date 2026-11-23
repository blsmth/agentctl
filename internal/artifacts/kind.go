package artifacts

import (
	"html"
	"os"
	"path"
	"regexp"
	"strings"
	"unicode"
)

func validKind(k string) bool {
	switch k {
	case KindHTML, KindMarkdown, KindImage, KindText:
		return true
	}
	return false
}

// kindForExt infers the kind from the entry's extension; "" = unknown.
func kindForExt(entry string) string {
	switch strings.ToLower(path.Ext(entry)) {
	case ".html", ".htm":
		return KindHTML
	case ".md", ".markdown":
		return KindMarkdown
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg":
		return KindImage
	case ".txt", ".log", ".json", ".csv":
		return KindText
	}
	return ""
}

const maxTitleRunes = 120

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

// inferTitle reads the HTML <title>, else the first markdown "# " heading,
// else falls back to the source file name.
func inferTitle(kind, entryPath, fallback string) string {
	if kind == KindHTML || kind == KindMarkdown {
		if data, err := os.ReadFile(entryPath); err == nil {
			head := string(data)
			if len(head) > 256<<10 {
				head = head[:256<<10]
			}
			if t := titleFrom(kind, head); t != "" {
				return t
			}
		}
	}
	return clip(fallback)
}

func titleFrom(kind, text string) string {
	if kind == KindHTML {
		if m := titleRe.FindStringSubmatch(text); m != nil {
			return clip(html.UnescapeString(m[1]))
		}
		return ""
	}
	inFence := false
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r ")
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if !inFence && strings.HasPrefix(line, "# ") {
			return clip(strings.TrimSpace(strings.Trim(line[2:], "# ")))
		}
	}
	return ""
}

// clip collapses whitespace and cuts to maxTitleRunes.
func clip(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > maxTitleRunes {
		s = string(r[:maxTitleRunes-1]) + "…"
	}
	return s
}

// slugify is the display slug of a title: lowercase letters and digits
// joined by single dashes, at most 40 characters.
func slugify(title string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(title) {
		if r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			b.WriteRune(r)
			dash = false
		} else if !dash && b.Len() > 0 {
			b.WriteByte('-')
			dash = true
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}
