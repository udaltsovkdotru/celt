package celtlint

import (
	"fmt"
	"strings"

	"cel.dev/cel-go/common"
)

// Stats aggregates finding counts for a lint run.
type Stats struct {
	Warnings int
	Errors   int
	Fixable  int
	Checked  int
}

// Summary renders a human summary of a lint run's Stats.
func Summary(s Stats, binName string) string {
	if binName == "" {
		binName = "celt"
	}
	plural := func(n int, w string) string {
		if n == 1 {
			return w
		}
		return w + "s"
	}
	var b strings.Builder
	if s.Warnings > 0 {
		msg := fmt.Sprintf("%s generated %d %s", binName, s.Warnings, plural(s.Warnings, "warning"))
		if s.Fixable > 0 {
			msg += fmt.Sprintf(" (run `%s lint --fix --dir <dir>` to apply %d %s)",
				binName, s.Fixable, plural(s.Fixable, "suggestion"))
		}
		fmt.Fprintf(&b, "%s: %s\n",
			colorize(bold("warning"), ansiYellow),
			WhiteBold(msg))
	}
	if s.Errors > 0 {
		errMsg := fmt.Sprintf("check failed due to %d previous %s", s.Errors, plural(s.Errors, "error"))
		if s.Warnings > 0 {
			errMsg += fmt.Sprintf("; %d %s emitted", s.Warnings, plural(s.Warnings, "warning"))
		}
		fmt.Fprintf(&b, "%s: %s", colorize(bold("error"), ansiRed), WhiteBold(errMsg))
		return b.String()
	}
	fmt.Fprintf(&b, "    %s checking %d %s",
		Green("Finished"), s.Checked, plural(s.Checked, "file"))
	return b.String()
}

// LintedSource is a source with its findings, for bulk rendering.
type LintedSource struct {
	Name     string
	Content  string
	Findings []Finding
}

// RenderAll renders all non-empty findings across a set of linted sources.
func RenderAll(ls []LintedSource) string {
	var b strings.Builder
	for _, s := range ls {
		src := common.NewStringSource(s.Content, s.Name)
		if r := RenderFindings(src, s.Findings); r != "" {
			b.WriteString(r)
			b.WriteString("\n\n")
		}
	}
	return strings.TrimSuffix(b.String(), "\n\n")
}
