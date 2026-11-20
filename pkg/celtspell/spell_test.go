package celtspell

import (
	"testing"

	"github.com/udaltsovkdotru/celt/celtindex"
)

// spAbc is the repeated sample token used across the Levenshtein table rows so
// goconst stays quiet.
const spAbc = "abc"

// TestLevenshtein verifies the edit-distance helper on known pairs, including
// the case-insensitive comparison used by closestCandidate.
func TestLevenshtein(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want int
	}{
		{name: "both empty", a: "", b: "", want: 0},
		{name: "identical", a: spAbc, b: spAbc, want: 0},
		{name: "empty source", a: spAbc, b: "", want: 3},
		{name: "empty target", a: "", b: spAbc, want: 3},
		{name: "substitution", a: "kitten", b: "sitting", want: 3},
		{name: "near miss", a: "usernmae", b: "username", want: 2},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Levenshtein(tc.a, tc.b); got != tc.want {
				t.Errorf("Levenshtein(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// TestWithinThreshold verifies the edit-distance acceptance threshold scales
// with token length and short tokens require a distance of one.
func TestWithinThreshold(t *testing.T) {
	tests := []struct {
		name     string
		dist     int
		tokenLen int
		want     bool
	}{
		{name: "exact", dist: 0, tokenLen: 3, want: true},
		{name: "short one away", dist: 1, tokenLen: 2, want: true},
		{name: "short two away", dist: 2, tokenLen: 2, want: false},
		{name: "medium two away", dist: 2, tokenLen: 3, want: true},
		{name: "long two away", dist: 2, tokenLen: 8, want: true},
		{name: "long proportional", dist: 1, tokenLen: 100, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := WithinThreshold(tc.dist, tc.tokenLen); got != tc.want {
				t.Errorf("WithinThreshold(%d,%d) = %v, want %v", tc.dist, tc.tokenLen, got, tc.want)
			}
		})
	}
}

// TestClosest verifies the nearest-candidate lookup returns the closest match
// and rejects candidates beyond the threshold or an empty set.
func TestClosest(t *testing.T) {
	cands := []celtindex.Candidate{{Name: "userName", Kind: celtindex.KindVariable}}

	if got, ok := Closest("usrname", cands); !ok || got.Name != "userName" {
		t.Errorf("Closest(usrname) = %v/%v, want userName", got, ok)
	}
	if _, ok := Closest("xyzzy", cands); ok {
		t.Error("Closest(xyzzy) matched, want none (too far)")
	}
	if _, ok := Closest("usrname", nil); ok {
		t.Error("Closest(empty) matched, want none")
	}
}
