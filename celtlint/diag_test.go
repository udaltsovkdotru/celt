package celtlint

import (
	"testing"
)

// fdSetColor temporarily overrides the package Color flag and restores it when
// the test finishes, so tests run in isolation from any other test that toggles
// color output.
func fdSetColor(t *testing.T, v bool) {
	t.Helper()
	old := Color
	Color = v
	t.Cleanup(func() { Color = old })
}

func TestFDLevelOrdering(t *testing.T) {
	if LevelOff != -1 {
		t.Errorf("LevelOff = %d, want -1", LevelOff)
	}
	if LevelError != 1 {
		t.Errorf("LevelError = %d, want 1 (iota after explicit LevelOff)", LevelError)
	}
	if LevelWarn != 2 {
		t.Errorf("LevelWarn = %d, want 2", LevelWarn)
	}
	if LevelOff >= LevelError || LevelError >= LevelWarn {
		t.Errorf("levels not ordered: off=%d error=%d warn=%d", LevelOff, LevelError, LevelWarn)
	}
}

func TestFDColorizeDisabled(t *testing.T) {
	fdSetColor(t, false)
	if got := colorize("warn", ansiYellow); got != "warn" {
		t.Errorf("colorize disabled = %q, want plain", got)
	}
}

func TestFDColorizeEnabled(t *testing.T) {
	fdSetColor(t, true)
	want := ansiRed + "err" + ansiReset
	if got := colorize("err", ansiRed); got != want {
		t.Errorf("colorize enabled = %q, want %q", got, want)
	}
}

func TestFDBold(t *testing.T) {
	fdSetColor(t, false)
	if got := bold("x"); got != "x" {
		t.Errorf("bold disabled = %q", got)
	}
	fdSetColor(t, true)
	want := ansiBold + "x" + ansiReset
	if got := bold("x"); got != want {
		t.Errorf("bold enabled = %q, want %q", got, want)
	}
}

func TestFDHintLabel(t *testing.T) {
	fdSetColor(t, false)
	if got := HintLabel(); got != "hint: " {
		t.Errorf("HintLabel = %q, want %q", got, "hint: ")
	}
}

func TestFDIsCompBoundary(t *testing.T) {
	yes := "&|(),"
	for _, c := range []byte(yes) {
		if !isCompBoundary(c) {
			t.Errorf("isCompBoundary(%q) = false, want true", c)
		}
	}
	no := "ab =+ "
	for _, c := range []byte(no) {
		if isCompBoundary(c) {
			t.Errorf("isCompBoundary(%q) = true, want false", c)
		}
	}
}

func TestFDPrimarySpan(t *testing.T) {
	cases := []struct {
		name      string
		snippet   string
		col       int
		msg       string
		wantStart int
		wantLen   int
	}{
		{"backtick token in snippet", "foo + bar", 0, "unknown name 'foo'", 0, 3},
		{"dq token in snippet", `x == "yes"`, 4, `found "yes"`, 6, 3},
		{"namespace member uses last segment", "b(x)", 0, "unknown 'a.b'", 0, 1},
		{"col expands ident", fdFooBar, 1, "", 0, 3},
		{"col past end clamps", "ab", 10, "", 0, 2},
		{"col negative clamps to zero", "xy", -3, "", 0, 2},
		{"single char snippet", "x", 0, "", 0, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, length := primarySpan(tc.snippet, tc.col, tc.msg)
			if start != tc.wantStart || length != tc.wantLen {
				t.Errorf("primarySpan(%q, %d, %q) = (%d, %d), want (%d, %d)",
					tc.snippet, tc.col, tc.msg, start, length, tc.wantStart, tc.wantLen)
			}
		})
	}
}

func TestFDMetadataSpan(t *testing.T) {
	cases := []struct {
		name    string
		snippet string
		msg     string
		wantIdx int
		wantTok string
	}{
		{"backtick token", fdFooBar, "unknown name `foo`", 0, fdFoo},
		{"dq token", "a \"b\" c", `found "b"`, 3, "b"},
		{"no token", fdAbc, "nothing relevant", -1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx, tok := metadataSpan(tc.snippet, tc.msg)
			if idx != tc.wantIdx || tok != tc.wantTok {
				t.Errorf("metadataSpan(%q, %q) = (%d, %q), want (%d, %q)",
					tc.snippet, tc.msg, idx, tok, tc.wantIdx, tc.wantTok)
			}
		})
	}
}

func TestFDComparisonSpan(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		eq        int
		wantStart int
		wantLen   int
	}{
		{"whole comparison", "x==1", 1, 0, 4},
		{"spaced comparison", "x == 1", 2, 0, 6},
		{"eq beyond line falls back to col", fdAbc, 10, 0, 3},
		{"eq negative falls back to col", fdAbc, -1, 0, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, length := comparisonSpan(tc.line, tc.eq)
			if start != tc.wantStart || length != tc.wantLen {
				t.Errorf("comparisonSpan(%q, %d) = (%d, %d), want (%d, %d)",
					tc.line, tc.eq, start, length, tc.wantStart, tc.wantLen)
			}
		})
	}
}

func TestFDErrorSpan(t *testing.T) {
	cases := []struct {
		name      string
		snippet   string
		col       int
		msg       string
		wantStart int
		wantLen   int
	}{
		{"col zero with token uses metadata", "foo + 1", 0, "unknown name `foo`", 0, 3},
		{"col zero without token spans end", "abc", 0, "some error", 3, 1},
		{"token recognition of equals uses comparison", "a = b", 1, "token recognition error at '='", 0, 5},
		{"default delegates to primary span", "foo bar", 4, "x", 4, 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start, length := errorSpan(tc.snippet, tc.col, tc.msg)
			if start != tc.wantStart || length != tc.wantLen {
				t.Errorf("errorSpan(%q, %d, %q) = (%d, %d), want (%d, %d)",
					tc.snippet, tc.col, tc.msg, start, length, tc.wantStart, tc.wantLen)
			}
		})
	}
}
