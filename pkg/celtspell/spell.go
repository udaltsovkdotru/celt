// Package celtspell provides the fuzzy, spell-checking primitives that the
// misspell rule and compile-error suggestions use to find a real identifier
// near a mistyped token. It operates on the public celtindex candidate set and
// is internal-by-convention (like golangci-lint's pkg/ tree): importable by the
// module, not a documented part of the framework's public API.
package celtspell

import (
	"strings"

	"github.com/udaltsovkdotru/celt/celtindex"
)

// Nearest returns the closest candidate name to token (compared
// case-insensitively), preserving any dotted namespace prefix on the token. ok
// is false when no candidate is within the edit-distance threshold.
func Nearest(ix *celtindex.Index, token string) (repl string, kind celtindex.Kind, ok bool) {
	prefix := ""
	base := token
	if i := strings.LastIndex(token, "."); i >= 0 {
		prefix = token[:i+1]
		base = token[i+1:]
	}
	lb := strings.ToLower(base)
	if lb == "" {
		return "", "", false
	}
	c, found := Closest(base, ix.Candidates())
	if !found {
		return "", "", false
	}
	return prefix + c.Name, c.Kind, true
}

// Closest returns the nearest candidate to token (case-insensitive), or ok
// false when none is within the edit-distance threshold.
func Closest(token string, cands []celtindex.Candidate) (celtindex.Candidate, bool) {
	lb := strings.ToLower(token)
	if lb == "" {
		return celtindex.Candidate{}, false
	}
	return closestCandidate(lb, cands)
}

// closestCandidate returns the candidate with the smallest edit distance to
// the lowercased token, provided it is within the accepted threshold. A
// candidate whose length differs by more than the max distance can never match
// (Levenshtein distance is never below the length difference).
func closestCandidate(lb string, cands []celtindex.Candidate) (celtindex.Candidate, bool) {
	bestDist := 1 << 30
	best := celtindex.Candidate{}
	found := false
	maxDist := 2
	if len(lb) <= 2 {
		maxDist = 1
	} else if d := len(lb) / 4; d > maxDist {
		maxDist = d
	}
	for _, c := range cands {
		diff := len(lb) - len(c.Name)
		if diff < 0 {
			diff = -diff
		}
		if diff > maxDist {
			continue
		}
		d := levenshtein(lb, strings.ToLower(c.Name))
		if d < bestDist {
			bestDist, best = d, c
			found = true
		}
	}
	if !found || bestDist == 0 || !withinThreshold(bestDist, len(lb)) {
		return celtindex.Candidate{}, false
	}
	return best, true
}

// WithinThreshold reports whether an edit distance is close enough for a token
// of the given length to count as a likely misspelling.
func WithinThreshold(dist, tokenLen int) bool {
	if tokenLen <= 2 {
		return dist <= 1
	}
	return dist <= 2 || dist*4 <= tokenLen
}

func withinThreshold(dist, tokenLen int) bool { return WithinThreshold(dist, tokenLen) }

// Levenshtein returns the edit distance between two strings.
func Levenshtein(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i, ra := range ar {
		cur[0] = i + 1
		for j, rb := range br {
			cost := 1
			if ra == rb {
				cost = 0
			}
			cur[j+1] = min(prev[j+1]+1, cur[j]+1, prev[j]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

func levenshtein(a, b string) int { return Levenshtein(a, b) }
