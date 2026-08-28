package tui

import (
	"regexp"
	"strconv"
	"strings"
)

var ansiEscapeRE = regexp.MustCompile(`\x1b\[[0-9;]*m`)

type cellPos struct {
	x int
	y int
}

type cellRange struct {
	start cellPos
	end   cellPos
}

func normalizeCellRange(a, b cellPos) cellRange {
	if a.y > b.y || (a.y == b.y && a.x > b.x) {
		a, b = b, a
	}
	if b.x < a.x && a.y == b.y {
		b.x = a.x
	}
	return cellRange{start: a, end: b}
}

func (r cellRange) colsForRow(y int) (int, int) {
	start, end := 0, maxInt
	if y == r.start.y {
		start = r.start.x
	}
	if y == r.end.y {
		end = r.end.x + 1
	}
	if end < start {
		end = start
	}
	return start, end
}

const maxInt = int(^uint(0) >> 1)

func lastAssistantReply(lines []string) string {
	return nthAssistantReply(lines, 1)
}

func nthAssistantReply(lines []string, n int) string {
	blocks := assistantBlocks(lines)
	if n < 1 || n > len(blocks) {
		return ""
	}
	return blocks[len(blocks)-n]
}

func assistantBlocks(lines []string) []string {
	var blocks []string
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		blocks = append(blocks, strings.Join(cur, "\n"))
		cur = nil
	}
	for _, raw := range lines {
		if strings.HasPrefix(raw, tagReply+" ") {
			cur = append(cur, strings.TrimPrefix(raw, tagReply+" "))
			continue
		}
		flush()
	}
	flush()
	return blocks
}

func linePlain(raw string) string {
	_, text := classify(raw)
	return text
}

func blockPlain(lines []string, sel int) string {
	if sel < 0 || sel >= len(lines) {
		return ""
	}
	class, _ := classify(lines[sel])
	lo, hi := sel, sel
	for lo > 0 {
		c, _ := classify(lines[lo-1])
		if c != class && c != classNone {
			break
		}
		lo--
	}
	for hi+1 < len(lines) {
		c, _ := classify(lines[hi+1])
		if c != class && c != classNone {
			break
		}
		hi++
	}
	out := make([]string, 0, hi-lo+1)
	for _, raw := range lines[lo : hi+1] {
		out = append(out, linePlain(raw))
	}
	return strings.Join(out, "\n")
}

func transcriptPlain(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, raw := range lines {
		out = append(out, linePlain(raw))
	}
	return strings.Join(out, "\n")
}

func cleanCopiedPaneRows(lines []string) string {
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		clean, keep := cleanCopiedPaneRow(line)
		if keep {
			out = append(out, clean)
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func cleanCopiedPaneSelection(lines []string, rng cellRange) string {
	out := make([]string, 0, len(lines))
	for y := max(0, rng.start.y); y <= min(len(lines)-1, rng.end.y); y++ {
		start, end := rng.colsForRow(y)
		selected := visibleCellSlice(lines[y], start, end)
		clean, keep := cleanCopiedPaneRow(selected)
		if keep {
			out = append(out, clean)
		}
	}
	for len(out) > 0 && strings.TrimSpace(out[0]) == "" {
		out = out[1:]
	}
	for len(out) > 0 && strings.TrimSpace(out[len(out)-1]) == "" {
		out = out[:len(out)-1]
	}
	return strings.Join(out, "\n")
}

func cleanCopiedPaneRow(line string) (string, bool) {
	line = ansiEscapeRE.ReplaceAllString(line, "")
	line = strings.TrimLeft(line, " \t")
	if line == "" {
		return "", true
	}
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "╭") || strings.HasPrefix(trimmed, "╰") {
		return "", false
	}
	if strings.HasPrefix(trimmed, "▶ ╭") {
		return "", false
	}
	if idx := strings.LastIndex(line, "│"); idx >= 0 && strings.TrimSpace(line[idx:]) == "│" {
		line = strings.TrimRight(line[:idx], " \t")
	}
	if strings.HasPrefix(line, "│") {
		body := strings.TrimPrefix(line, "│")
		body = strings.TrimPrefix(body, " ")
		if idx := strings.LastIndex(body, "│"); idx >= 0 {
			body = body[:idx]
		}
		return strings.TrimRight(body, " \t"), true
	}
	return strings.TrimRight(line, " \t"), true
}

func visibleCellSlice(line string, start, end int) string {
	line = ansiEscapeRE.ReplaceAllString(line, "")
	if start < 0 {
		start = 0
	}
	runes := []rune(line)
	if end < start {
		end = start
	}
	start = min(start, len(runes))
	end = min(end, len(runes))
	return string(runes[start:end])
}

func rewindLines(lines []string, keepUsers int) []string {
	if keepUsers <= 0 {
		return nil
	}
	n := 0
	for i, raw := range lines {
		if !strings.HasPrefix(raw, tagUser+" ") {
			continue
		}
		n++
		if n > keepUsers {
			return append([]string(nil), lines[:i]...)
		}
	}
	return append([]string(nil), lines...)
}

func userPromptItems(lines []string) []overlayItem {
	var items []overlayItem
	n := 0
	var cur []string
	flush := func() {
		if len(cur) == 0 {
			return
		}
		n++
		items = append(items, overlayItem{
			id:    strconv.Itoa(n),
			title: strings.Join(cur, " "),
		})
		cur = nil
	}
	for _, raw := range lines {
		if strings.HasPrefix(raw, tagUser+" ") {
			cur = append(cur, strings.TrimPrefix(raw, tagUser+" "))
			continue
		}
		flush()
	}
	flush()
	return items
}
