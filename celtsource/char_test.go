package celtsource

import (
	"testing"
)

func TestIsSpace(t *testing.T) {
	srcIsSpace := []struct {
		name string
		b    byte
		want bool
	}{
		{name: "space", b: ' ', want: true},
		{name: "tab", b: '\t', want: true},
		{name: "newline", b: '\n', want: true},
		{name: "carriage return", b: '\r', want: true},
		{name: "letter", b: 'a', want: false},
		{name: "digit", b: '0', want: false},
		{name: "underscore", b: '_', want: false},
		{name: "nul", b: 0x1f, want: false},
	}
	for _, tc := range srcIsSpace {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsSpace(tc.b); got != tc.want {
				t.Errorf("IsSpace(%q) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

func TestIsIdentByte(t *testing.T) {
	srcIsIdentByte := []struct {
		name string
		b    byte
		want bool
	}{
		{name: "lowercase", b: 'a', want: true},
		{name: "uppercase", b: 'Z', want: true},
		{name: "number", b: '7', want: true},
		{name: "underscore byte", b: '_', want: true},
		{name: "dot byte", b: '.', want: false},
		{name: "dash byte", b: '-', want: false},
		{name: "blank", b: ' ', want: false},
	}
	for _, tc := range srcIsIdentByte {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsIdentByte(tc.b); got != tc.want {
				t.Errorf("IsIdentByte(%q) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

func TestIsIdentStart(t *testing.T) {
	srcIsIdentStart := []struct {
		name string
		b    byte
		want bool
	}{
		{name: "underscore start", b: '_', want: true},
		{name: "lowercase", b: 'a', want: true},
		{name: "uppercase", b: 'Q', want: true},
		{name: "digit not start", b: '3', want: false},
		{name: "dot start", b: '.', want: false},
		{name: "dash start", b: '-', want: false},
	}
	for _, tc := range srcIsIdentStart {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsIdentStart(tc.b); got != tc.want {
				t.Errorf("IsIdentStart(%q) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

func TestIsIdentChar(t *testing.T) {
	srcIsIdentChar := []struct {
		name string
		b    byte
		want bool
	}{
		{name: "letter", b: 'a', want: true},
		{name: "digit char", b: '9', want: true},
		{name: "underscore char", b: '_', want: true},
		{name: "dot char", b: '.', want: true},
		{name: "dash char", b: '-', want: false},
		{name: "blank char", b: ' ', want: false},
	}
	for _, tc := range srcIsIdentChar {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsIdentChar(tc.b); got != tc.want {
				t.Errorf("IsIdentChar(%q) = %v, want %v", tc.b, got, tc.want)
			}
		})
	}
}

func TestSkipString(t *testing.T) {
	srcSkipString := []struct {
		name    string
		content string
		want    int
	}{
		{name: "double quoted", content: srcStrLit, want: 4},
		{name: "single quoted", content: `'x'`, want: 3},
		{name: "escaped quote", content: `"a\"b"`, want: 6},
		{name: "escaped backslash", content: `"a\\"`, want: 5},
		{name: "empty string literal", content: `""`, want: 2},
		{name: "unterminated", content: `"abc`, want: 4},
		{name: "trailing escape skips past end", content: `"abc\`, want: 6},
	}
	for _, tc := range srcSkipString {
		t.Run(tc.name, func(t *testing.T) {
			if got := SkipString(tc.content, 0); got != tc.want {
				t.Errorf("SkipString(%q, 0) = %d, want %d", tc.content, got, tc.want)
			}
		})
	}
}
