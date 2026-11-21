package celtsource

import (
	"unicode/utf16"
	"unicode/utf8"
)

type Position struct {
	Line int
	Char int
}

// nextLineBreak returns the byte index of the first line-break sequence at or
// after i, and the index just past it. A line break is \n, \r, or \r\n, so CR-
// only files (and LSP line arithmetic) split lines the same way LF files do.
func nextLineBreak(content string, i int) (idx, after int) {
	for i < len(content) {
		switch content[i] {
		case '\r':
			after = i + 1
			if after < len(content) && content[after] == '\n' {
				after++
			}
			return i, after
		case '\n':
			return i, i + 1
		}
		i++
	}
	return -1, -1
}

func OffsetToPos(content string, off int) Position {
	if off < 0 {
		off = 0
	}
	if off > len(content) {
		off = len(content)
	}
	line := 0
	lastLineStart := 0
	i := 0
	for i < off {
		idx, after := nextLineBreak(content, i)
		if idx < 0 || off <= idx {
			break
		}
		// An offset inside a multi-byte break (e.g. between the CR and LF of
		// CRLF) belongs to the line being terminated, not to the next line.
		if off < after {
			break
		}
		line++
		lastLineStart = after
		i = after
	}
	return Position{Line: line, Char: off - lastLineStart}
}

func PosToOffset(content string, p Position) int {
	off := 0
	for i := 0; i < p.Line && off < len(content); i++ {
		_, after := nextLineBreak(content, off)
		if after < 0 {
			break
		}
		off = after
	}
	return off + p.Char
}

func UTF16Position(content string, p Position) Position {
	start, end := LineByteRange(content, p.Line)
	line := content[start:end]
	if p.Char < 0 {
		p.Char = 0
	}
	if p.Char > len(line) {
		p.Char = len(line)
	}
	return Position{Line: p.Line, Char: utf16Column(line, p.Char)}
}

func UTF16Len(s string) int {
	return utf16Column(s, len(s))
}

func utf16Column(line string, byteCol int) int {
	units := 0
	for i := 0; i < byteCol; {
		r, size := utf8.DecodeRuneInString(line[i:])
		units += utf16.RuneLen(r)
		i += size
	}
	return units
}

func UTF16ToByteCol(line string, char int) int {
	if char <= 0 {
		return 0
	}
	units := 0
	i := 0
	for i < len(line) && units < char {
		r, size := utf8.DecodeRuneInString(line[i:])
		units += utf16.RuneLen(r)
		i += size
	}
	return i
}

func LineByteRange(content string, line int) (start, end int) {
	for l := 0; l < line && start < len(content); l++ {
		_, after := nextLineBreak(content, start)
		if after < 0 {
			start = len(content)
			break
		}
		start = after
	}
	idx, _ := nextLineBreak(content, start)
	if idx < 0 {
		end = len(content)
	} else {
		end = idx
	}
	return start, end
}

func LineByteColOffset(content string, line, char int) (off, lineStart, lineEnd int, ok bool) {
	lineStart, lineEnd = LineByteRange(content, line)
	if lineStart >= lineEnd {
		return 0, lineStart, lineEnd, false
	}
	return lineStart + UTF16ToByteCol(content[lineStart:lineEnd], char), lineStart, lineEnd, true
}
