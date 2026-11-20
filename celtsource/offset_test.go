package celtsource

import (
	"reflect"
	"testing"
)

// Package-level constants shared by the celtsource tests. They exist to avoid
// goconst flags on repeated string literals across test files.
const (
	srcASCII     = "abc"
	srcEmojiMix  = "a\U0001F600b"
	srcThreeLine = "ab\ncd\nef"
	srcTwoLine   = "ab\ncd"
	srcArithExpr = "a + b * c"
	srcStrLit    = `"hi"`
)

func TestRuneOffsets(t *testing.T) {
	srcRuneOffsets := []struct {
		name    string
		content string
		want    []int
	}{
		{name: "empty string", content: "", want: []int{0}},
		{name: "single ascii", content: "a", want: []int{0, 1}},
		{name: "ascii content", content: srcASCII, want: []int{0, 1, 2, 3}},
		{name: "multibyte two byte", content: "a\u00e9b", want: []int{0, 1, 3, 4}},
		{name: "multibyte mixed", content: "a\u00e9\u754c", want: []int{0, 1, 3, 6}},
		{name: "emoji four byte", content: "\U0001F600", want: []int{0, 4}},
		{name: "newline", content: "a\nb", want: []int{0, 1, 2, 3}},
	}
	for _, tc := range srcRuneOffsets {
		t.Run(tc.name, func(t *testing.T) {
			got := RuneOffsets(tc.content)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RuneOffsets(%q) = %v, want %v", tc.content, got, tc.want)
			}
		})
	}
}

func TestByteOffset(t *testing.T) {
	// Byte offsets of rune indices for the multi-byte string "a\u00e9\u754c":
	// 'a' at byte 0, '\u00e9' at byte 1, '\u754c' at byte 3, end at byte 6.
	srcOff := RuneOffsets("a\u00e9\u754c")
	srcByteOffset := []struct {
		name    string
		runeOff int
		want    int
	}{
		{name: "first", runeOff: 0, want: 0},
		{name: "second multibyte", runeOff: 1, want: 1},
		{name: "third", runeOff: 2, want: 3},
		{name: "last", runeOff: 3, want: 6},
		{name: "beyond end clamps", runeOff: 10, want: 6},
		{name: "negative clamps to zero", runeOff: -1, want: 0},
	}
	for _, tc := range srcByteOffset {
		t.Run(tc.name, func(t *testing.T) {
			got := ByteOffset(srcOff, tc.runeOff)
			if got != tc.want {
				t.Errorf("ByteOffset(%v, %d) = %d, want %d", srcOff, tc.runeOff, got, tc.want)
			}
		})
	}
}

func TestByteOffsetAscii(t *testing.T) {
	off := RuneOffsets("ab")
	srcByteOffsetAscii := []struct {
		runeOff int
		want    int
	}{
		{runeOff: 0, want: 0},
		{runeOff: 1, want: 1},
		{runeOff: 2, want: 2},
		{runeOff: 5, want: 2},
		{runeOff: -3, want: 0},
	}
	for _, tc := range srcByteOffsetAscii {
		if got := ByteOffset(off, tc.runeOff); got != tc.want {
			t.Errorf("ByteOffset(%v, %d) = %d, want %d", off, tc.runeOff, got, tc.want)
		}
	}
}
