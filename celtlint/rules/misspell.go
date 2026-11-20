package rules

import (
	"slices"
	"strings"

	"cel.dev/cel-go/common"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtsource"
	"github.com/udaltsovkdotru/celt/pkg/celtspell"
)

// Misspell flags unknown identifiers and suggests corrections.
type Misspell struct {
	celtlint.RuleMeta
}

func (Misspell) Check(ctx *celtlint.Context) {
	if ctx.Index == nil {
		return
	}
	ctx.ReportAll(checkMisspellings(ctx.Source, ctx.Index))
}

// checkMisspellings scans the source for identifiers, functions, enum values
// and fields that are unknown and, where a close real name exists, reports a
// finding with a suggested fix.
func checkMisspellings(src common.Source, idx *celtindex.Index) []celtlint.Finding {
	content := src.Content()
	var out []celtlint.Finding
	i, n := 0, len(content)
	lastIdent := ""
	member := false
	for i < n {
		c := content[i]
		switch {
		case isComment(content, i):
			for i < n && content[i] != '\n' {
				i++
			}
		case c == '"' || c == '\'':
			i = celtsource.SkipString(content, i)
		case c == '.':
			member = true
			i++
		case celtsource.IsIdentStart(c):
			start := i
			for i < n && celtsource.IsIdentByte(content[i]) {
				i++
			}
			tok := content[start:i]
			out = append(out, identFindings(idx, content, start, i, tok, member, lastIdent)...)
			lastIdent = tok
			member = false
		default:
			member = false
			i++
		}
	}
	return out
}

func identFindings(idx *celtindex.Index, content string, start, end int, tok string, member bool, receiver string) []celtlint.Finding {
	if idx.Valid(tok) && !member {
		return nil
	}
	if member {
		kind, cands := idx.MemberCandidates(receiver)
		if kind == celtindex.MemberNone || slices.ContainsFunc(cands, func(c celtindex.Candidate) bool { return c.Name == tok }) {
			return nil
		}
		switch kind {
		case celtindex.MemberEnumValue, celtindex.MemberObjField:
			code, msg := celtlint.CodeUnknownEnumVariant, "unknown enum variant"
			if kind == celtindex.MemberObjField {
				code, msg = celtlint.CodeUnknownField, "unknown field"
			}
			repl, hasMatch := match(idx, tok, cands)
			return []celtlint.Finding{unknownError(code, msg, repl, hasMatch, content, start, end)}
		default:
			if repl, hasMatch := match(idx, tok, cands); hasMatch {
				return []celtlint.Finding{unknownError(celtlint.CodeUnknownFunction,
					"unknown function", repl, true, content, start, end)}
			}
		}
		return nil
	}
	if repl, hasMatch := match(idx, tok, nil); hasMatch {
		return []celtlint.Finding{unknownError(celtlint.CodeUnknownIdentifier,
			"unknown identifier", repl, true, content, start, end)}
	}
	return nil
}

func unknownError(code, msg, repl string, hasMatch bool, content string, start, end int) celtlint.Finding {
	f := celtlint.Finding{
		Level:   celtlint.LevelError,
		Code:    code,
		Message: msg,
		Start:   celtsource.OffsetToPos(content, start),
		End:     celtsource.OffsetToPos(content, end),
	}
	if hasMatch {
		f.Fix = repl
	}
	return f
}

// match returns the closest candidate name to token, if any is within the
// spell threshold.
func match(idx *celtindex.Index, token string, cands []celtindex.Candidate) (string, bool) {
	if len(cands) == 0 {
		cands = idx.Candidates()
	}
	lb := strings.ToLower(token)
	if lb == "" {
		return "", false
	}
	if c, ok := celtspell.Closest(lb, cands); ok {
		return c.Name, true
	}
	return "", false
}

func isComment(content string, i int) bool {
	return i+1 < len(content) && content[i] == '/' && content[i+1] == '/'
}

// DropRedundantCompileErrors removes compile errors that are already covered by
// a misspell finding reported against the same name, an enum member reference,
// or a macro-call typo.
func DropRedundantCompileErrors(findings []celtlint.Finding, content string, idx *celtindex.Index, reported map[string]bool) []celtlint.Finding {
	out := findings[:0]
	for i := range findings {
		f := &findings[i]
		if !isCompileError(f.Code) {
			out = append(out, *f)
			continue
		}
		detail := f.Detail
		if detail == "" {
			detail = f.Message
		}
		if name := undeclaredName(detail); name != "" {
			if reported[name] {
				continue
			}
			if idx.HasEnum(name) && hasEnumMemberRef(content, name) {
				continue
			}
		}
		if name := undefinedField(detail); name != "" && reported[name] {
			continue
		}
		if isCheckMacroError(detail, idx.MacroKeywords()) && reportedMacroTypo(reported, idx.MacroKeywords()) {
			continue
		}
		out = append(out, *f)
	}
	return out
}

func isCheckMacroError(msg string, macros []string) bool {
	for _, m := range macros {
		if strings.Contains(msg, m+"(") {
			return true
		}
	}
	return false
}

func reportedMacroTypo(reported map[string]bool, macros []string) bool {
	for name := range reported {
		lb := strings.ToLower(name)
		if lb == "" {
			continue
		}
		for _, mk := range macros {
			if celtspell.WithinThreshold(celtspell.Levenshtein(lb, strings.ToLower(mk)), len(lb)) {
				return true
			}
		}
	}
	return false
}

// ReportedNames returns the set of tokens already covered by an unknown-name
// finding, used to dedup compile errors.
func ReportedNames(findings []celtlint.Finding, content string) map[string]bool {
	reported := map[string]bool{}
	for i := range findings {
		f := &findings[i]
		if f.Code != celtlint.CodeUnknownField && f.Code != celtlint.CodeUnknownFunction && f.Code != celtlint.CodeUnknownIdentifier {
			continue
		}
		start := clampOffset(celtsource.PosToOffset(content, f.Start), content)
		end := clampOffset(celtsource.PosToOffset(content, f.End), content)
		if end > start && end <= len(content) {
			reported[content[start:end]] = true
		}
	}
	return reported
}

func undeclaredName(msg string) string {
	const marker = "undeclared reference to `"
	if i := strings.Index(msg, marker); i >= 0 {
		rest := msg[i+len(marker):]
		if j := strings.IndexByte(rest, '`'); j >= 0 {
			return rest[:j]
		}
	}
	return ""
}

func undefinedField(msg string) string {
	const marker = "undefined field `"
	if i := strings.Index(msg, marker); i >= 0 {
		rest := msg[i+len(marker):]
		if j := strings.IndexByte(rest, '`'); j >= 0 {
			return rest[:j]
		}
	}
	return ""
}

func hasEnumMemberRef(content, name string) bool {
	prefix := name + "."
	i := 0
	for i < len(content) {
		idx := strings.Index(content[i:], prefix)
		if idx < 0 {
			return false
		}
		after := i + idx + len(prefix)
		if after < len(content) && celtsource.IsIdentStart(content[after]) {
			return true
		}
		i = after
	}
	return false
}

// clampOffset bounds a byte offset to the content length.
func clampOffset(off int, content string) int {
	if off < 0 {
		return 0
	}
	if off > len(content) {
		return len(content)
	}
	return off
}
