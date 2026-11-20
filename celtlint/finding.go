package celtlint

import (
	"bytes"
	"fmt"
	"strings"

	"cel.dev/cel-go/common"
	"github.com/fatih/color"
	"github.com/nobuyuki/srcspan/diagnostic"
	"github.com/nobuyuki/srcspan/files"
	"github.com/nobuyuki/srcspan/term"

	"github.com/udaltsovkdotru/celt/celtsource"
)

type Position = celtsource.Position

type Span struct {
	Start   Position
	End     Position
	Message string
}

const (
	CodeManualMacro         = "manual-macro"
	CodeRawEnumValue        = "raw-enum-value"
	CodeOddComparison       = "odd-comparison"
	CodeInvalidResultType   = "invalid-result-type"
	CodeCompileError        = "compile-error"
	CodeTypeMismatch        = "type-mismatch"
	CodeNoMatchingOverload  = "no-matching-overload"
	CodeUndeclaredReference = "undeclared-reference"
	CodeUndefinedField      = "undefined-field"
	CodeUnexpectedToken     = "unexpected-token"
	CodeUnknownEnumVariant  = "unknown-enum-variant"
	CodeUnknownField        = "unknown-field"
	CodeUnknownFunction     = "unknown-function"
	CodeUnknownIdentifier   = "unknown-identifier"

	CodeMissingHasCheck = "missing-has-check"
)

type Finding struct {
	Level   Level
	Code    string
	Message string
	Start   Position
	End     Position
	Spans   []Span
	Hint    string
	Fix     string

	SpanMessage string
	FixStart    Position
	FixEnd      Position

	Detail string
}

func (f *Finding) FixRange() (start, end Position) {
	if f.FixStart != (Position{}) || f.FixEnd != (Position{}) {
		return f.FixStart, f.FixEnd
	}
	return f.Start, f.End
}

func (f *Finding) IsError() bool { return f.Level == LevelError }

func (f *Finding) HintMessage() string {
	if f.Fix == "" {
		return f.Hint
	}
	if strings.ContainsRune(f.Fix, '\n') {
		return UseInstead(f.HintCallee())
	}
	return fmt.Sprintf("use %#q instead", f.Fix)
}

// UseInstead renders the "use X instead" guidance shared by code-action titles
// and multi-line fix hints.
func UseInstead(target string) string {
	return fmt.Sprintf("use %#q instead", target)
}

func (f *Finding) HintCallee() string {
	if i := strings.IndexByte(f.Fix, '('); i > 0 {
		return f.Fix[:i]
	}
	return f.Fix
}

func (f *Finding) IsFix() bool {
	return f.Fix != ""
}

func fixHint(f *Finding) string {
	fs, _ := f.FixRange()
	col := fs.Char
	if col <= 0 || !strings.ContainsRune(f.Fix, '\n') {
		return f.Fix
	}
	pad := strings.Repeat(" ", col)
	lines := strings.Split(f.Fix, "\n")
	for i := 1; i < len(lines); i++ {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

func (f *Finding) FixText() string { return fixHint(f) }

func RenderFindings(src common.Source, diags []Finding) string {
	var b strings.Builder
	for i := range diags {
		b.WriteString(RenderFinding(src, &diags[i]))
		b.WriteString("\n\n")
	}
	return strings.TrimSuffix(b.String(), "\n\n")
}

func RenderFinding(src common.Source, f *Finding) string {
	content := src.Content()
	fs := files.NewSimpleFiles()
	fileID := fs.Add(src.Description(), content)

	var sev *diagnostic.Diagnostic
	switch f.Level {
	case LevelError:
		sev = diagnostic.NewError()
	default:
		sev = diagnostic.NewWarning()
	}
	d := sev.WithCode(f.Code).WithMessage(f.Message)
	label := func(t *diagnostic.Diagnostic, s, e Position, msg string, style diagnostic.LabelStyle) *diagnostic.Diagnostic {
		start := clampOffset(celtsource.PosToOffset(content, s), content)
		end := clampOffset(celtsource.PosToOffset(content, e), content)
		if end <= start {
			end = start + 1
			if end > len(content) {
				end = len(content)
			}
		}
		lbl := diagnostic.NewLabel(style, fileID, files.Range{Start: start, End: end})
		if msg != "" {
			lbl = lbl.WithMessage(msg)
		}
		return t.WithLabel(lbl)
	}
	d = label(d, f.Start, f.End, f.SpanMessage, diagnostic.Primary)
	for _, sp := range f.Spans {
		d = label(d, sp.Start, sp.End, sp.Message, diagnostic.Secondary)
	}
	if f.Hint != "" || f.Fix != "" || f.Detail != "" {
		d = d.WithNotes([]string{noteText(f)})
	}

	var b strings.Builder
	b.WriteString(renderDiagnostic(&fs, d, srcspanStyles(f)))
	return b.String()
}

func noteText(f *Finding) string {
	switch {
	case f.Detail != "" && f.Detail != f.Message:
		return f.Detail
	default:
		return hintNote(f)
	}
}

func renderDiagnostic(fs files.Files, d *diagnostic.Diagnostic, styles *term.Styles) string {
	var buf bytes.Buffer
	var writer term.WriteStyle
	if Color {
		writer = term.NewStylesWriter(&buf, styles)
	} else {
		writer = term.NewStylesWriter(&buf, term.NoColor())
	}
	cfg := term.DefaultConfig()
	cfg.Chars = srcspanChars()
	if err := term.EmitToWriteStyle(writer, &cfg, fs, d); err != nil {
		return d.Message
	}
	return buf.String()
}

func hintNote(f *Finding) string {
	return HintLabel() + f.HintMessage()
}

func clampOffset(off int, content string) int {
	if off < 0 {
		return 0
	}
	if off > len(content) {
		return len(content)
	}
	return off
}

func srcspanChars() term.Chars {
	c := term.Ascii()
	c.SingleSecondaryCaret = '^'
	c.MultiSecondaryCaretStart = '|'
	c.MultiSecondaryCaretEnd = '^'
	c.MultiBottomLeft = '|'
	c.MultiBottom = '_'
	return c
}

func srcspanStyles(f *Finding) *term.Styles {
	s := term.DefaultStyles()
	blueBold := color.New(color.FgHiBlue, color.Bold)
	s.SourceBorder = blueBold
	s.LineNumber = blueBold
	if f.Level == LevelError {
		s.PrimaryLabelError = color.New(color.FgHiRed, color.Bold)
	} else {
		s.PrimaryLabelWarning = color.New(color.FgHiYellow, color.Bold)
	}

	return s
}
