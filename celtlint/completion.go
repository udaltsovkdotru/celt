package celtlint

import (
	"fmt"
	"sort"
	"strings"

	"github.com/udaltsovkdotru/celt/celtindex"
	"github.com/udaltsovkdotru/celt/celtsource"
)

type CompletionItem struct {
	Label            string
	Kind             string
	Documentation    string
	InsertText       string
	InsertTextFormat int
}

const (
	InsertTextFormatSnippet = 2
)

const (
	CompletionKindField    = string(celtindex.KindField)
	CompletionKindFunction = string(celtindex.KindFunction)
	CompletionKindVariable = string(celtindex.KindVariable)
	CompletionKindMacro    = string(celtindex.KindMacro)
	CompletionKindEnum     = string(celtindex.KindEnum)
	CompletionKindValue    = string(celtindex.KindValue)
)

func (a *Analyzer) Completion(content string, line, char int) []CompletionItem {
	a.ensureBuilt()
	off, lineStart, _, ok := celtsource.LineByteColOffset(content, line, char)
	if !ok {
		return nil
	}

	tokStart := off
	for tokStart > lineStart && celtsource.IsIdentByte(content[tokStart-1]) {
		tokStart--
	}
	prefix := content[tokStart:off]

	dot := -1
	if tokStart > lineStart && content[tokStart-1] == '.' {
		dot = tokStart - 1
	} else if tokStart == off && off > lineStart && content[off-1] == '.' {
		dot = off - 1
	}
	if dot >= 0 {
		return a.memberItems(content, lineStart, dot, prefix)
	}

	if len(prefix) < minTopLevelPrefix {
		return nil
	}
	return a.topLevel(prefix)
}

const minTopLevelPrefix = 2

// lastSegment returns the final dot-separated component of a receiver path.
func lastSegment(path string) string {
	if i := strings.LastIndexByte(path, '.'); i >= 0 {
		return path[i+1:]
	}
	return path
}

func (a *Analyzer) topLevel(prefix string) []CompletionItem {
	var out []CompletionItem
	for _, c := range a.index.Candidates() {
		switch c.Kind {
		case celtindex.KindVariable, celtindex.KindMacro, celtindex.KindEnum, celtindex.KindValue:
			if it, ok := a.item(c, prefix, "", ""); ok {
				out = append(out, it)
			}
		}
	}
	return dedupeItems(out)
}

func (a *Analyzer) memberItems(content string, lineStart, dot int, prefix string) []CompletionItem {
	recv, ok := receiverTextBefore(content, lineStart, dot)
	if !ok {
		return nil
	}
	var cands []celtindex.Candidate
	docType := a.resolveReceiverType(recv)
	if docType != "" {
		cands = a.index.TypeMembers(docType)
		if cands == nil {
			if _, c := a.index.MemberCandidates(docType); c != nil {
				cands = c
			}
		}
	}
	if cands == nil {
		_, cands = a.index.MemberCandidatesPath(recv)
	}
	// Function receivers (list/string fields) are keyed by their bare field
	// name, so scope member-function completion by the last path segment (e.g.
	// the list<string> functions of `w.tags`).
	recvElem := a.index.ReceiverElem(lastSegment(recv))
	if cands == nil {
		cands = a.index.FnCands(lastSegment(recv))
	}
	var out []CompletionItem
	for _, c := range cands {
		if it, ok := a.item(c, prefix, recvElem, docType); ok {
			out = append(out, it)
		}
	}
	return dedupeItems(out)
}

func (a *Analyzer) item(c celtindex.Candidate, prefix, receiverElem, docType string) (CompletionItem, bool) {
	if prefix != "" && !strings.HasPrefix(c.Name, prefix) {
		return CompletionItem{}, false
	}
	doc := a.hoverFor(c.Name, receiverElem)
	// A field's documentation is keyed by its receiver's type, so fields that
	// share a bare name across message types resolve to the right signature.
	if docType != "" && c.Kind == celtindex.KindField {
		if d, ok := a.docs.fieldFor(docType, c.Name); ok {
			doc = renderHover(d)
		}
	}
	it := CompletionItem{
		Label:         c.Name,
		Kind:          string(c.Kind),
		Documentation: doc,
	}
	if c.Kind == celtindex.KindFunction {
		if a.fnHasArgs(c.Name, receiverElem) {
			it.InsertText = fmt.Sprintf("%s($0)", c.Name)
			it.InsertTextFormat = InsertTextFormatSnippet
		} else {
			it.InsertText = fmt.Sprintf("%s()", c.Name)
		}
	}
	return it, true
}

// resolveReceiverType returns the CEL type name of a receiver expression,
// resolving a dotted path via the index and falling back to compiling the
// expression (which handles indexed/complex receivers such as `[person][0]`).
func (a *Analyzer) resolveReceiverType(recv string) string {
	if t := a.index.ReceiverTypeName(recv); t != "" {
		return t
	}
	if a.env == nil {
		return ""
	}
	ast, iss := a.env.Compile(recv)
	if iss != nil && iss.Err() != nil || ast == nil {
		return ""
	}
	return celTypeName(ast.OutputType())
}

// receiverDotAt returns the byte offset of the '.' that begins the member
// access ending at (or after) off, or -1 when the cursor is not on a field
// select's receiver position. lineStart bounds the current line.
func receiverDotAt(content string, lineStart, off int) int {
	i := off
	for i > lineStart && celtsource.IsIdentByte(content[i-1]) {
		i--
	}
	if i <= lineStart || content[i-1] != '.' {
		return -1
	}
	return i - 1
}

// receiverTextBefore returns the CEL receiver expression text immediately
// preceding a member-select dot at dot, e.g. "person", "a.child", or
// "[person][0]". It reports false when no receiver precedes the dot.
func receiverTextBefore(content string, lineStart, dot int) (string, bool) {
	i := dot - 1
	for i >= lineStart && celtsource.IsSpace(content[i]) {
		i--
	}
	if i < lineStart {
		return "", false
	}
	var start int
	depth := 0
	for j := i; j >= lineStart; j-- {
		c := content[j]
		switch c {
		case ']', ')', '}':
			depth++
		case '[', '(', '{':
			if depth > 0 {
				depth--
			} else {
				start = j
				return strings.TrimSpace(content[start:dot]), true
			}
		default:
			if depth == 0 && (celtsource.IsSpace(c) || isMemberBoundary(c)) {
				start = j + 1
				return strings.TrimSpace(content[start:dot]), true
			}
		}
	}
	recv := strings.TrimSpace(content[lineStart:dot])
	if recv == "" {
		return "", false
	}
	return recv, true
}

// isMemberBoundary reports whether c ends a receiver expression at the top
// level (an operator, separator, or grouping/collection delimiter).
func isMemberBoundary(c byte) bool {
	switch c {
	case ',', ';', '=', '!', '<', '>', '+', '-', '*', '/', '%', '&', '|', '^', '?', ':', '(', '{':
		return true
	}
	return false
}

func (a *Analyzer) fnHasArgs(name, receiverElem string) bool {
	d, ok := a.docs.functions[name]
	if !ok || len(d.overloads) == 0 {
		return false
	}
	// Choose the overload matching the receiver's element type (e.g. the
	// list<int> overload of `pick` for `w.numbers`) rather than always the
	// first declared overload, so the argument placeholder matches the
	// completion that would actually be inserted.
	overloads := filterOverloads(d.overloads, receiverElem)
	if len(overloads) == 0 {
		return false
	}
	return fnArgsNonEmpty(overloads[0].sig)
}

func fnArgsNonEmpty(sig string) bool {
	recvClose := strings.IndexByte(sig, ')')
	if recvClose < 0 {
		return false
	}
	argsOpen := strings.IndexByte(sig[recvClose:], '(')
	if argsOpen < 0 {
		return false
	}
	argsOpen += recvClose
	argsClose := strings.IndexByte(sig[argsOpen:], ')')
	if argsClose < 0 {
		return false
	}
	argsClose += argsOpen
	return strings.TrimSpace(sig[argsOpen+1:argsClose]) != ""
}

func (a *Analyzer) hoverFor(name, receiverElem string) string {
	return a.docs.Lookup(name, receiverElem)
}

func dedupeItems(items []CompletionItem) []CompletionItem {
	seen := map[string]bool{}
	var out []CompletionItem
	for _, it := range items {
		if seen[it.Label] {
			continue
		}
		seen[it.Label] = true
		out = append(out, it)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}
