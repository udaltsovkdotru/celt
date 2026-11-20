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

func (a *Analyzer) topLevel(prefix string) []CompletionItem {
	var out []CompletionItem
	for _, c := range a.index.Candidates() {
		switch c.Kind {
		case celtindex.KindVariable, celtindex.KindMacro, celtindex.KindEnum, celtindex.KindValue:
			if it, ok := a.item(c, prefix, ""); ok {
				out = append(out, it)
			}
		}
	}
	return dedupeItems(out)
}

func (a *Analyzer) memberItems(content string, lineStart, dot int, prefix string) []CompletionItem {
	recvEnd := dot
	recvStart := recvEnd - 1
	for recvStart >= lineStart && celtsource.IsIdentByte(content[recvStart]) {
		recvStart--
	}
	recv := content[recvStart+1 : recvEnd]

	_, cands := a.index.MemberCandidates(recv)
	recvElem := a.index.ReceiverElem(recv)
	var out []CompletionItem
	for _, c := range cands {
		if it, ok := a.item(c, prefix, recvElem); ok {
			out = append(out, it)
		}
	}
	return dedupeItems(out)
}

func (a *Analyzer) item(c celtindex.Candidate, prefix, receiverElem string) (CompletionItem, bool) {
	if prefix != "" && !strings.HasPrefix(c.Name, prefix) {
		return CompletionItem{}, false
	}
	it := CompletionItem{
		Label:         c.Name,
		Kind:          string(c.Kind),
		Documentation: a.hoverFor(c.Name, receiverElem),
	}
	if c.Kind == celtindex.KindFunction {
		if a.fnHasArgs(c.Name) {
			it.InsertText = fmt.Sprintf("%s($0)", c.Name)
			it.InsertTextFormat = InsertTextFormatSnippet
		} else {
			it.InsertText = fmt.Sprintf("%s()", c.Name)
		}
	}
	return it, true
}

func (a *Analyzer) fnHasArgs(name string) bool {
	d, ok := a.docs.functions[name]
	if !ok || len(d.overloads) == 0 {
		return false
	}
	return fnArgsNonEmpty(d.overloads[0].sig)
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
