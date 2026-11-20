package celtlint

import (
	"strings"

	"github.com/udaltsovkdotru/celt/celtsource"
)

var Color bool

type Level int

const (
	// LevelOff disables a rule.
	LevelOff   Level = -1
	LevelError Level = iota // 0
	LevelWarn
)

const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiBlue   = "\x1b[34m"
	ansiCyan   = "\x1b[36m"
	ansiWhite  = "\x1b[37m"
)

func colorize(s, code string) string {
	if !Color {
		return s
	}
	return code + s + ansiReset
}

func bold(s string) string {
	if !Color {
		return s
	}
	return ansiBold + s + ansiReset
}

// Red, Green, Blue, Cyan and WhiteBold colorize a string for terminal output.
// They are exported so the rule package (which renders diff-style suggestions)
// can share the engine's color scheme.
func Red(s string) string       { return colorize(s, ansiRed) }
func Green(s string) string     { return colorize(s, ansiGreen) }
func Blue(s string) string      { return colorize(bold(s), ansiBlue) }
func Cyan(s string) string      { return colorize(bold(s), ansiCyan) }
func WhiteBold(s string) string { return colorize(bold(s), ansiWhite) }

// HintLabel returns the "hint:" label prefix shared by suggestion rendering.
func HintLabel() string {
	return Cyan("hint") + ": "
}

// BacktickQuotes normalizes single-quoted error names into backticks so the
// backtick extractor can pull them out regardless of the checker's quoting.
func BacktickQuotes(msg string) string {
	return strings.ReplaceAll(msg, "'", "`")
}

// BtName extracts the first backtick-quoted name from a message.
func BtName(msg string) string {
	start := strings.IndexByte(msg, '`')
	if start < 0 {
		return ""
	}
	rest := msg[start+1:]
	if end := strings.IndexByte(rest, '`'); end >= 0 {
		return rest[:end]
	}
	return ""
}

// DqName extracts the first double-quoted name from a message.
func DqName(msg string) string {
	start := strings.IndexByte(msg, '"')
	if start < 0 {
		return ""
	}
	rest := msg[start+1:]
	if end := strings.IndexByte(rest, '"'); end >= 0 {
		return rest[:end]
	}
	return ""
}

func errorSpan(snippet string, col int, msg string) (start, length int) {
	if col == 0 {
		if idx, tok := metadataSpan(snippet, msg); tok != "" {
			return idx, len(tok)
		}
		return len(snippet), 1
	}
	if strings.Contains(msg, "token recognition error") {
		if tok := strings.TrimSpace(BtName(BacktickQuotes(msg))); tok == "=" {
			return comparisonSpan(snippet, strings.Index(snippet, "="))
		}
	}
	return primarySpan(snippet, col, msg)
}

func metadataSpan(snippet, msg string) (idx int, tok string) {
	if tok := BtName(msg); tok != "" {
		if idx := strings.Index(snippet, tok); idx >= 0 {
			return idx, tok
		}
	}
	if tok := DqName(msg); tok != "" {
		if idx := strings.Index(snippet, tok); idx >= 0 {
			return idx, tok
		}
	}
	return -1, ""
}

func comparisonSpan(line string, eq int) (start, length int) {
	if eq < 0 || eq >= len(line) {
		return primarySpan(line, eq, "")
	}
	start = eq
	for start > 0 && !isCompBoundary(line[start-1]) {
		start--
	}
	for start < len(line) && (isCompBoundary(line[start]) || line[start] == ' ') {
		start++
	}
	end := eq
	for end < len(line) && !isCompBoundary(line[end]) {
		end++
	}
	for end > start && line[end-1] == ' ' {
		end--
	}
	if end <= start {
		return primarySpan(line, eq, "")
	}
	return start, end - start
}

func isCompBoundary(c byte) bool {
	return c == '&' || c == '|' || c == '(' || c == ')' || c == ','
}

func primarySpan(snippet string, col int, msg string) (start, length int) {
	token := BtName(BacktickQuotes(msg))
	if token == "" {
		token = DqName(msg)
	}
	if token != "" {
		if idx := strings.Index(snippet, token); idx >= 0 {
			return idx, len(token)
		}
		if i := strings.LastIndex(token, "."); i >= 0 {
			if idx := strings.Index(snippet, token[i+1:]); idx >= 0 {
				return idx, len(token) - i - 1
			}
		}
	}

	if col >= len(snippet) {
		col = len(snippet) - 1
	}
	if col < 0 {
		col = 0
	}
	start = col
	for start > 0 && celtsource.IsIdentByte(snippet[start-1]) {
		start--
	}
	end := col
	for end < len(snippet) && celtsource.IsIdentByte(snippet[end]) {
		end++
	}
	if end == start {
		end = start + 1
	}
	return start, end - start
}
