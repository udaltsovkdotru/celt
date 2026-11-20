package actions

import (
	"testing"

	"go.lsp.dev/protocol"

	"github.com/udaltsovkdotru/celt/celtfmt"
	"github.com/udaltsovkdotru/celt/celtlint"
	"github.com/udaltsovkdotru/celt/celtls"
)

// acEmptyCfg is a minimal celtfmt.Config that contributes no rules; the
// printer then falls back to its defaults (two-space indent, 100-column
// width). It exists so All and newFormatAction receive a non-nil Config.
type acEmptyCfg struct{}

func (acEmptyCfg) Rules() []celtfmt.FormatRule { return nil }

// acURI is the synthetic document URI used across the action tests.
const acURI = protocol.DocumentURI("file:///test.cel")

const (
	acFixStartsWith   = "startsWith(y)"
	acTitleStartsWith = "use `startsWith` instead"
)

func TestAll(t *testing.T) {
	t.Run("nilConfigOmitsFormatAction", func(t *testing.T) {
		acts := All(nil)
		if len(acts) != 1 {
			t.Fatalf("All(nil) = %d actions, want 1", len(acts))
		}
		if got := acts[0].Meta().Name; got != "quick-fix" {
			t.Errorf("actions[0].Name = %q, want quick-fix", got)
		}
		if got := acts[0].Meta().Desc; got == "" {
			t.Error("quick-fix action has empty description")
		}
	})
	t.Run("configAppendsFormatAction", func(t *testing.T) {
		acts := All(acEmptyCfg{})
		if len(acts) != 2 {
			t.Fatalf("All(cfg) = %d actions, want 2", len(acts))
		}
		want := []string{"quick-fix", "format"}
		for i, w := range want {
			if got := acts[i].Meta().Name; got != w {
				t.Errorf("actions[%d].Name = %q, want %q", i, got, w)
			}
		}
	})
}

func TestFormatActionApply(t *testing.T) {
	act := newFormatAction(acEmptyCfg{})

	t.Run("unformattedProducesSourceFormatEdit", func(t *testing.T) {
		got := act.Apply(celtls.ActionContext{URI: acURI, Content: "1 + 2"})
		if len(got) != 1 {
			t.Fatalf("Apply = %d actions, want 1", len(got))
		}
		a := got[0]
		if a.Title != "Format document" {
			t.Errorf("Title = %q, want %q", a.Title, "Format document")
		}
		if a.Kind != "source.format" {
			t.Errorf("Kind = %q, want %q", a.Kind, "source.format")
		}
		if a.Edit == nil {
			t.Fatal("Edit is nil")
		}
		edits, ok := a.Edit.Changes[acURI]
		if !ok {
			t.Fatalf("Changes missing URI %q", acURI)
		}
		if len(edits) != 1 {
			t.Fatalf("Changes[uri] = %d edits, want 1", len(edits))
		}
		e := edits[0]
		if e.NewText != "1 + 2\n" {
			t.Errorf("NewText = %q, want %q", e.NewText, "1 + 2\n")
		}
		wantRange := protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 5}}
		if e.Range != wantRange {
			t.Errorf("Range = %+v, want %+v", e.Range, wantRange)
		}
	})

	t.Run("alreadyFormattedReturnsNone", func(t *testing.T) {
		got := act.Apply(celtls.ActionContext{URI: acURI, Content: "1 + 2\n"})
		if len(got) != 0 {
			t.Errorf("Apply(formatted) = %d actions, want 0", len(got))
		}
	})

	t.Run("invalidContentReturnsNone", func(t *testing.T) {
		got := act.Apply(celtls.ActionContext{URI: acURI, Content: "1 +"})
		if len(got) != 0 {
			t.Errorf("Apply(invalid) = %d actions, want 0", len(got))
		}
	})

	t.Run("emptyContentReturnsNone", func(t *testing.T) {
		got := act.Apply(celtls.ActionContext{URI: acURI, Content: ""})
		if len(got) != 0 {
			t.Errorf("Apply(empty) = %d actions, want 0", len(got))
		}
	})
}

// acQuickFixContent is a one-line CEL expression where "startsWith" occupies
// characters 2..11 of line 0.
const acQuickFixContent = `x.startsWith("foo")` + "\n"

var (
	acFindingStart = celtlint.Position{Line: 0, Char: 2}
	acFindingEnd   = celtlint.Position{Line: 0, Char: 12}
)

func TestQuickFixActionApply(t *testing.T) {
	act := newQuickFixAction()

	t.Run("overlappingFixProducesQuickFix", func(t *testing.T) {
		f := celtlint.Finding{
			Code:    "manual-macro",
			Message: "manual macro",
			Start:   acFindingStart,
			End:     acFindingEnd,
			Fix:     acFixStartsWith,
		}
		got := act.Apply(celtls.ActionContext{
			URI:      acURI,
			Content:  acQuickFixContent,
			Range:    acOverlappingRange(),
			Findings: []celtlint.Finding{f},
		})
		if len(got) != 1 {
			t.Fatalf("Apply = %d actions, want 1", len(got))
		}
		a := got[0]
		if a.Title != acTitleStartsWith {
			t.Errorf("Title = %q, want %q", a.Title, acTitleStartsWith)
		}
		if a.Kind != protocol.QuickFix {
			t.Errorf("Kind = %q, want %q", a.Kind, protocol.QuickFix)
		}
		if a.IsPreferred {
			t.Error("IsPreferred = true, want false (at most one preferred action per request)")
		}
		if a.Edit == nil {
			t.Fatal("Edit is nil")
		}
		edits := a.Edit.Changes[acURI]
		if len(edits) != 1 {
			t.Fatalf("Changes[uri] = %d edits, want 1", len(edits))
		}
		e := edits[0]
		if e.NewText != acFixStartsWith {
			t.Errorf("NewText = %q, want %q", e.NewText, acFixStartsWith)
		}
		wantRange := protocol.Range{Start: protocol.Position{Line: 0, Character: 2}, End: protocol.Position{Line: 0, Character: 12}}
		if e.Range != wantRange {
			t.Errorf("Range = %+v, want %+v", e.Range, wantRange)
		}
	})

	t.Run("nonOverlappingFindingSkipped", func(t *testing.T) {
		f := celtlint.Finding{Fix: acFixStartsWith, Start: acFindingStart, End: acFindingEnd}
		got := act.Apply(celtls.ActionContext{
			URI:      acURI,
			Content:  acQuickFixContent,
			Range:    protocol.Range{Start: protocol.Position{Line: 0, Character: 20}, End: protocol.Position{Line: 0, Character: 25}},
			Findings: []celtlint.Finding{f},
		})
		if len(got) != 0 {
			t.Errorf("Apply = %d actions, want 0 for non-overlapping finding", len(got))
		}
	})

	t.Run("noFixFindingSkipped", func(t *testing.T) {
		f := celtlint.Finding{Start: acFindingStart, End: acFindingEnd}
		got := act.Apply(celtls.ActionContext{
			URI:      acURI,
			Content:  acQuickFixContent,
			Range:    acOverlappingRange(),
			Findings: []celtlint.Finding{f},
		})
		if len(got) != 0 {
			t.Errorf("Apply = %d actions, want 0 for finding without fix", len(got))
		}
	})
}

func TestQuickFixActionApplyFilter(t *testing.T) {
	act := newQuickFixAction()
	fixable := celtlint.Finding{Fix: acFixStartsWith, Start: acFindingStart, End: acFindingEnd}
	nofix := celtlint.Finding{
		Start: celtlint.Position{Line: 0, Char: 0},
		End:   celtlint.Position{Line: 0, Char: 1},
	}
	offfix := celtlint.Finding{
		Fix:   "somethingElse(z)",
		Start: celtlint.Position{Line: 0, Char: 14},
		End:   celtlint.Position{Line: 0, Char: 18},
	}
	got := act.Apply(celtls.ActionContext{
		URI:      acURI,
		Content:  acQuickFixContent,
		Range:    acOverlappingRange(),
		Findings: []celtlint.Finding{fixable, nofix, offfix},
	})
	if len(got) != 1 {
		t.Fatalf("Apply = %d actions, want 1", len(got))
	}
	if got[0].Title != acTitleStartsWith {
		t.Errorf("Title = %q, want the startsWith fix", got[0].Title)
	}
}

func TestQuickFixActionApplyNoFindings(t *testing.T) {
	act := newQuickFixAction()
	got := act.Apply(celtls.ActionContext{
		URI:     acURI,
		Content: acQuickFixContent,
		Range:   protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 5}},
	})
	if len(got) != 0 {
		t.Errorf("Apply = %d actions, want 0 with no findings", len(got))
	}
}

func TestQuickFixActionApplyTitle(t *testing.T) {
	act := newQuickFixAction()

	t.Run("titleFallsBackToFullFix", func(t *testing.T) {
		f := celtlint.Finding{Fix: "startsWith", Start: acFindingStart, End: acFindingEnd}
		got := act.Apply(celtls.ActionContext{
			URI:      acURI,
			Content:  acQuickFixContent,
			Range:    acOverlappingRange(),
			Findings: []celtlint.Finding{f},
		})
		if len(got) != 1 {
			t.Fatalf("Apply = %d actions, want 1", len(got))
		}
		// Fix has no '(' so HintCallee returns the whole fix; title and text match.
		if got[0].Title != acTitleStartsWith {
			t.Errorf("Title = %q, want %q", got[0].Title, acTitleStartsWith)
		}
		if e := got[0].Edit.Changes[acURI][0]; e.NewText != "startsWith" {
			t.Errorf("NewText = %q, want %q", e.NewText, "startsWith")
		}
	})

	t.Run("multilineFixIndentsContinuation", func(t *testing.T) {
		f := celtlint.Finding{Fix: "a\nb", Start: acFindingStart, End: acFindingEnd, FixStart: acFindingStart}
		got := act.Apply(celtls.ActionContext{
			URI:      acURI,
			Content:  acQuickFixContent,
			Range:    acOverlappingRange(),
			Findings: []celtlint.Finding{f},
		})
		if len(got) != 1 {
			t.Fatalf("Apply = %d actions, want 1", len(got))
		}
		e := got[0].Edit.Changes[acURI][0]
		if e.NewText != "a\n  b" {
			t.Errorf("NewText = %q, want %q", e.NewText, "a\n  b")
		}
	})
}

// acOverlappingRange returns an LSP range that covers the fixable region of
// acQuickFixContent (characters 0..12 of line 0).
func acOverlappingRange() protocol.Range {
	return protocol.Range{Start: protocol.Position{Line: 0, Character: 0}, End: protocol.Position{Line: 0, Character: 13}}
}
