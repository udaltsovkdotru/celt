package celtsource

import (
	"testing"
)

func TestOffsetToPos(t *testing.T) {
	srcOffsetToPos := []struct {
		name string
		off  int
		want Position
	}{
		{name: "offset at zero", off: 0, want: Position{Line: 0, Char: 0}},
		{name: "middle of line", off: 1, want: Position{Line: 0, Char: 1}},
		{name: "end of first line", off: 2, want: Position{Line: 0, Char: 2}},
		{name: "start second line", off: 3, want: Position{Line: 1, Char: 0}},
		{name: "middle second line", off: 5, want: Position{Line: 1, Char: 2}},
		{name: "start third line", off: 6, want: Position{Line: 2, Char: 0}},
		{name: "end of content", off: 8, want: Position{Line: 2, Char: 2}},
		{name: "negative clamps", off: -4, want: Position{Line: 0, Char: 0}},
		{name: "overflow clamps", off: 100, want: Position{Line: 2, Char: 2}},
	}
	for _, tc := range srcOffsetToPos {
		t.Run(tc.name, func(t *testing.T) {
			got := OffsetToPos(srcThreeLine, tc.off)
			if got != tc.want {
				t.Errorf("OffsetToPos(%q, %d) = %+v, want %+v", srcThreeLine, tc.off, got, tc.want)
			}
		})
	}
}

func TestOffsetToPosInsideCRLFDoesNotReturnNegativeColumn(t *testing.T) {
	if got := OffsetToPos("a\r\nb", 2); got.Char < 0 {
		t.Errorf("offset between CR and LF gave a negative column: %+v", got)
	}
}

func TestOffsetToPosTrailingNewline(t *testing.T) {
	const srcTrailingContent = "ab\n"
	srcTrailing := []struct {
		off  int
		want Position
	}{
		{off: 0, want: Position{Line: 0, Char: 0}},
		{off: 2, want: Position{Line: 0, Char: 2}},
		{off: 3, want: Position{Line: 1, Char: 0}},
	}
	for _, tc := range srcTrailing {
		if got := OffsetToPos(srcTrailingContent, tc.off); got != tc.want {
			t.Errorf("OffsetToPos(%q, %d) = %+v, want %+v", srcTrailingContent, tc.off, got, tc.want)
		}
	}
}

func TestPosToOffset(t *testing.T) {
	const srcPosContent = "l1\nl2\nl3"
	srcPosToOffset := []struct {
		name string
		pos  Position
		want int
	}{
		{name: "zero offset", pos: Position{Line: 0, Char: 0}, want: 0},
		{name: "middle first", pos: Position{Line: 0, Char: 1}, want: 1},
		{name: "start second", pos: Position{Line: 1, Char: 0}, want: 3},
		{name: "end second", pos: Position{Line: 1, Char: 2}, want: 5},
		{name: "start third", pos: Position{Line: 2, Char: 0}, want: 6},
		{name: "end content", pos: Position{Line: 2, Char: 2}, want: 8},
		{name: "line beyond end", pos: Position{Line: 9, Char: 0}, want: 6},
		{name: "char beyond line", pos: Position{Line: 0, Char: 50}, want: 50},
	}
	for _, tc := range srcPosToOffset {
		t.Run(tc.name, func(t *testing.T) {
			got := PosToOffset(srcPosContent, tc.pos)
			if got != tc.want {
				t.Errorf("PosToOffset(%q, %+v) = %d, want %d", srcPosContent, tc.pos, got, tc.want)
			}
		})
	}
}

func TestUTF16Position(t *testing.T) {
	srcUTF16Pos := []struct {
		name string
		pos  Position
		want Position
	}{
		{name: "utf16 zero", pos: Position{Line: 0, Char: 0}, want: Position{Line: 0, Char: 0}},
		{name: "ascii column", pos: Position{Line: 0, Char: 1}, want: Position{Line: 0, Char: 1}},
		// byte col 2 is the first byte of the 4-byte emoji, which is 2 UTF-16 units.
		{name: "emoji start", pos: Position{Line: 0, Char: 2}, want: Position{Line: 0, Char: 3}},
		// byte col 5 is the trailing 'b' (after a + emoji), still 3 UTF-16 units.
		{name: "after emoji", pos: Position{Line: 0, Char: 5}, want: Position{Line: 0, Char: 3}},
		{name: "negative char clamps", pos: Position{Line: 0, Char: -2}, want: Position{Line: 0, Char: 0}},
		{name: "overflow clamps", pos: Position{Line: 0, Char: 100}, want: Position{Line: 0, Char: 4}},
	}
	for _, tc := range srcUTF16Pos {
		t.Run(tc.name, func(t *testing.T) {
			got := UTF16Position(srcEmojiMix, tc.pos)
			if got != tc.want {
				t.Errorf("UTF16Position(%q, %+v) = %+v, want %+v", srcEmojiMix, tc.pos, got, tc.want)
			}
		})
	}
}

func TestUTF16PositionMultiline(t *testing.T) {
	const srcMultiContent = "a\n\U0001F600\u754c\n"
	srcMulti := []struct {
		name string
		pos  Position
		want Position
	}{
		{name: "second line start", pos: Position{Line: 1, Char: 0}, want: Position{Line: 1, Char: 0}},
		// char 1 is the first byte of the emoji -> 2 UTF-16 units.
		{name: "second line emoji", pos: Position{Line: 1, Char: 1}, want: Position{Line: 1, Char: 2}},
		// char 5 is the start of \u754c -> emoji(2) + \u754c(1) = 3 units.
		{name: "second line han", pos: Position{Line: 1, Char: 5}, want: Position{Line: 1, Char: 3}},
		// char 7 is end of line -> 3 units.
		{name: "second line end", pos: Position{Line: 1, Char: 7}, want: Position{Line: 1, Char: 3}},
		{name: "third line empty", pos: Position{Line: 2, Char: 0}, want: Position{Line: 2, Char: 0}},
	}
	for _, tc := range srcMulti {
		t.Run(tc.name, func(t *testing.T) {
			got := UTF16Position(srcMultiContent, tc.pos)
			if got != tc.want {
				t.Errorf("UTF16Position(%q, %+v) = %+v, want %+v", srcMultiContent, tc.pos, got, tc.want)
			}
		})
	}
}

func TestUTF16Len(t *testing.T) {
	srcUTF16Len := []struct {
		name    string
		content string
		want    int
	}{
		{name: "empty utf16", content: "", want: 0},
		{name: "ascii string", content: srcASCII, want: 3},
		{name: "bmp han", content: "\u754c", want: 1},
		{name: "emoji surrogate pair", content: "\U0001F600", want: 2},
		{name: "mixed", content: srcEmojiMix, want: 4},
		{name: "two astral", content: "\U0001F600\U0001F601", want: 4},
	}
	for _, tc := range srcUTF16Len {
		t.Run(tc.name, func(t *testing.T) {
			got := UTF16Len(tc.content)
			if got != tc.want {
				t.Errorf("UTF16Len(%q) = %d, want %d", tc.content, got, tc.want)
			}
		})
	}
}

func TestUTF16ToByteCol(t *testing.T) {
	srcUTF16ToByte := []struct {
		name string
		line string
		char int
		want int
	}{
		{name: "ascii start", line: srcASCII, char: 0, want: 0},
		{name: "ascii mid", line: srcASCII, char: 2, want: 2},
		{name: "ascii end", line: srcASCII, char: 3, want: 3},
		{name: "ascii overflow", line: srcASCII, char: 9, want: 3},
		{name: "ascii negative", line: srcASCII, char: -1, want: 0},
		{name: "mixed after a", line: srcEmojiMix, char: 1, want: 1},
		{name: "mixed into emoji", line: srcEmojiMix, char: 2, want: 5},
		{name: "mixed at b", line: srcEmojiMix, char: 4, want: 6},
		{name: "mixed overflow", line: srcEmojiMix, char: 9, want: 6},
		{name: "han", line: "\u754c", char: 1, want: 3},
	}
	for _, tc := range srcUTF16ToByte {
		t.Run(tc.name, func(t *testing.T) {
			got := UTF16ToByteCol(tc.line, tc.char)
			if got != tc.want {
				t.Errorf("UTF16ToByteCol(%q, %d) = %d, want %d", tc.line, tc.char, got, tc.want)
			}
		})
	}
}

func TestLineByteRange(t *testing.T) {
	srcLineRange := []struct {
		name      string
		content   string
		line      int
		wantStart int
		wantEnd   int
	}{
		{name: "first", content: srcThreeLine, line: 0, wantStart: 0, wantEnd: 2},
		{name: "second", content: srcThreeLine, line: 1, wantStart: 3, wantEnd: 5},
		{name: "third", content: srcThreeLine, line: 2, wantStart: 6, wantEnd: 8},
		{name: "beyond", content: srcThreeLine, line: 3, wantStart: 8, wantEnd: 8},
		{name: "negative", content: srcThreeLine, line: -1, wantStart: 0, wantEnd: 2},
		{name: "trailing newline", content: "ab\ncd\n", line: 2, wantStart: 6, wantEnd: 6},
		{name: "empty range", content: "", line: 0, wantStart: 0, wantEnd: 0},
	}
	for _, tc := range srcLineRange {
		t.Run(tc.name, func(t *testing.T) {
			start, end := LineByteRange(tc.content, tc.line)
			if start != tc.wantStart || end != tc.wantEnd {
				t.Errorf("LineByteRange(%q, %d) = (%d, %d), want (%d, %d)",
					tc.content, tc.line, start, end, tc.wantStart, tc.wantEnd)
			}
		})
	}
}

func TestLineByteColOffset(t *testing.T) {
	srcLineCol := []struct {
		name      string
		content   string
		line      int
		char      int
		wantOff   int
		wantStart int
		wantEnd   int
		wantOK    bool
	}{
		{name: "first line start", content: srcTwoLine, line: 0, char: 0, wantOff: 0, wantStart: 0, wantEnd: 2, wantOK: true},
		{name: "first line end", content: srcTwoLine, line: 0, char: 2, wantOff: 2, wantStart: 0, wantEnd: 2, wantOK: true},
		{name: "last line without newline", content: srcTwoLine, line: 1, char: 1, wantOff: 4, wantStart: 3, wantEnd: 5, wantOK: true},
		{name: "line beyond", content: srcTwoLine, line: 2, char: 0, wantOff: 0, wantStart: 5, wantEnd: 5, wantOK: false},
		{name: "char overflow clamps", content: srcTwoLine, line: 0, char: 9, wantOff: 2, wantStart: 0, wantEnd: 2, wantOK: true},
		{name: "trailing empty line", content: "ab\n", line: 1, char: 0, wantOff: 0, wantStart: 3, wantEnd: 3, wantOK: false},
	}
	for _, tc := range srcLineCol {
		t.Run(tc.name, func(t *testing.T) {
			off, lineStart, lineEnd, ok := LineByteColOffset(tc.content, tc.line, tc.char)
			if off != tc.wantOff || lineStart != tc.wantStart || lineEnd != tc.wantEnd || ok != tc.wantOK {
				t.Errorf("LineByteColOffset(%q, %d, %d) = (%d, %d, %d, %v), want (%d, %d, %d, %v)",
					tc.content, tc.line, tc.char, off, lineStart, lineEnd, ok,
					tc.wantOff, tc.wantStart, tc.wantEnd, tc.wantOK)
			}
		})
	}
}
