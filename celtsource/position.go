package celtsource

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

type Position struct {
	Line int
	Char int
}

func OffsetToPos(content string, off int) Position {
	if off < 0 {
		off = 0
	}
	if off > len(content) {
		off = len(content)
	}
	line := strings.Count(content[:off], "\n")
	lineStart := strings.LastIndex(content[:off], "\n") + 1
	return Position{Line: line, Char: off - lineStart}
}

func PosToOffset(content string, p Position) int {
	off := 0
	for i := 0; i < p.Line && off < len(content); i++ {
		nl := strings.IndexByte(content[off:], '\n')
		if nl < 0 {
			break
		}
		off += nl + 1
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
		nl := strings.IndexByte(content[start:], '\n')
		if nl < 0 {
			start = len(content)
			break
		}
		start += nl + 1
	}
	end = strings.IndexByte(content[start:], '\n')
	if end < 0 {
		end = len(content)
	} else {
		end += start
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
