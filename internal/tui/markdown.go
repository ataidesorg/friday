package tui

import (
	"regexp"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

var mdListRe = regexp.MustCompile(`^(\s*)([-*+]|\d+\.)\s+(.*)$`)

func (cs chatStyles) paintMarkdownLine(text string, inner int, selected bool) string {
	mark := ""
	if selected {
		mark = cs.glyph("▶", cs.accent) + " "
	}
	avail := max(1, inner-lipgloss.Width(mark))
	trim := strings.TrimSpace(text)
	switch {
	case trim == "---" || trim == "***" || trim == "___":
		return mark + cs.dimText(strings.Repeat("─", avail))
	case strings.HasPrefix(trim, "> "):
		body := cs.paintInline(strings.TrimPrefix(trim, "> "))
		return mark + cs.dimText("│ ") + body
	}
	if m := mdListRe.FindStringSubmatch(text); m != nil {
		bullet := "•"
		if strings.HasSuffix(m[2], ".") {
			bullet = m[2]
		}
		prefix := m[1] + cs.dimText(bullet+" ")
		bodyW := max(1, avail-lipgloss.Width(m[1])-lipgloss.Width(bullet)-1)
		var rows []string
		for i, row := range wrapText(m[3], bodyW) {
			if i == 0 {
				rows = append(rows, mark+prefix+cs.paintInline(row))
				continue
			}
			rows = append(rows, mark+strings.Repeat(" ", lipgloss.Width(m[1])+lipgloss.Width(bullet)+1)+cs.paintInline(row))
		}
		return strings.Join(rows, "\n")
	}
	var rows []string
	for i, row := range wrapText(text, avail) {
		lead := mark
		if i > 0 {
			lead = strings.Repeat(" ", lipgloss.Width(mark))
		}
		rows = append(rows, lead+cs.paintInline(row))
	}
	if len(rows) == 0 {
		return mark
	}
	return strings.Join(rows, "\n")
}

func (cs chatStyles) paintInline(s string) string {
	type span struct {
		kind string // "code" | "bold" | "em" | "plain"
		text string
	}
	var spans []span
	rest := s
	for rest != "" {
		code := strings.IndexByte(rest, '`')
		bold := strings.Index(rest, "**")
		star := indexEm(rest)
		next, kind := -1, ""
		if code >= 0 {
			next, kind = code, "code"
		}
		if bold >= 0 && (next < 0 || bold < next) {
			next, kind = bold, "bold"
		}
		if star >= 0 && (next < 0 || star < next) {
			next, kind = star, "em"
		}
		if next < 0 {
			spans = append(spans, span{"plain", rest})
			break
		}
		if next > 0 {
			spans = append(spans, span{"plain", rest[:next]})
		}
		switch kind {
		case "code":
			end := strings.IndexByte(rest[next+1:], '`')
			if end < 0 {
				spans = append(spans, span{"plain", rest[next:]})
				rest = ""
				continue
			}
			spans = append(spans, span{"code", rest[next+1 : next+1+end]})
			rest = rest[next+1+end+1:]
		case "bold":
			end := strings.Index(rest[next+2:], "**")
			if end < 0 {
				spans = append(spans, span{"plain", rest[next:]})
				rest = ""
				continue
			}
			spans = append(spans, span{"bold", rest[next+2 : next+2+end]})
			rest = rest[next+2+end+2:]
		default:
			end := strings.IndexByte(rest[next+1:], '*')
			if end < 0 {
				spans = append(spans, span{"plain", rest[next:]})
				rest = ""
				continue
			}
			spans = append(spans, span{"em", rest[next+1 : next+1+end]})
			rest = rest[next+1+end+1:]
		}
	}
	var b strings.Builder
	for _, sp := range spans {
		t := stripLink(sp.text)
		if !cs.on {
			b.WriteString(t)
			continue
		}
		switch sp.kind {
		case "code":
			b.WriteString(lipgloss.NewStyle().Foreground(thCodeFg()).Background(thCodeBg()).Render(t))
		case "bold":
			b.WriteString(cs.header.Render(t))
		case "em":
			b.WriteString(lipgloss.NewStyle().Italic(true).Foreground(cs.dim.GetForeground()).Render(t))
		default:
			b.WriteString(t)
		}
	}
	return b.String()
}

func indexEm(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] != '*' {
			continue
		}
		if i+1 < len(s) && s[i+1] == '*' {
			i++
			continue
		}
		return i
	}
	return -1
}

var mdLinkRe = regexp.MustCompile(`\[([^\]]+)\]\([^)]+\)`)

func stripLink(s string) string {
	return mdLinkRe.ReplaceAllString(s, "$1")
}
